package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// parseFileMode parses a Unix file-mode string for the optional `mode`
// param on write_file / edit_file. Accepted forms: "755", "0755",
// "0o755". Any setuid / setgid / sticky bits are masked off — the agent
// has no business setting those, and a typo'd "4755" silently granting
// setuid would be a real footgun. Empty input returns 0, signalling
// "caller should use the default for this operation".
func parseFileMode(s string) (os.FileMode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	s = strings.TrimPrefix(s, "0o")
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("mode %q is not a valid octal file mode (e.g. \"0755\")", s)
	}
	return os.FileMode(n) & 0o777, nil
}

const maxOutput = 20_000

// maxFileReadBytes caps the byte size of a single read_file / read_minified_file
// response. It applies to the already-formatted output (line-numbered), so it
// bounds what ends up in the model's context regardless of raw file size. When
// the cap is hit the model sees a trailing notice and can re-read the specific
// range it needs with offset/limit.
const maxFileReadBytes = 256 * 1024

// --- Permission model ---
// _DANGEROUS_TOOLS is currently empty (no tools require confirmation by default)
var dangerousTools = map[string]bool{}

// --- Brain update helpers ---

var sourceExtensions = map[string]bool{
	".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
	".md": true, ".txt": true, ".yml": true, ".yaml": true, ".json": true,
	".toml": true, ".cfg": true, ".ini": true,
}

var toolSkipDirs = map[string]bool{
	"node_modules": true, "__pycache__": true, "build": true,
	"dist": true, "target": true, ".git": true,
}

func shouldTriggerUpdate(filePath string) bool {
	parts := strings.Split(filePath, string(filepath.Separator))
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return false
		}
		if toolSkipDirs[part] {
			return false
		}
	}
	ext := filepath.Ext(filePath)
	return sourceExtensions[ext]
}

// flushBrainUpdate triggers an immediate brain update for the given files.
func flushBrainUpdate(s *Server, files []string) {
	handler := s.GetHandler("brain.update_files")
	if handler == nil {
		LogWarn("brain.update_files handler not registered, skipping update")
		return
	}

	LogInfo("Auto brain update for %d file(s): %v", len(files), files)
	response, err := handler(map[string]any{
		"command": "brain.update_files",
		"params": map[string]any{
			"files":        files,
			"project_path": ".",
		},
	})
	if err != nil {
		LogError("Brain update error: %v", err)
		return
	}
	status, _ := response["status"].(string)
	if status == "ok" {
		data, _ := response["data"].(map[string]any)
		LogInfo("Brain update complete (%v)", data["duration_seconds"])
	} else {
		msg, _ := response["message"].(string)
		LogError("Brain update failed: %s", msg)
	}
}

// resolvePathInCwd resolves a path relative to a given working directory.
// Absolute paths outside cwd are remapped by suffix-matching against cwd.
// Returns an error if the resolved path escapes the working directory.
func resolvePathInCwd(cwd, path string) (string, error) {
	return resolvePathInAllowed(cwd, nil, path)
}

// resolvePathInAllowed resolves a path, allowing access to cwd and any extra
// allowed directories. Absolute paths outside all allowed roots are remapped
// by suffix-matching against cwd or rejected.
func resolvePathInAllowed(cwd string, allowedDirs []string, path string) (string, error) {
	path = strings.TrimSpace(path)
	var resolved string
	if !filepath.IsAbs(path) {
		resolved = filepath.Clean(filepath.Join(cwd, path))
	} else if isUnderAny(path, cwd, allowedDirs) {
		// Already under cwd or an allowed directory — use as-is.
		resolved = path
	} else if isSystemPath(path) {
		// Platform system path (e.g. /tmp) — allowed by policy without remapping.
		resolved = path
	} else {
		// Absolute path outside allowed roots — try to remap by finding a
		// matching suffix under cwd (handles LLM using wrong project root).
		// Require ≥2 suffix segments to avoid false positives on system paths.
		sep := string(filepath.Separator)
		parts := strings.Split(path, sep)
		remapped := false
		for i := 2; i < len(parts)-1; i++ {
			suffix := strings.Join(parts[i:], sep)
			candidate := filepath.Join(cwd, suffix)
			dir := filepath.Dir(candidate)
			if _, err := os.Stat(dir); err == nil {
				log.Printf("[tools] remapped path outside workdir: %s → %s", path, candidate)
				resolved = candidate
				remapped = true
				break
			}
		}
		if !remapped {
			return "", fmt.Errorf("path %s is outside working directory %s", path, cwd)
		}
	}

	// Resolve symlinks to prevent symlink-based escapes.
	resolvedReal, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// File may not exist yet (write_file) — resolve the parent directory
		// and reconstruct the full path for comparison.
		parentReal, perr := filepath.EvalSymlinks(filepath.Dir(resolved))
		if perr != nil {
			// EvalSymlinks unavailable (e.g. /private not traversable on some
			// macOS configurations). Fall back to component-level symlink
			// detection: walk each segment relative to cwd using os.Lstat
			// (which works even when EvalSymlinks fails) and check symlink
			// targets directly so escapes are still caught.
			if rel, relErr := filepath.Rel(cwd, resolved); relErr == nil && !strings.HasPrefix(rel, "..") {
				cur := cwd
				for _, part := range strings.Split(rel, string(filepath.Separator)) {
					if part == "" || part == "." {
						continue
					}
					cur = filepath.Join(cur, part)
					fi, statErr := os.Lstat(cur)
					if statErr != nil {
						break // path doesn't exist past this point
					}
					if fi.Mode()&os.ModeSymlink != 0 {
						target, readErr := os.Readlink(cur)
						if readErr != nil {
							return "", fmt.Errorf("path %s contains unreadable symlink", path)
						}
						if !filepath.IsAbs(target) {
							target = filepath.Join(filepath.Dir(cur), target)
						}
						target = filepath.Clean(target)
						if !isUnderAny(target, cwd, allowedDirs) {
							return "", fmt.Errorf("path %s resolves outside working directory", path)
						}
					}
				}
			}
			parentReal = filepath.Dir(resolved)
		}
		resolvedReal = filepath.Join(parentReal, filepath.Base(resolved))
	}

	if !isUnderAnyReal(resolvedReal, cwd, allowedDirs) && !isSystemPath(resolvedReal) {
		return "", fmt.Errorf("path %s resolves outside working directory", path)
	}

	return resolved, nil
}

// isUnderAny returns true if path is equal to or under root or any of the extra dirs.
func isUnderAny(path, root string, extraDirs []string) bool {
	if pathHasAncestor(path, root) {
		return true
	}
	for _, dir := range extraDirs {
		if pathHasAncestor(path, dir) {
			return true
		}
	}
	return false
}

// isUnderAnyReal checks resolved (symlink-free) paths against cwd and allowed dirs.
func isUnderAnyReal(resolvedReal, cwd string, allowedDirs []string) bool {
	cwdReal, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		cwdReal = cwd
	}
	if pathHasAncestor(resolvedReal, cwdReal) {
		return true
	}
	for _, dir := range allowedDirs {
		dirReal, err := filepath.EvalSymlinks(dir)
		if err != nil {
			dirReal = dir
		}
		if pathHasAncestor(resolvedReal, dirReal) {
			return true
		}
	}
	return false
}

var (
	cdPattern   = regexp.MustCompile(`\bcd\s+["']?([^\s"';&|]+)`)
	absPathPat  = regexp.MustCompile(`(?:^|\s|=|:)(/[^\s"';&|<>]+)`)
	tildePathPat = regexp.MustCompile(`(?:^|\s|=)~/([^\s"';&|<>]+)`)
)

// detectOutsidePaths extracts filesystem paths from a bash command and returns
// any that are outside cwd and the allowed directories. This is a best-effort
// heuristic — the sandbox remains the hard enforcement boundary.
func detectOutsidePaths(command, cwd string, allowedDirs []string) []string {
	seen := make(map[string]bool)
	var outside []string

	home := os.Getenv("HOME")

	check := func(raw string) {
		// Expand tilde.
		if strings.HasPrefix(raw, "~/") && home != "" {
			raw = filepath.Join(home, raw[2:])
		}
		if !filepath.IsAbs(raw) {
			return
		}
		raw = filepath.Clean(raw)

		// Unified "is this default-accessible?" check: cwd, $HOME, the
		// host's system directories (per platform_policy.go), or the
		// runtime allowed-dirs all flow through silently.
		if isAccessibleByDefault(raw, cwd, allowedDirs) {
			return
		}

		// Only flag paths that exist on disk to reduce false positives.
		if _, err := os.Stat(raw); err != nil {
			// Also check parent directory (for new file creation targets).
			if _, err := os.Stat(filepath.Dir(raw)); err != nil {
				return
			}
		}

		// Use the directory (not the file) for the approval.
		dir := raw
		if info, err := os.Stat(raw); err != nil || !info.IsDir() {
			dir = filepath.Dir(raw)
		}
		if !seen[dir] {
			seen[dir] = true
			outside = append(outside, dir)
		}
	}

	// Extract cd targets.
	for _, m := range cdPattern.FindAllStringSubmatch(command, -1) {
		check(m[1])
	}

	// Extract absolute paths.
	for _, m := range absPathPat.FindAllStringSubmatch(command, -1) {
		check(m[1])
	}

	// Extract tilde paths.
	for _, m := range tildePathPat.FindAllStringSubmatch(command, -1) {
		check("~/" + m[1])
	}

	return outside
}

// --- Tool implementations ---

// extractAllowedDirs pulls the allowed_dirs slice from tool params.
func extractAllowedDirs(params map[string]any) []string {
	raw, _ := params["allowed_dirs"].([]string)
	return raw
}

func toolOK(output string, isError bool) map[string]any {
	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"output":   output,
			"is_error": isError,
		},
	}
}

func toolConfirmResponse(tool string, params map[string]any) map[string]any {
	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"confirm": true,
			"tool":    tool,
			"params":  params,
		},
	}
}

func needsConfirmation(toolName string, params map[string]any) bool {
	if !dangerousTools[toolName] {
		return false
	}
	confirmed, _ := params["confirmed"].(bool)
	return !confirmed
}

// readFileImpl reads a file with line numbers. Supports offset/limit for partial reads.
func readFileImpl(cwd string, allowedDirs []string, path string, offset, limit *int) (string, error) {
	p, err := resolvePathInAllowed(cwd, allowedDirs, path)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(p); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", path)
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}

	text := string(raw)
	lines := strings.Split(text, "\n")

	start := 0
	if offset != nil && *offset >= 1 {
		start = *offset - 1
	}
	end := len(lines)
	if limit != nil {
		end = start + *limit
	}
	if start > len(lines) {
		start = len(lines)
	}
	if end > len(lines) {
		end = len(lines)
	}

	var numbered []string
	for i, line := range lines[start:end] {
		numbered = append(numbered, fmt.Sprintf("%5d\t%s", i+start+1, line))
	}

	return strings.Join(numbered, "\n"), nil
}

// capFileReadOutput truncates a read_file / read_minified_file response that
// exceeds maxFileReadBytes. The cut is made at the last newline under the cap
// so the output never ends mid-line, and a trailing notice tells the model how
// much was elided and how to fetch the rest.
func capFileReadOutput(output string) string {
	if len(output) <= maxFileReadBytes {
		return output
	}
	total := len(output)
	truncated := output[:maxFileReadBytes]
	if idx := strings.LastIndexByte(truncated, '\n'); idx > 0 {
		truncated = truncated[:idx]
	}
	return truncated + fmt.Sprintf(
		"\n\n... [file truncated: returned %d of %d bytes. Use offset/limit to read a specific range.]",
		len(truncated), total,
	)
}

// extToLang maps a file extension (e.g. ".go") to a Markdown language hint.
func extToLang(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".sh", ".bash":
		return "bash"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".md":
		return "markdown"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	default:
		return ""
	}
}

// formatWritePreview reads the written file and returns a markdown fenced code
// block containing the first 10 lines, suitable for use as a Detail field in
// EventToolResult. The language hint is derived from the file extension.
// Returns "" if the file cannot be read.
func formatWritePreview(filePath string) string {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	// Remove trailing empty line produced by Split when file ends with \n
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	n := len(lines)
	truncated := n > 10
	if truncated {
		n = 10
	}
	lang := extToLang(filepath.Ext(filePath))
	var b strings.Builder
	b.WriteString("```" + lang + "\n")
	for _, line := range lines[:n] {
		b.WriteString(line + "\n")
	}
	if truncated {
		b.WriteString("// ...\n")
	}
	b.WriteString("```")
	return b.String()
}

// writeFileImpl writes content to path. mode==0 means "use the default"
// — 0644 for new files, preserve the existing mode when overwriting an
// existing file (so re-writing a 0755 script doesn't silently demote it
// to 0644). A nonzero mode is applied verbatim.
func writeFileImpl(cwd string, allowedDirs []string, path, content string, mode os.FileMode) (string, error) {
	p, err := resolvePathInAllowed(cwd, allowedDirs, path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	effectiveMode := mode
	if effectiveMode == 0 {
		if st, err := os.Stat(p); err == nil {
			effectiveMode = st.Mode().Perm()
		} else {
			effectiveMode = 0o644
		}
	}

	encoded := []byte(content)
	if err := os.WriteFile(p, encoded, effectiveMode); err != nil {
		return "", err
	}
	// os.WriteFile only honours the mode arg when *creating* the file.
	// Chmod the existing file too so an explicit mode actually lands.
	if mode != 0 {
		if err := os.Chmod(p, mode); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(encoded), path), nil
}

// editFileImpl rewrites path with one occurrence of oldString replaced
// by newString. mode==0 means "preserve the existing file's mode" — the
// historical 0o644-on-edit was a quiet downgrade for executable scripts.
// A nonzero mode is applied verbatim.
func editFileImpl(cwd string, allowedDirs []string, path, oldString, newString string, mode os.FileMode) (string, int, error) {
	p, err := resolvePathInAllowed(cwd, allowedDirs, path)
	if err != nil {
		return "", 0, err
	}
	st, err := os.Stat(p)
	if os.IsNotExist(err) {
		return "", 0, fmt.Errorf("file not found: %s", path)
	}
	if err != nil {
		return "", 0, err
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		return "", 0, err
	}

	text := string(raw)
	count := strings.Count(text, oldString)
	if count == 0 {
		return "", 0, fmt.Errorf("old_string not found in file")
	}
	if count > 1 {
		return "", 0, fmt.Errorf("old_string found %d times (must be unique)", count)
	}

	// Compute line offset before writing so the diff preview shows real file line numbers.
	lineOffset := 0
	if idx := strings.Index(text, oldString); idx >= 0 {
		lineOffset = strings.Count(text[:idx], "\n")
	}

	effectiveMode := mode
	if effectiveMode == 0 {
		effectiveMode = st.Mode().Perm()
	}

	newText := strings.Replace(text, oldString, newString, 1)
	newRaw := []byte(newText)
	if err := os.WriteFile(p, newRaw, effectiveMode); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(p, effectiveMode); err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("Edited %s (replaced 1 occurrence).", path), lineOffset, nil
}

func deleteFileImpl(cwd string, allowedDirs []string, path string) (string, error) {
	p, err := resolvePathInAllowed(cwd, allowedDirs, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", path)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory, not a file: %s", path)
	}
	fileSize := info.Size()
	if err := os.Remove(p); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted %s (%d bytes)", path, fileSize), nil
}

// bashHistoryPath is the file where full (untruncated) bash commands and
// results are logged. Defaults to /tmp (so it's cleaned up with the
// container) but follows --log-dir when set via SetTmpLogDir.
func bashHistoryPath() string {
	return filepath.Join(TmpLogDir(), "vix-bash-history.log")
}

func logBashHistory(format string, args ...any) {
	f, err := os.OpenFile(bashHistoryPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

func bashImpl(ctx context.Context, server *Server, command, cwd string, extraDirs []string, headless bool) (string, error) {
	cmdPreview := command
	if len(cmdPreview) > 500 {
		cmdPreview = cmdPreview[:500] + "..."
	}
	LogInfo("[tool.bash] cwd=%s command=%s sandbox=%s headless=%v", cwd, cmdPreview, SandboxName(), headless)
	logBashHistory("cwd=%s\nCOMMAND:\n%s", cwd, command)

	// The dispatcher (executeToolDirect / executeToolConfirmed) already
	// wraps ctx with the global tool timeout (120s default, up to 600s
	// when the model passes an explicit `timeout` override). We just run
	// the command against that ctx; a deadline surfaces as
	// context.DeadlineExceeded and is reported by the dispatcher.
	cmd := sandboxedBashCmd(ctx, command, cwd, extraDirs)

	// Use Start+Wait instead of CombinedOutput so we can set the child's
	// OOM score between the two — this ensures the kernel kills the child
	// (not the daemon) when the container runs out of memory.
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	var err error
	if startErr := cmd.Start(); startErr != nil {
		return "", startErr
	}
	setOOMScore(cmd.Process.Pid, 1000)
	err = cmd.Wait()
	result := outBuf.String()
	if len(result) > maxOutput {
		result = result[:maxOutput] + fmt.Sprintf("\n... (truncated at %d chars)", maxOutput)
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			logBashHistory("RESULT (err=DeadlineExceeded):\n%s\n---", result)
			// Let the dispatcher surface the standard timeout error.
			return "", ctx.Err()
		}
		if ctx.Err() == context.Canceled {
			logBashHistory("RESULT (err=Canceled):\n%s\n---", result)
			return result, ctx.Err()
		}
		// WaitDelay expired: the process exited but a background child
		// kept the stdout pipe open. Treat this as a normal completion
		// — we already captured the output from the main process.
		if errors.Is(err, exec.ErrWaitDelay) {
			log.Printf("[tool.bash] WaitDelay expired — background child held pipe open")
			err = nil
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result += fmt.Sprintf("\n[exit code: %d]", exitErr.ExitCode())
		}
	}
	if result == "" {
		result = "(no output)"
	}
	resultPreview := result
	if len(resultPreview) > 500 {
		resultPreview = resultPreview[:500] + "..."
	}
	LogInfo("[tool.bash] result=%s", resultPreview)
	logBashHistory("RESULT (err=%v):\n%s\n---", err, result)
	return result, nil
}

// runBashWithContext runs a bash command with context-based cancellation and
// process group management. On context cancellation the entire process group
// is killed so that child processes don't become orphans.
// If onLine is non-nil it is called for every line of combined stdout/stderr.
func runBashWithContext(ctx context.Context, command, cwd, input string, onLine func(string)) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}

	pr, pw, pipeErr := os.Pipe()
	if pipeErr != nil {
		out, e := cmd.CombinedOutput()
		return string(out), e
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return "", err
	}
	setOOMScore(cmd.Process.Pid, 1000)

	// Kill the entire process group when context is cancelled.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()

	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line + "\n")
			if onLine != nil {
				onLine(line)
			}
		}
	}()

	cmdErr := cmd.Wait()
	pw.Close()
	wg.Wait()
	pr.Close()
	close(done)

	if ctx.Err() != nil {
		return buf.String(), ctx.Err()
	}
	return buf.String(), cmdErr
}

// BashJob is a single detached bash command spawned via bash tool
// `background: true`. The daemon keeps ownership of log/rc file handles and a
// cancel hook so the whole process group can be SIGKILLed at session shutdown
// (or when the job's own deadline fires). The LLM polls via ordinary `bash`
// calls against LogPath / RCPath — no new tool surface needed.
type BashJob struct {
	ID      string
	PID     int
	PGID    int
	LogPath string
	RCPath  string
	PIDPath string
	cancel  context.CancelFunc // cancels the per-job ctx; sandbox's cmd.Cancel picks it up and SIGKILLs -pgid
	Done    chan struct{}      // closed after the reaper goroutine writes the rc file
}

// BashJobRegistry owns the bash-tool background jobs for one session. It does
// not cross sessions — a session shutdown reaps its own jobs via KillAll().
// sync.Map because writes (Store on spawn, Delete on reap) are both O(jobs
// ever spawned) and reads are rare (only iteration in KillAll).
type BashJobRegistry struct {
	m sync.Map
}

func (r *BashJobRegistry) Store(j *BashJob) { r.m.Store(j.ID, j) }
func (r *BashJobRegistry) Load(id string) (*BashJob, bool) {
	v, ok := r.m.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*BashJob), true
}
func (r *BashJobRegistry) Delete(id string) { r.m.Delete(id) }

// KillAll cancels every job in the registry and waits up to 2s per job for
// its reaper goroutine to finish. Called from server.go when a session ends.
func (r *BashJobRegistry) KillAll() {
	jobs := []*BashJob{}
	r.m.Range(func(_, v any) bool {
		if j, ok := v.(*BashJob); ok {
			jobs = append(jobs, j)
		}
		return true
	})
	for _, j := range jobs {
		if j.cancel != nil {
			j.cancel()
		}
	}
	for _, j := range jobs {
		select {
		case <-j.Done:
		case <-time.After(2 * time.Second):
			// Reaper goroutine didn't finish in time; registry will be GC'd
			// with the session anyway. Move on.
		}
	}
}

// bashJobRoot is where per-job dirs live: /tmp/vix-jobs/<id>/{log,rc,pid}.
// `/tmp` is writable under both seatbelt and bwrap sandbox profiles. The
// daemon creates the dir and opens the file handles BEFORE the sandboxed child
// starts, so sandbox constraints on the child are irrelevant for job files.
func bashJobRoot() string { return filepath.Join(TmpLogDir(), "vix-jobs") }

func newBashJobID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "bg-" + hex.EncodeToString(b[:]), nil
}

// bashBackgroundImpl spawns the command detached, returns immediately with a
// flat-key-value result string the LLM can parse. `timeoutSec` caps the job's
// wall clock; 0 defaults to 3600s so leaked jobs self-die even if the daemon
// crashes without running KillAll.
func bashBackgroundImpl(registry *BashJobRegistry, command, cwd string, extraDirs []string, timeoutSec int) (string, error) {
	if registry == nil {
		return "", fmt.Errorf("no bash job registry available on this session")
	}
	jobID, err := newBashJobID()
	if err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	jobDir := filepath.Join(bashJobRoot(), jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", jobDir, err)
	}
	logPath := filepath.Join(jobDir, "log")
	rcPath := filepath.Join(jobDir, "rc")
	pidPath := filepath.Join(jobDir, "pid")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = os.RemoveAll(jobDir)
		return "", fmt.Errorf("open log: %w", err)
	}

	// Separate ctx from the dispatcher's tool ctx so the 120/300s tool
	// timeout does NOT kill the backgrounded child. The sandbox's cmd.Cancel
	// hook (sandbox.go:140-142 / 194-196) SIGKILLs the process group when
	// this ctx is cancelled — which we drive from KillAll() or the per-job
	// deadline below.
	if timeoutSec <= 0 {
		timeoutSec = 3600
	}
	bgCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)

	cmd := sandboxedBashCmd(bgCtx, command, cwd, extraDirs)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// /dev/null stdin — the child should never block waiting on input, and we
	// don't want it to inherit the daemon's stdin and tickle a read.
	if devNull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin = devNull
	}

	if err := cmd.Start(); err != nil {
		cancel()
		_ = logFile.Close()
		_ = os.RemoveAll(jobDir)
		return "", fmt.Errorf("start: %w", err)
	}
	setOOMScore(cmd.Process.Pid, 1000)
	pid := cmd.Process.Pid
	pgid, _ := syscall.Getpgid(pid)
	if pgid <= 0 {
		pgid = pid
	}
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", pid)), 0o644)

	job := &BashJob{
		ID:      jobID,
		PID:     pid,
		PGID:    pgid,
		LogPath: logPath,
		RCPath:  rcPath,
		PIDPath: pidPath,
		cancel:  cancel,
		Done:    make(chan struct{}),
	}
	registry.Store(job)

	// Reaper: waits on the child, writes rc, closes Done, drops registry
	// entry, closes the log file. Must NOT hold any session-scoped state —
	// the session may end before the goroutine returns, and that's fine:
	// KillAll() cancel'd our ctx, the child dies, we record the signal.
	go func() {
		werr := cmd.Wait()
		rcContent := "0\n"
		if werr != nil {
			if ee, ok := werr.(*exec.ExitError); ok {
				rcContent = fmt.Sprintf("%d\n", ee.ExitCode())
			} else if bgCtx.Err() == context.DeadlineExceeded {
				rcContent = "timeout\n"
			} else if bgCtx.Err() == context.Canceled {
				rcContent = "cancelled\n"
			} else {
				rcContent = fmt.Sprintf("error: %s\n", werr.Error())
			}
		}
		_ = os.WriteFile(rcPath, []byte(rcContent), 0o644)
		_ = logFile.Close()
		close(job.Done)
		registry.Delete(jobID)
		cancel() // release the ctx's timer even on clean exit
	}()

	result := fmt.Sprintf(
		"job_id: %s\n"+
			"pid: %d\n"+
			"pgid: %d\n"+
			"log: %s\n"+
			"rc: %s\n"+
			"timeout_sec: %d\n"+
			"poll: test -f %s && cat %s\n"+
			"tail: tail -n 50 %s\n"+
			"kill: kill -TERM -%d\n"+
			"note: rc is created only after the command exits. Empty/missing rc means still running.\n",
		jobID, pid, pgid, logPath, rcPath, timeoutSec, rcPath, rcPath, logPath, pgid,
	)
	return result, nil
}

// FormatEditDiff builds a structured side-by-side diff by running the system
// `diff -U3` command on the old and new strings and parsing its unified output.
// The result uses tagged rows consumed by renderDiffDetail in chat.go:
//
//	"H <text>\n"                  — header line (summary)
//	"C <leftN> <rightN> <text>\n" — context line present on both sides
//	"R <leftN> <text>\n"          — removed line (left side only)
//	"A <rightN> <text>\n"         — added line (right side only)
//
// lineOffset is the 0-based line index of oldStr's first line within the real
// file, computed before the write so the numbers reflect actual file positions.
func FormatEditDiff(oldStr, newStr string, lineOffset int) string {
	// Write old and new content to temp files so diff can compare them.
	oldTmp, err := os.CreateTemp("", "vix-diff-old-*")
	if err != nil {
		return formatEditDiffFallback(oldStr, newStr, lineOffset)
	}
	defer os.Remove(oldTmp.Name())
	if _, err := oldTmp.WriteString(oldStr); err != nil {
		oldTmp.Close()
		return formatEditDiffFallback(oldStr, newStr, lineOffset)
	}
	oldTmp.Close()

	newTmp, err := os.CreateTemp("", "vix-diff-new-*")
	if err != nil {
		return formatEditDiffFallback(oldStr, newStr, lineOffset)
	}
	defer os.Remove(newTmp.Name())
	if _, err := newTmp.WriteString(newStr); err != nil {
		newTmp.Close()
		return formatEditDiffFallback(oldStr, newStr, lineOffset)
	}
	newTmp.Close()

	// Run diff -U3 (unified, 3 context lines). Exit code 1 means differences
	// found (normal); only exit code >1 signals a real error.
	cmd := exec.Command("diff", "-U3", "--label", "old", "--label", "new", oldTmp.Name(), newTmp.Name())
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() > 1 {
			return formatEditDiffFallback(oldStr, newStr, lineOffset)
		}
	}

	return parseUnifiedDiff(string(out), oldStr, newStr, lineOffset)
}

// parseUnifiedDiff converts unified diff output into the H/C/R/A tag format
// expected by renderDiffDetail in chat.go.
// lineOffset is the 0-based line index of oldStr's first line within the real file,
// used to convert snippet-relative line numbers to actual file line numbers.
func parseUnifiedDiff(unified, oldStr, newStr string, lineOffset int) string {
	addedLines := strings.Count(newStr, "\n") + 1
	removedLines := strings.Count(oldStr, "\n") + 1

	var b strings.Builder
	b.WriteString(fmt.Sprintf("H +%d -%d lines\n", addedLines, removedLines))

	lines := strings.Split(unified, "\n")

	// Track current line numbers on each side as we walk hunks.
	var leftN, rightN int

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		// Unified diff hunk header: @@ -l,s +l,s @@
		if strings.HasPrefix(line, "@@") {
			// Parse left and right starting line numbers.
			// Format: @@ -<l>[,<s>] +<l>[,<s>] @@
			// Note: Go's fmt.Sscanf does not support %*d (C-style suppression),
			// so we parse by scanning tokens for the "-" and "+" fields.
			var ls, rs int
			for _, tok := range strings.Fields(line) {
				if strings.HasPrefix(tok, "-") {
					fmt.Sscanf(strings.TrimPrefix(tok, "-"), "%d", &ls)
				} else if strings.HasPrefix(tok, "+") {
					fmt.Sscanf(strings.TrimPrefix(tok, "+"), "%d", &rs)
				}
			}
			// Apply offset so numbers reflect real file positions.
			leftN = ls + lineOffset
			rightN = rs + lineOffset
			continue
		}
		// Skip the --- / +++ file header lines.
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}

		switch line[0] {
		case ' ':
			// Context line — present on both sides.
			b.WriteString(fmt.Sprintf("C %d %d %s\n", leftN, rightN, line[1:]))
			leftN++
			rightN++
		case '-':
			// Removed line.
			b.WriteString(fmt.Sprintf("R %d %s\n", leftN, line[1:]))
			leftN++
		case '+':
			// Added line.
			b.WriteString(fmt.Sprintf("A %d %s\n", rightN, line[1:]))
			rightN++
		}
	}

	return b.String()
}

// formatEditDiffFallback is used when diff is unavailable or temp file creation
// fails. It emits the same H/R/A tag format without context lines.
// lineOffset is the 0-based line index of oldStr's first line within the real file.
func formatEditDiffFallback(oldStr, newStr string, lineOffset int) string {
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")
	var b strings.Builder
	b.WriteString(fmt.Sprintf("H +%d -%d lines\n", len(newLines), len(oldLines)))
	maxPairs := len(oldLines)
	if len(newLines) > maxPairs {
		maxPairs = len(newLines)
	}
	for i := 0; i < maxPairs; i++ {
		if i < len(oldLines) {
			b.WriteString(fmt.Sprintf("R %d %s\n", i+1+lineOffset, oldLines[i]))
		}
		if i < len(newLines) {
			b.WriteString(fmt.Sprintf("A %d %s\n", i+1+lineOffset, newLines[i]))
		}
	}
	return b.String()
}

// --- Async handler wrappers ---

func RegisterToolHandlers(s *Server) {
	// Load tool backend config from home config. Project-level overrides are
	// not applied here because tool handlers are registered once per daemon,
	// not per session. Use ~/.vix/settings.json as the sole source.
	toolsCfg := loadToolsConfig([]string{filepath.Join(config.HomeVixDir(), "settings.json")})
	grepBackend := newGrepRunner(toolsCfg.Grep.Backend)
	globBackend := newGlobRunner(toolsCfg.Glob.Backend)

	s.RegisterHandler("tool.read_file", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("read_file", params) {
			return toolConfirmResponse("read_file", params), nil
		}
		path, _ := params["path"].(string)
		var offset, limit *int
		if v, ok := params["offset"].(float64); ok {
			i := int(v)
			offset = &i
		}
		if v, ok := params["limit"].(float64); ok {
			i := int(v)
			limit = &i
		}
		cwd, _ := params["cwd"].(string)
		allowedDirs := extractAllowedDirs(params)
		output, err := readFileImpl(cwd, allowedDirs, path, offset, limit)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		s.LogAccess("read_file", params)
		return toolOK(capFileReadOutput(output), false), nil
	})

	s.RegisterHandler("tool.read_minified_file", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("read_minified_file", params) {
			return toolConfirmResponse("read_minified_file", params), nil
		}
		path, _ := params["path"].(string)
		var offset, limit *int
		if v, ok := params["offset"].(float64); ok {
			i := int(v)
			offset = &i
		}
		if v, ok := params["limit"].(float64); ok {
			i := int(v)
			limit = &i
		}
		cwd, _ := params["cwd"].(string)
		allowedDirs := extractAllowedDirs(params)
		extMap, _, vfsConfigs := loadFormatterConfigs(defaultLanguagesPaths(s.homeVixDir))
		keepComments := keepCommentsForPath(extMap, vfsConfigs, path)
		var output string
		var err error
		if vfsReadEnabledForPath(extMap, vfsConfigs, path) {
			output, err = VfsRead(cwd, allowedDirs, path, offset, limit, keepComments)
		} else {
			LogInfo("[vfs] read_minified_file: VFS disabled for %s, falling back to read_file", path)
			output, err = readFileImpl(cwd, allowedDirs, path, offset, limit)
		}
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		s.LogAccess("read_minified_file", params)
		return toolOK(capFileReadOutput(output), false), nil
	})

	s.RegisterHandler("tool.write_file", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("write_file", params) {
			return toolConfirmResponse("write_file", params), nil
		}
		path, _ := params["path"].(string)
		content, _ := params["content"].(string)
		cwd, _ := params["cwd"].(string)
		allowedDirs := extractAllowedDirs(params)
		modeStr, _ := params["mode"].(string)
		mode, err := parseFileMode(modeStr)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		output, err := writeFileImpl(cwd, allowedDirs, path, content, mode)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		if shouldTriggerUpdate(path) {
			go flushBrainUpdate(s, []string{path})
		}
		s.LogAccess("write_file", params)
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.write_minified_file", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("write_minified_file", params) {
			return toolConfirmResponse("write_minified_file", params), nil
		}
		path, _ := params["path"].(string)
		content, _ := params["content"].(string)
		cwd, _ := params["cwd"].(string)
		allowedDirs := extractAllowedDirs(params)
		var output string
		var err error
		extMap, formatters, vfsConfigs := loadFormatterConfigs(defaultLanguagesPaths(s.homeVixDir))
		if vfsEnabledForPath(extMap, formatters, vfsConfigs, path) {
			output, err = VfsWrite(cwd, allowedDirs, s.homeVixDir, path, content)
		} else {
			LogInfo("[vfs] write_minified_file: VFS disabled for %s, falling back to write_file", path)
			output, err = writeFileImpl(cwd, allowedDirs, path, content, 0)
		}
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		if shouldTriggerUpdate(path) {
			go flushBrainUpdate(s, []string{path})
		}
		s.LogAccess("write_minified_file", params)
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.edit_file", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("edit_file", params) {
			return toolConfirmResponse("edit_file", params), nil
		}
		path, _ := params["path"].(string)
		oldString, _ := params["old_string"].(string)
		newString, _ := params["new_string"].(string)
		cwd, _ := params["cwd"].(string)
		allowedDirs := extractAllowedDirs(params)
		modeStr, _ := params["mode"].(string)
		mode, err := parseFileMode(modeStr)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		output, lineOffset, err := editFileImpl(cwd, allowedDirs, path, oldString, newString, mode)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		if shouldTriggerUpdate(path) {
			go flushBrainUpdate(s, []string{path})
		}
		s.LogAccess("edit_file", params)
		result := toolOK(output, false)
		result["data"].(map[string]any)["line_offset"] = lineOffset
		return result, nil
	})

	s.RegisterHandler("tool.edit_minified_file", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("edit_minified_file", params) {
			return toolConfirmResponse("edit_minified_file", params), nil
		}
		path, _ := params["path"].(string)
		oldString, _ := params["old_string"].(string)
		newString, _ := params["new_string"].(string)
		cwd, _ := params["cwd"].(string)
		allowedDirs := extractAllowedDirs(params)
		var output string
		var lineOffset int
		var err error
		extMap, formatters, vfsConfigs := loadFormatterConfigs(defaultLanguagesPaths(s.homeVixDir))
		keepComments := keepCommentsForPath(extMap, vfsConfigs, path)
		if vfsEnabledForPath(extMap, formatters, vfsConfigs, path) {
			output, lineOffset, err = VfsEdit(cwd, allowedDirs, s.homeVixDir, path, oldString, newString, keepComments)
		} else {
			LogInfo("[vfs] edit_minified_file: VFS disabled for %s, falling back to edit_file", path)
			output, lineOffset, err = editFileImpl(cwd, allowedDirs, path, oldString, newString, 0)
		}
		if err != nil {
			LogWarn("[vfs] edit_minified_file failed for %s: %v (keepComments=%v, old_string length=%d)", path, err, keepComments, len(oldString))
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		if shouldTriggerUpdate(path) {
			go flushBrainUpdate(s, []string{path})
		}
		s.LogAccess("edit_minified_file", params)
		result := toolOK(output, false)
		result["data"].(map[string]any)["line_offset"] = lineOffset
		return result, nil
	})

	s.RegisterHandler("tool.delete_file", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("delete_file", params) {
			return toolConfirmResponse("delete_file", params), nil
		}
		path, _ := params["path"].(string)
		cwd, _ := params["cwd"].(string)
		allowedDirs := extractAllowedDirs(params)
		output, err := deleteFileImpl(cwd, allowedDirs, path)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		if shouldTriggerUpdate(path) {
			go flushBrainUpdate(s, []string{path})
		}
		s.LogAccess("delete_file", params)
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.bash", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("bash", params) {
			return toolConfirmResponse("bash", params), nil
		}
		ctx, _ := data["ctx"].(context.Context)
		if ctx == nil {
			ctx = context.Background()
		}
		command, _ := params["command"].(string)
		cwd, _ := params["cwd"].(string)
		if cwd == "" {
			cwd = "."
		}
		allowedDirs := extractAllowedDirs(params)
		headless, _ := params["headless"].(bool)
		// Background path: detach the command via sandboxedBashCmd + Start(),
		// return a job handle immediately. The `timeout` param becomes a
		// wall-clock cap on the detached child, NOT on this tool call. The
		// dispatcher's tool ctx is deliberately ignored for backgrounded work
		// — that ctx will fire at 120/300s and we do not want to kill a
		// legitimate 10-minute `john` run because of it.
		if bg, _ := params["background"].(bool); bg {
			sess, _ := params["_session"].(*Session)
			var registry *BashJobRegistry
			if sess != nil {
				registry = &sess.bashJobs
			}
			// `timeout` arrives as float64 when the param came from JSON; as
			// int64/int when constructed in Go tests. Accept both.
			var tmo int
			switch v := params["timeout"].(type) {
			case float64:
				tmo = int(v)
			case int:
				tmo = v
			case int64:
				tmo = int(v)
			}
			out, berr := bashBackgroundImpl(registry, command, cwd, allowedDirs, tmo)
			if berr != nil {
				return toolOK(fmt.Sprintf("Error: %v", berr), true), nil
			}
			return toolOK(out, false), nil
		}
		// Note: the `timeout` param is consumed by the dispatcher
		// (resolveToolTimeout in session.go) — not here.
		output, err := bashImpl(ctx, s, command, cwd, allowedDirs, headless)
		if err != nil {
			if err == context.Canceled {
				return toolOK("Cancelled", true), nil
			}
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.grep", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("grep", params) {
			return toolConfirmResponse("grep", params), nil
		}
		ctx, _ := data["ctx"].(context.Context)
		if ctx == nil {
			ctx = context.Background()
		}
		pattern, _ := params["pattern"].(string)
		path, _ := params["path"].(string)
		include, _ := params["include"].(string)
		cwd, _ := params["cwd"].(string)
		if cwd == "" {
			cwd = "."
		}
		output, err := grepBackend.Run(ctx, pattern, path, include, cwd)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return toolOK("Cancelled", true), nil
			}
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		if sess, ok := params["_session"].(*Session); ok {
			if dl := sess.denyListSnapshot(); len(dl) > 0 {
				output = filterOutputAgainstDeny(output, cwd, dl)
			}
		}
		resultPreview := output
		if len(resultPreview) > 500 {
			resultPreview = resultPreview[:500] + "..."
		}
		LogInfo("[tool.grep] result=%s", resultPreview)
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.glob_files", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		if needsConfirmation("glob_files", params) {
			return toolConfirmResponse("glob_files", params), nil
		}
		ctx, _ := data["ctx"].(context.Context)
		if ctx == nil {
			ctx = context.Background()
		}
		patterns := toStringList(params["pattern"])
		paths := toStringList(params["path"])
		cwd, _ := params["cwd"].(string)
		typeFilter, _ := params["type"].(string)
		includeHidden := true
		if v, ok := params["include_hidden"].(bool); ok {
			includeHidden = v
		}
		if cwd == "" {
			cwd = "."
		}
		// max_results defaults to 1000. JSON numbers unmarshal into float64, so
		// we accept that plus int variants for robustness against future callers.
		maxResults := 1000
		switch v := params["max_results"].(type) {
		case float64:
			if v > 0 {
				maxResults = int(v)
			}
		case int:
			if v > 0 {
				maxResults = v
			}
		case int64:
			if v > 0 {
				maxResults = int(v)
			}
		}
		if len(patterns) == 0 {
			return toolOK("Error: pattern is required (string or non-empty array of strings)", true), nil
		}
		output, err := globBackend.Run(ctx, patterns, paths, cwd, typeFilter, includeHidden, maxResults)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return toolOK("Cancelled", true), nil
			}
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		if sess, ok := params["_session"].(*Session); ok {
			if dl := sess.denyListSnapshot(); len(dl) > 0 {
				output = filterOutputAgainstDeny(output, cwd, dl)
			}
		}
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.tool_orchestrator", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		ctx, _ := data["ctx"].(context.Context)
		if ctx == nil {
			ctx = context.Background()
		}
		workflow, _ := params["workflow"].(string)
		cwd, _ := params["cwd"].(string)
		if cwd == "" {
			cwd = "."
		}
		output, err := toolOrchestratorImpl(ctx, s, workflow, cwd)
		if err != nil {
			if err == context.Canceled {
				return toolOK("Cancelled", true), nil
			}
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.web_fetch", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		rawURL, _ := params["url"].(string)
		selector, _ := params["selector"].(string)
		t0 := time.Now()
		output, err := webFetchImpl(rawURL, selector)
		elapsedMs := time.Since(t0).Milliseconds()
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		result := toolOK(output, false)
		result["data"].(map[string]any)["elapsed_ms"] = elapsedMs
		return result, nil
	})

	s.RegisterHandler("tool.web_search", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		query, _ := params["query"].(string)
		count := 5
		if v, ok := params["count"].(float64); ok {
			count = int(v)
		}
		output, err := webSearchImpl(query, count)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		return toolOK(output, false), nil
	})

	s.RegisterHandler("tool.lsp_query", func(data map[string]any) (map[string]any, error) {
		params, _ := data["params"].(map[string]any)
		operation, _ := params["operation"].(string)
		file, _ := params["file"].(string)
		query, _ := params["query"].(string)
		cwd, _ := params["cwd"].(string)
		if cwd == "" {
			cwd = "."
		}

		line := 0
		if v, ok := params["line"].(float64); ok {
			line = int(v)
		}
		character := 0
		if v, ok := params["character"].(float64); ok {
			character = int(v)
		}
		includeDecl := true
		if v, ok := params["include_declaration"].(bool); ok {
			includeDecl = v
		}

		output, err := lspQueryImpl(operation, file, query, line, character, includeDecl, cwd)
		if err != nil {
			return toolOK(fmt.Sprintf("Error: %v", err), true), nil
		}
		s.LogAccess("lsp_query", params)
		return toolOK(output, false), nil
	})
}

// doGetTopFiles retrieves the top N most accessed files with their content.
func doGetTopFiles(s *Server, input map[string]any) (map[string]any, error) {
	// Handle case where accessDB is nil
	if s.accessDB == nil {
		return map[string]any{
			"status": "ok",
			"data": map[string]any{
				"files": []map[string]any{},
			},
		}, nil
	}

	// Extract count parameter (default to 10)
	count := 10
	if countParam, ok := input["count"].(float64); ok {
		count = int(countParam)
	} else if countParam, ok := input["count"].(int); ok {
		count = countParam
	}

	// Get top accessed files
	filePaths, err := getTopAccessedFiles(s.accessDB, count)
	if err != nil {
		return map[string]any{
			"status":  "error",
			"message": fmt.Sprintf("Failed to get top accessed files: %v", err),
		}, nil
	}

	// Read content for each file
	var files []map[string]any
	for _, path := range filePaths {
		content, err := readFileForTopFiles(path)
		if err != nil {
			// Skip files that can't be read (may have been deleted)
			continue
		}

		files = append(files, map[string]any{
			"path":    path,
			"content": content,
		})
	}

	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"files": files,
		},
	}, nil
}

// readFileForTopFiles reads a file and truncates it if too large (>500 lines).
func readFileForTopFiles(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(p); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", path)
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}

	text := string(raw)
	lines := strings.Split(text, "\n")

	if len(lines) > 500 {
		truncatedLines := lines[:400]
		truncatedLines = append(truncatedLines, "... (truncated)")
		return strings.Join(truncatedLines, "\n"), nil
	}

	return text, nil
}
