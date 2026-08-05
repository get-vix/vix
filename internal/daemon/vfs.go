package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

// errVFSUnsupported is returned when the file cannot be minified by Tree-sitter.
var errVFSUnsupported = errors.New("vfs: unsupported file type")

// defaultLanguagesPaths returns the canonical [home/config/languages.json]
// list used by stateless tool handlers that don't have a thread-level
// VixPaths available. Languages are home-only (not layered with the project),
// so this is a single-element slice. Per-thread config-dir overrides are
// handled at the thread layer.
func defaultLanguagesPaths(homeVixDir string) []string {
	if homeVixDir == "" {
		return nil
	}
	return []string{filepath.Join(homeVixDir, "config", "languages.json")}
}

// fileLocks is a per-file mutex registry to ensure vfsEdit's read→minify→write
// sequence is atomic per file under concurrent agent calls.
var fileLocks sync.Map

// fileMutexFor returns the per-file mutex for the given absolute path,
// creating one if it does not yet exist.
func fileMutexFor(absPath string) *sync.Mutex {
	actual, _ := fileLocks.LoadOrStore(absPath, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// loadFormatterConfigs loads language configs from the given settings.json
// paths in order (later entries override earlier ones by language Name), and
// returns three maps:
//   - extMap: file extension → language name (e.g. ".go" → "go")
//   - formatters: language name → FormatterConfig
//   - vfsConfigs: language name → VFSConfig
//
// All maps are non-nil even on error.
func loadFormatterConfigs(settingsPaths []string) (extMap map[string]string, formatters map[string]*lsp.FormatterConfig, vfsConfigs map[string]*lsp.VFSConfig) {
	extMap = make(map[string]string)
	formatters = make(map[string]*lsp.FormatterConfig)
	vfsConfigs = make(map[string]*lsp.VFSConfig)

	var merged []lsp.LanguageConfig
	for _, p := range settingsPaths {
		if p == "" {
			continue
		}
		langs := lsp.LoadLanguageConfigs(p)
		for _, pl := range langs {
			found := false
			for i, hl := range merged {
				if hl.Name == pl.Name {
					merged[i] = pl
					found = true
					break
				}
			}
			if !found {
				merged = append(merged, pl)
			}
		}
	}

	for _, lc := range merged {
		if lc.Formatter != nil {
			formatters[lc.Name] = lc.Formatter
		}
		if lc.VFS != nil {
			vfsConfigs[lc.Name] = lc.VFS
		}
		for _, ext := range lc.Extensions {
			extMap[ext] = lc.Name
		}
	}
	return extMap, formatters, vfsConfigs
}

// vfsReadEnabledForPath returns true if VFS read (minification) is enabled for the given file path.
// Only requires vfs.enable=true for the language — no formatter needed for reading.
func vfsReadEnabledForPath(extMap map[string]string, vfsConfigs map[string]*lsp.VFSConfig, path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := extMap[ext]
	if !ok {
		return false
	}
	vc := vfsConfigs[lang]
	return vc != nil && vc.Enable
}

// vfsEnabledForPath returns true if VFS is fully enabled for the given file path.
// Requires both vfs.enable=true and a formatter configured (needed for write/edit).
func vfsEnabledForPath(extMap map[string]string, formatters map[string]*lsp.FormatterConfig, vfsConfigs map[string]*lsp.VFSConfig, path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := extMap[ext]
	if !ok {
		return false
	}
	vc := vfsConfigs[lang]
	if vc == nil || !vc.Enable {
		return false
	}
	_, hasFormatter := formatters[lang]
	return hasFormatter
}

// keepCommentsForPath resolves the per-language keep_comments setting for the given file path.
func keepCommentsForPath(extMap map[string]string, vfsConfigs map[string]*lsp.VFSConfig, path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := extMap[ext]
	if !ok {
		return false
	}
	vc := vfsConfigs[lang]
	if vc == nil {
		return false
	}
	return vc.KeepComments
}

// VfsRead reads a file, optionally extracts a line range, then minifies via Tree-sitter.
// offset/limit are 1-based line numbers. nil means whole file.
// Falls back to returning raw content if minification is not supported.
func VfsRead(cwd string, allowedDirs []string, path string, offset, limit *int, keepComments bool) (string, error) {
	absPath, err := resolvePathInAllowed(cwd, allowedDirs, path)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", path)
	}

	raw, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	content := string(raw)

	// Apply line-based slicing if offset/limit provided.
	if offset != nil || limit != nil {
		lines := strings.Split(content, "\n")
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
		content = strings.Join(lines[start:end], "\n")
	}

	minified, err := minifyWithTreeSitter(content, path, keepComments)
	if err != nil {
		LogWarn("[vfs] VfsRead: minifier error for %s, falling back to raw content: %v", path, err)

		return content, nil
	}
	if minified == "" {
		LogInfo("[vfs] VfsRead: minifier returned empty for %s (unsupported grammar or empty input), falling back to raw content", path)
		return content, nil
	}
	return minified, nil
}

// vfsFormat runs the configured formatter on the file at absPath.
// Formatter failure is non-fatal at the filesystem level (the write already
// happened) but the error is returned so callers can surface it to the user
// and the LLM — a silent formatter failure leaves the file in minified form
// on disk and breaks the next edit's match.
func vfsFormat(absPath string, cfg *lsp.FormatterConfig) error {
	args := make([]string, len(cfg.Args), len(cfg.Args)+1)
	copy(args, cfg.Args)
	args = append(args, absPath)

	if err := exec.Command(cfg.Command, args...).Run(); err != nil {
		LogWarn("vfs: formatter %q failed for %s: %v", cfg.Command, absPath, err)
		return fmt.Errorf("formatter %q failed: %w", cfg.Command, err)
	}
	return nil
}

// projectSpan maps a byte range [outStart, outEnd) in the minified output back
// to a byte range [srcStart, srcEnd) in the original source, using the position
// map produced by minifyWithTreeSitterMapped. segs must be sorted by outStart
// (as emitted), non-overlapping, and increasing.
//
// When a boundary falls inside a token segment it maps linearly, because a
// token's text is a verbatim copy of the source bytes it came from. When a
// boundary falls in a synthetic gap (an inserted separator or merge-prevention
// space that exists in no source), it snaps to the nearest real token: the start
// snaps forward to the next token, the end snaps back to the previous token. ok
// is false when the range cannot be anchored to any token — for example when it
// lies entirely within synthetic text (an inserted ";" the model matched alone).
func projectSpan(segs []srcSegment, outStart, outEnd int) (srcStart, srcEnd int, ok bool) {
	if len(segs) == 0 || outEnd <= outStart {
		return 0, 0, false
	}

	// Resolve srcStart from outStart: the first segment whose end is past it.
	startFound := false
	for _, s := range segs {
		if outStart < s.outStart+s.length {
			if outStart >= s.outStart {
				srcStart = s.srcStart + (outStart - s.outStart) // inside the token
			} else {
				srcStart = s.srcStart // in the gap before it: snap forward
			}
			startFound = true
			break
		}
	}
	if !startFound {
		return 0, 0, false
	}

	// Resolve srcEnd from the last matched byte: the last segment at/before it.
	last := outEnd - 1
	endFound := false
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if last >= s.outStart {
			if last < s.outStart+s.length {
				srcEnd = s.srcStart + (last - s.outStart) + 1 // inside the token
			} else {
				srcEnd = s.srcStart + s.length // in the gap after it: snap back
			}
			endFound = true
			break
		}
	}
	if !endFound {
		return 0, 0, false
	}

	if srcStart >= srcEnd {
		return 0, 0, false
	}
	return srcStart, srcEnd, true
}

// VfsEdit performs an edit on a VFS-managed file. The model supplies oldString
// in the minified representation (as returned by read_minified_file). VfsEdit
// minifies the current file content while keeping a position map, matches
// oldString against the minified text, projects the matched span back onto exact
// byte offsets in the original source, and splices newString into those bytes —
// touching only the matched region and leaving all surrounding whitespace,
// indentation, comments, and formatting untouched. The on-disk file is never
// rewritten in minified form.
//
// homeVixDir is retained for signature compatibility with the other Vfs helpers;
// the splice path needs no formatter.
//
// Unlike editFileImpl, there is no fallback on failure — errors are surfaced
// directly (callers fall back to editFileImpl on errVFSUnsupported).
func VfsEdit(cwd string, allowedDirs []string, homeVixDir, path, oldString, newString string, keepComments bool) (message string, lineOffset int, err error) {
	absPath, pathErr := resolvePathInAllowed(cwd, allowedDirs, path)
	if pathErr != nil {
		return "", 0, pathErr
	}

	mu := fileMutexFor(absPath)
	mu.Lock()
	defer mu.Unlock()

	rawBytes, err := os.ReadFile(absPath)
	if err != nil {
		return "", 0, err
	}

	// Minify the current file content and keep the position map so the match
	// can be projected back onto exact byte offsets in the unminified source.
	minifiedText, segs, err := minifyWithTreeSitterMapped(string(rawBytes), path, keepComments)
	if err != nil || minifiedText == "" {
		return "", 0, errVFSUnsupported
	}

	count := strings.Count(minifiedText, oldString)
	if count == 0 {
		return "", 0, errors.New("old_string not found in file")
	}
	if count > 1 {
		return "", 0, fmt.Errorf("old_string found %d times (must be unique)", count)
	}

	outStart := strings.Index(minifiedText, oldString)
	outEnd := outStart + len(oldString)

	srcStart, srcEnd, ok := projectSpan(segs, outStart, outEnd)
	if !ok {
		return "", 0, errors.New("old_string matched only inserted separators and cannot be mapped back to the source file")
	}

	// Splice the replacement into the original (unminified) source, touching
	// only the matched byte range.
	newSource := make([]byte, 0, srcStart+len(newString)+(len(rawBytes)-srcEnd))
	newSource = append(newSource, rawBytes[:srcStart]...)
	newSource = append(newSource, newString...)
	newSource = append(newSource, rawBytes[srcEnd:]...)

	// lineOffset is reported in the source domain so the diff preview shows real
	// file line numbers.
	lineOffset = strings.Count(string(rawBytes[:srcStart]), "\n")

	// Preserve the existing file mode (matches editFileImpl's mode==0 behavior).
	mode := os.FileMode(0o644)
	if st, statErr := os.Stat(absPath); statErr == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(absPath, newSource, mode); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(absPath, mode); err != nil {
		return "", 0, err
	}

	return fmt.Sprintf("Edited %s (replaced 1 occurrence).", path), lineOffset, nil
}

// VfsWrite writes minified content to a VFS-managed file, then runs the
// formatter to restore valid source. Creates parent directories if needed.
//
// The caller is expected to provide content in the same minified form that
// read_minified_file returns. After writing, the language's formatter expands
// it back into properly formatted code.
//
// Unlike writeFileImpl, there is no fallback on failure — errors are surfaced directly.
func VfsWrite(cwd string, allowedDirs []string, homeVixDir, path, content string) (string, error) {
	absPath, pathErr := resolvePathInAllowed(cwd, allowedDirs, path)
	if pathErr != nil {
		return "", pathErr
	}

	mu := fileMutexFor(absPath)
	mu.Lock()
	defer mu.Unlock()

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	encoded := []byte(content)
	if err := os.WriteFile(absPath, encoded, 0o644); err != nil {
		return "", err
	}

	// Run formatter if configured.
	extMap, formatters, _ := loadFormatterConfigs(defaultLanguagesPaths(homeVixDir))
	lang := extMap[strings.ToLower(filepath.Ext(path))]
	msg := fmt.Sprintf("Wrote %d bytes to %s", len(encoded), path)
	if cfg, ok := formatters[lang]; ok {
		if fmtErr := vfsFormat(absPath, cfg); fmtErr != nil {
			msg += fmt.Sprintf("\n\nWARNING: %v. File is left on disk in minified form.", fmtErr)
		}
	}

	return msg, nil
}
