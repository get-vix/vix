package daemon

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// normalizeForDeny returns an absolute, cleaned form of path suitable for
// ancestor-matching against a deny_list entry. Relative paths are joined
// with cwd. Symlinks are resolved best-effort via evalSymlinksBestEffort so
// a symlink that points into a denied tree is caught.
func normalizeForDeny(cwd, path string) string {
	if path == "" {
		return ""
	}
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(filepath.Join(cwd, path))
	}
	return evalSymlinksBestEffort(abs)
}

// evalSymlinksBestEffort walks back up p's ancestors until it finds one that
// exists on disk, resolves that ancestor's symlinks, then reattaches the
// trailing components. This matches the pattern in resolvePathInAllowed
// (tools.go:134) for paths that may not exist yet (write_file, mkdir, etc.).
//
// When filepath.EvalSymlinks fails for all ancestors (e.g. on macOS configs
// where /private is not traversable), it falls back to evalSymlinksComponents
// which uses os.Lstat + os.Readlink and works even in that case.
func evalSymlinksBestEffort(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	// Walk up until we find an existing ancestor.
	suffix := ""
	cur := p
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached root without finding a resolvable ancestor.
			// Fall back to component-level resolution.
			return evalSymlinksComponents(p)
		}
		base := filepath.Base(cur)
		if suffix == "" {
			suffix = base
		} else {
			suffix = filepath.Join(base, suffix)
		}
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			reconstructed := filepath.Join(real, suffix)
			if reconstructed == p {
				// Ancestor resolved to itself (e.g. "/" → "/") so no
				// hierarchy symlinks were resolved. Fall through to the
				// component-level walker which can still detect file-level
				// symlinks via os.Lstat and os.Readlink.
				return evalSymlinksComponents(p)
			}
			return reconstructed
		}
		cur = parent
	}
}

// evalSymlinksComponents resolves symlinks in an absolute path by walking each
// component individually with os.Lstat and os.Readlink. This is a fallback for
// environments where filepath.EvalSymlinks cannot traverse certain directories
// (e.g. /private on restricted macOS configurations). Circular symlinks are
// broken after 40 iterations, matching the Linux kernel's MAXSYMLINKS limit.
//
// When following a symlink would lead to a path whose root component is itself
// inaccessible (e.g. /var → private/var when /private is blocked), the symlink
// is skipped and the original path component is kept. This allows file-level
// symlinks (e.g. /var/folders/.../link → /var/folders/.../secret) to still be
// resolved correctly even when the top-level /private symlink cannot be chased.
func evalSymlinksComponents(p string) string {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return p
	}
	sep := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(p, sep), sep)
	current := sep
	const maxSymlinks = 40
	followed := 0

	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		next := filepath.Join(current, part)
		fi, err := os.Lstat(next)
		if err != nil {
			// Can't access this component — append remaining verbatim.
			return filepath.Join(append([]string{current}, parts[i:]...)...)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if followed >= maxSymlinks {
				return next
			}
			target, err := os.Readlink(next)
			if err != nil {
				current = next
				continue
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(current, target)
			}
			target = filepath.Clean(target)
			// Before following, verify the target's root component is
			// accessible. If not (e.g. /var → private/var when /private is
			// blocked), skip this symlink and keep the original path so that
			// subsequent accessible components can still be resolved.
			targetParts := strings.Split(strings.TrimPrefix(target, sep), sep)
			if len(targetParts) > 0 && targetParts[0] != "" {
				if _, checkErr := os.Lstat(sep + targetParts[0]); checkErr != nil {
					current = next
					continue
				}
			}
			followed++
			parts = append(targetParts, parts[i+1:]...)
			i = -1 // restart from the new root on next iteration
			current = sep
		} else {
			current = next
		}
	}
	return current
}

// combineDenyPaths returns the effective deny path list for a session: the
// config-resolved absolute entries (denyPaths) plus each raw relative entry
// (denyPathsRel) resolved against cwd, deduped. This is what makes a relative
// `deny_list.paths` entry like ".envrc.private" in ./.vix/settings.json block
// the file at the project root, in addition to the config-dir-relative form
// that LoadProjectConfig already produced.
func combineDenyPaths(cwd string, denyPaths, denyPathsRel []string) []string {
	out := append([]string(nil), denyPaths...)
	for _, rel := range denyPathsRel {
		out = appendUniqueStr(out, filepath.Clean(filepath.Join(cwd, rel)))
	}
	return out
}

// isPathDenied reports whether absPath matches any deny_list entry.
// Match means: the normalized (symlink-resolved) absPath equals or is under
// the normalized (symlink-resolved) entry. Returns the matching entry for
// error reporting. cwd is only used when absPath itself happens to be
// relative (rare — callers should normalize first, but we defend).
func isPathDenied(absPath, cwd string, denyList []string) (bool, string) {
	if len(denyList) == 0 || absPath == "" {
		return false, ""
	}
	realPath := normalizeForDeny(cwd, absPath)
	for _, entry := range denyList {
		realEntry := evalSymlinksBestEffort(filepath.Clean(entry))
		if pathHasAncestor(realPath, realEntry) {
			return true, entry
		}
	}
	return false, ""
}

// denyFileTools lists the file-operation tools subject to path-based deny
// checks. Kept local to this file so adding a new file tool only requires
// updating this set (plus adding the handler of course).
var denyFileTools = map[string]bool{
	"read_file":           true,
	"write_file":          true,
	"write_minified_file": true,
	"edit_file":           true,
	"edit_minified_file":  true,
	"delete_file":         true,
}

// checkDenyList gates a tool call against the deny list (paths + URLs).
// Returns a short-circuit ToolResult when the call targets a denied path
// or URL, or nil to let execution proceed. The function is pure (no
// Session dep) to keep the logic testable in isolation.
func checkDenyList(name string, params map[string]any, cwd string, denyPaths, denyURLs []string) *ToolResult {
	if len(denyPaths) == 0 && len(denyURLs) == 0 {
		return nil
	}
	if denyFileTools[name] {
		path, _ := params["path"].(string)
		if path == "" {
			return nil
		}
		if denied, entry := isPathDenied(path, cwd, denyPaths); denied {
			return &ToolResult{
				IsError: true,
				Output:  fmt.Sprintf("Error: path %q is blocked by deny_list entry %q", path, entry),
			}
		}
		return nil
	}
	if name == "web_fetch" {
		rawURL, _ := params["url"].(string)
		if rawURL == "" {
			return nil
		}
		if denied, entry := isURLDenied(rawURL, denyURLs); denied {
			return &ToolResult{
				IsError: true,
				Output:  fmt.Sprintf("Error: url %q is blocked by deny_list entry %q", rawURL, entry),
			}
		}
		return nil
	}
	if name == "bash" {
		command, _ := params["command"].(string)
		if denied, entry, token := bashReferencesDeniedPath(command, cwd, denyPaths); denied {
			return &ToolResult{
				IsError: true,
				Output:  fmt.Sprintf("Error: bash command references path %q blocked by deny_list entry %q", token, entry),
			}
		}
		if denied, entry, token := bashReferencesDeniedURL(command, denyURLs); denied {
			return &ToolResult{
				IsError: true,
				Output:  fmt.Sprintf("Error: bash command references url %q blocked by deny_list entry %q", token, entry),
			}
		}
		return nil
	}
	return nil
}

// isURLDenied returns true if rawURL matches any entry in denyURLs.
// Match semantics:
//   - Entry has a scheme (e.g. "https://example.com/admin"): URL-prefix
//     match after canonicalizing scheme/host casing. Lets operators block
//     a specific endpoint or path under a host.
//   - Entry is hostname-only (e.g. "example.com"): match if the input URL's
//     host equals the entry or has it as a dot-aligned suffix
//     ("api.example.com" matches "example.com" but "notexample.com" does
//     not). This is the common shape and the default reading of a bare
//     hostname.
//   - Malformed URLs fall back to a case-insensitive substring check
//     against the raw entry — best-effort guarantee that obvious matches
//     aren't missed because of an unparseable input.
func isURLDenied(rawURL string, denyURLs []string) (bool, string) {
	if rawURL == "" || len(denyURLs) == 0 {
		return false, ""
	}
	parsed, perr := url.Parse(rawURL)
	for _, entry := range denyURLs {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if hasScheme(entry) {
			if perr != nil {
				if strings.Contains(strings.ToLower(rawURL), strings.ToLower(entry)) {
					return true, entry
				}
				continue
			}
			if urlHasPrefix(parsed, entry) {
				return true, entry
			}
			continue
		}
		// Hostname-only entry.
		if perr == nil && parsed.Host != "" {
			if hostMatches(parsed.Hostname(), entry) {
				return true, entry
			}
			continue
		}
		// Couldn't parse host — fall back to case-insensitive substring.
		if strings.Contains(strings.ToLower(rawURL), strings.ToLower(entry)) {
			return true, entry
		}
	}
	return false, ""
}

// hasScheme reports whether s starts with a `<scheme>://` prefix.
func hasScheme(s string) bool {
	idx := strings.Index(s, "://")
	if idx <= 0 {
		return false
	}
	for i := 0; i < idx; i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// urlHasPrefix reports whether parsed (lowercased scheme/host) starts with
// the entry prefix (also lowercased on scheme/host). Path comparison is
// case-sensitive.
func urlHasPrefix(parsed *url.URL, entry string) bool {
	entryParsed, err := url.Parse(entry)
	if err != nil || entryParsed.Scheme == "" || entryParsed.Host == "" {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, entryParsed.Scheme) {
		return false
	}
	if !strings.EqualFold(parsed.Host, entryParsed.Host) {
		return false
	}
	prefixPath := entryParsed.Path
	if prefixPath == "" || prefixPath == "/" {
		return true
	}
	prefixPath = strings.TrimRight(prefixPath, "/")
	inputPath := parsed.Path
	return inputPath == prefixPath || strings.HasPrefix(inputPath, prefixPath+"/")
}

// hostMatches reports whether host equals entry or has entry as a
// dot-aligned suffix. Both inputs are lowercased.
func hostMatches(host, entry string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	entry = strings.ToLower(strings.TrimSuffix(entry, "."))
	if host == "" || entry == "" {
		return false
	}
	if host == entry {
		return true
	}
	return strings.HasSuffix(host, "."+entry)
}

// bashReferencesDeniedURL scans command for URL-like tokens (anything
// containing "://") and checks each against denyURLs. Best-effort: matches
// obvious explicit URLs in arguments to curl/wget/http etc. Variable
// expansion and reassembled URLs are out of scope.
func bashReferencesDeniedURL(command string, denyURLs []string) (bool, string, string) {
	if command == "" || len(denyURLs) == 0 {
		return false, "", ""
	}
	for _, tok := range splitBashTokens(command) {
		if !strings.Contains(tok, "://") {
			continue
		}
		if denied, entry := isURLDenied(tok, denyURLs); denied {
			return true, entry, tok
		}
	}
	return false, "", ""
}

// bashReferencesDeniedPath scans command for path-like tokens that resolve
// inside a deny_list entry. Best-effort: a token only counts as a path when
// it contains a path separator ('/'). Bare words like `secrets` are NOT
// treated as paths — prose such as `echo 'I have no secrets'` must not
// trigger a block. Variable expansion, heredocs, and reassembly across
// variables are explicitly out of scope for v1.
func bashReferencesDeniedPath(command, cwd string, denyList []string) (bool, string, string) {
	if command == "" || len(denyList) == 0 {
		return false, "", ""
	}
	for _, tok := range splitBashTokens(command) {
		if !strings.ContainsRune(tok, '/') {
			continue
		}
		if denied, entry := isPathDenied(tok, cwd, denyList); denied {
			return true, entry, tok
		}
	}
	return false, "", ""
}

// splitBashTokens produces candidate tokens from a shell command string.
// The split points are shell metacharacters that terminate a word. Inside
// matching quotes (single or double) those metacharacters are treated as
// part of the word — this lets `cat "secrets/a b.txt"` produce the token
// `secrets/a b.txt` rather than being split on the space. Backslash-escapes
// the next character. We also strip the leading `$` from `$(...)` so the
// inner command is scanned for path-like tokens.
func splitBashTokens(command string) []string {
	// Drop `$(` prefixes so backtick- and $-style command substitutions both
	// reduce to "scan the inner command for paths". Replacing with a space
	// keeps token offsets stable without introducing spurious tokens.
	command = strings.ReplaceAll(command, "$(", " ")

	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	i := 0
	for i < len(command) {
		c := command[i]
		switch c {
		case '\\':
			// Copy the next char literally into the current token.
			if i+1 < len(command) {
				cur.WriteByte(command[i+1])
				i += 2
				continue
			}
			i++
		case '\'':
			// Single-quoted: consume until the matching quote.
			j := i + 1
			for j < len(command) && command[j] != '\'' {
				cur.WriteByte(command[j])
				j++
			}
			if j < len(command) {
				i = j + 1
			} else {
				i = j
			}
		case '"':
			// Double-quoted: process escapes but not word-splits.
			j := i + 1
			for j < len(command) && command[j] != '"' {
				if command[j] == '\\' && j+1 < len(command) {
					cur.WriteByte(command[j+1])
					j += 2
					continue
				}
				cur.WriteByte(command[j])
				j++
			}
			if j < len(command) {
				i = j + 1
			} else {
				i = j
			}
		case ' ', '\t', '\n', '|', '&', ';', '<', '>', '(', ')', '`':
			flush()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return tokens
}

// filterOutputAgainstDeny drops grep/glob output lines whose leading path
// token resolves inside a deny_list entry. The input is the raw tool output
// and the function returns the filtered string with line order preserved.
// Orphaned grep group separators ("--") left behind by filtering are also
// removed.
func filterOutputAgainstDeny(output, cwd string, denyList []string) string {
	if output == "" || len(denyList) == 0 {
		return output
	}
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "--" {
			// Deferred: keep only if it sits between two kept match lines.
			// We reconcile below.
			kept = append(kept, line)
			continue
		}
		path := extractLeadingPath(line)
		if path == "" {
			kept = append(kept, line)
			continue
		}
		if denied, _ := isPathDenied(path, cwd, denyList); denied {
			continue
		}
		kept = append(kept, line)
	}
	// Collapse leading/trailing/consecutive "--" separators introduced by
	// filtering neighbouring matches out.
	kept = collapseSeparators(kept)
	return strings.Join(kept, "\n")
}

// extractLeadingPath returns the path portion of a grep/glob output line.
// Grep/rg format: `path:lineno:content` (or `path-lineno-content` for
// context lines). Glob format: `path` alone. We take everything up to the
// first `:` — on macOS file paths cannot contain `:` in POSIX-standard
// filesystems, and on other systems ripgrep/grep already escape it for
// display so this heuristic is sound for the tool output we actually emit.
// Returns "" for lines we cannot parse (blank lines, pure text output).
func extractLeadingPath(line string) string {
	if line == "" {
		return ""
	}
	// glob output: single path, no separators beyond '/'. If the whole line
	// looks like a filesystem path (no ':' and contains '/' or '.'), treat
	// it as a path. An empty string or a line with no discernible path
	// structure is left alone.
	if idx := strings.IndexByte(line, ':'); idx > 0 {
		return line[:idx]
	}
	// No colon — could be glob output. If it contains a '/' or looks like a
	// relative file (has an extension), treat the whole line as the path.
	if strings.ContainsRune(line, '/') {
		return line
	}
	// Bare filename (e.g. `README.md` from a glob in cwd): treat as path.
	if strings.HasPrefix(line, ".") || strings.Contains(line, ".") {
		return line
	}
	return ""
}

// collapseSeparators removes grep "--" markers that no longer separate two
// surviving match groups after deny filtering.
func collapseSeparators(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "--" {
			if len(out) == 0 {
				// Leading separator: drop.
				continue
			}
			if out[len(out)-1] == "--" {
				// Consecutive separators: collapse.
				continue
			}
		}
		out = append(out, line)
	}
	// Trim trailing separator if the last kept line is "--".
	for len(out) > 0 && out[len(out)-1] == "--" {
		out = out[:len(out)-1]
	}
	return out
}
