package ui

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

const fileCompleterMaxVisible = 8

// truncatePathLeft shortens p to fit width columns, keeping the tail (the most
// specific path segments) and prefixing with an ellipsis when truncated.
func truncatePathLeft(p string, width int) string {
	if width <= 1 || lipgloss.Width(p) <= width {
		return p
	}
	runes := []rune(p)
	keep := width - 1
	if keep >= len(runes) {
		return p
	}
	return "…" + string(runes[len(runes)-keep:])
}

// resolveWorkDir normalizes a working-directory string (from the picker or a
// typed path) to an absolute path, expanding a leading ~ to the home directory.
func resolveWorkDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return dir
	}
	if dir == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		}
	} else if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// FileCompleter is a popup that lists filesystem entries matching a typed @-query.
type FileCompleter struct {
	visible    bool
	currentDir string // absolute path being listed
	query      string // prefix filter (part after last / in the @-token)
	entries    []os.DirEntry
	selected   int
	// dirsOnly restricts the listing to directories. Used by the welcome-screen
	// working-directory picker (Ctrl+O on a draft thread).
	dirsOnly bool
	// title overrides the popup's border title. Empty renders the default
	// " Files " label.
	title string
}

// OpenDir shows the completer in directory-only mode, rooted at dir. Used by the
// draft welcome screen to let the user pick a working directory before the
// thread starts.
func (f *FileCompleter) OpenDir(dir string) {
	f.dirsOnly = true
	f.title = " Select working directory "
	f.visible = true
	f.currentDir = resolveWorkDir(dir)
	f.query = ""
	f.reload()
	f.selected = 0
}

// CurrentDir returns the directory currently being listed.
func (f *FileCompleter) CurrentDir() string { return f.currentDir }

// Parent navigates to the parent of the current directory, resetting the query.
func (f *FileCompleter) Parent() {
	parent := filepath.Dir(f.currentDir)
	if parent == f.currentDir {
		return // already at root
	}
	f.currentDir = parent
	f.query = ""
	f.reload()
	f.selected = 0
}

// Open shows the completer for the given directory and filter prefix.
func (f *FileCompleter) Open(dir, query string) {
	f.dirsOnly = false
	f.title = ""
	f.visible = true
	f.currentDir = dir
	f.query = query
	f.reload()
	f.selected = 0
}

// Refresh updates the filter query and reloads entries without changing the directory.
func (f *FileCompleter) Refresh(query string) {
	f.query = query
	f.reload()
	if f.selected >= len(f.entries) {
		f.selected = max(0, len(f.entries)-1)
	}
}

// Close hides the completer.
func (f *FileCompleter) Close() {
	f.visible = false
}

// IsVisible returns whether the popup is showing.
func (f *FileCompleter) IsVisible() bool {
	return f.visible
}

// MoveUp moves the selection toward earlier entries.
func (f *FileCompleter) MoveUp() {
	if f.selected > 0 {
		f.selected--
	}
}

// MoveDown moves the selection toward later entries.
func (f *FileCompleter) MoveDown() {
	if f.selected < len(f.entries)-1 {
		f.selected++
	}
}

// SelectedEntry returns the currently highlighted DirEntry, or nil if the list is empty.
func (f *FileCompleter) SelectedEntry() os.DirEntry {
	if len(f.entries) == 0 || f.selected < 0 || f.selected >= len(f.entries) {
		return nil
	}
	return f.entries[f.selected]
}

// SelectedPath returns the absolute path of the currently highlighted entry.
func (f *FileCompleter) SelectedPath() string {
	entry := f.SelectedEntry()
	if entry == nil {
		return ""
	}
	return filepath.Join(f.currentDir, entry.Name())
}

// Descend navigates into a directory entry, resetting the query and reloading.
func (f *FileCompleter) Descend(entry os.DirEntry) {
	f.currentDir = filepath.Join(f.currentDir, entry.Name())
	f.query = ""
	f.reload()
	f.selected = 0
}

// reload reads currentDir and filters entries by the current query prefix.
func (f *FileCompleter) reload() {
	all, err := os.ReadDir(f.currentDir)
	if err != nil {
		f.entries = nil
		return
	}
	lowerQuery := strings.ToLower(f.query)
	var filtered []os.DirEntry
	for _, e := range all {
		entry := resolvedDirEntry(f.currentDir, e)
		// Directory-only mode (working-directory picker) skips plain files.
		if f.dirsOnly && !entry.IsDir() {
			continue
		}
		// Skip hidden files (starting with .) unless the query starts with .
		if strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(lowerQuery, ".") {
			continue
		}
		if lowerQuery == "" || strings.HasPrefix(strings.ToLower(e.Name()), lowerQuery) {
			filtered = append(filtered, entry)
		}
	}
	f.entries = filtered
}

// symlinkDirEntry wraps a DirEntry and overrides IsDir to return true when the
// entry is a symlink pointing to a directory.
type symlinkDirEntry struct {
	os.DirEntry
	isDir bool
}

func (s symlinkDirEntry) IsDir() bool { return s.isDir }

// resolvedDirEntry returns the entry unchanged if it is not a symlink.
// For symlinks it wraps the entry so that IsDir() reflects the symlink target.
func resolvedDirEntry(dir string, e os.DirEntry) os.DirEntry {
	if e.Type()&os.ModeSymlink == 0 {
		return e
	}
	info, err := os.Stat(filepath.Join(dir, e.Name()))
	if err != nil {
		return e
	}
	return symlinkDirEntry{DirEntry: e, isDir: info.IsDir()}
}

// View renders the file completer popup. Returns an empty string when not visible.
func (f *FileCompleter) View(width, maxHeight int, s Styles) string {
	if !f.visible {
		return ""
	}

	maxRows := maxHeight
	if maxRows > fileCompleterMaxVisible {
		maxRows = fileCompleterMaxVisible
	}

	// Build the border
	borderColor := colorPrimary
	borderCharStyle := lipgloss.NewStyle().Foreground(borderColor)

	title := " Files "
	if f.title != "" {
		title = f.title
	}
	titleStyle := lipgloss.NewStyle().Foreground(borderColor)
	titleRendered := titleStyle.Render(title)
	titleLen := lipgloss.Width(titleRendered)
	remainingDashes := width - 3 - titleLen
	if remainingDashes < 0 {
		remainingDashes = 0
	}
	topBorder := borderCharStyle.Render("╭─") + titleRendered +
		borderCharStyle.Render(strings.Repeat("─", remainingDashes)) +
		borderCharStyle.Render("╮")

	// Compute visible window around selected
	total := len(f.entries)
	if total == 0 {
		emptyLine := lipgloss.NewStyle().Foreground(colorDim).Render("  (no matches)")
		body := s.FileCompleterStyle.Width(width).Render(emptyLine)
		return topBorder + "\n" + body
	}

	if maxRows > total {
		maxRows = total
	}

	startIdx := 0
	if f.selected >= maxRows {
		startIdx = f.selected - maxRows + 1
	}
	endIdx := startIdx + maxRows
	if endIdx > total {
		endIdx = total
		startIdx = max(0, endIdx-maxRows)
	}

	innerWidth := width - 4 // border (2) + padding (2)
	if innerWidth < 1 {
		innerWidth = 1
	}

	var rows []string
	for i := startIdx; i < endIdx; i++ {
		e := f.entries[i]
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		name = lipgloss.NewStyle().MaxWidth(innerWidth).Render(name)

		if i == f.selected {
			row := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("▸ " + name)
			rows = append(rows, row)
		} else {
			row := lipgloss.NewStyle().Foreground(colorAccentCool).Width(innerWidth).Render("  " + name)
			rows = append(rows, row)
		}
	}

	content := strings.Join(rows, "\n")
	body := s.FileCompleterStyle.Width(width).Render(content)
	return topBorder + "\n" + body
}

// extractAtQuery scans the textarea value for an active @-token.
// It returns the path query (everything after @) and true if found.
// Examples:
//
//	"hello @src/u"  → ("src/u", true)
//	"hello @"       → ("", true)
//	"hello world"   → ("", false)
func extractAtQuery(value string) (query string, found bool) {
	// Find the last @ character
	atIdx := strings.LastIndex(value, "@")
	if atIdx < 0 {
		return "", false
	}
	// The @ must not be followed by whitespace-only — it must be at end or
	// followed by a path segment. Also ensure there's no whitespace between
	// @ and the cursor (i.e. the token after @ has no internal spaces).
	rest := value[atIdx+1:]
	// If rest contains a space or newline, the @ token has ended
	for _, r := range rest {
		if unicode.IsSpace(r) {
			return "", false
		}
	}
	return rest, true
}

// resolveAtDir splits a @-query into a directory to list and a filter prefix.
// The directory is absolute: relative paths are joined to cwd.
// Examples:
//
//	query="src/u",    cwd="/project" → dir="/project/src", prefix="u"
//	query="",         cwd="/project" → dir="/project",     prefix=""
//	query="/etc/pas", cwd="/project" → dir="/etc",         prefix="pas"
func resolveAtDir(query, cwd string) (dir, prefix string) {
	if query == "" {
		return cwd, ""
	}
	// Split on last /
	lastSlash := strings.LastIndex(query, "/")
	if lastSlash < 0 {
		// No slash — list cwd, filter by full query
		return cwd, query
	}
	dirPart := query[:lastSlash]
	prefix = query[lastSlash+1:]

	if filepath.IsAbs(dirPart) {
		dir = dirPart
	} else {
		dir = filepath.Join(cwd, dirPart)
	}
	// Handle trailing slash: query like "src/" means list src dir with no filter
	if dirPart == "" {
		// query started with "/" → absolute root
		dir = "/"
	}
	return dir, prefix
}

// replaceAtToken replaces the last @-token in value with replacement.
// The replacement includes the @ prefix itself being removed — only the
// raw replacement string is inserted (callers pass the path without @).
func replaceAtToken(value, replacement string) string {
	atIdx := strings.LastIndex(value, "@")
	if atIdx < 0 {
		return value + replacement
	}
	// Find end of token: first whitespace after @, or end of string
	rest := value[atIdx+1:]
	tokenEnd := len(rest)
	for i, r := range rest {
		if unicode.IsSpace(r) {
			tokenEnd = i
			break
		}
	}
	return value[:atIdx] + replacement + value[atIdx+1+tokenEnd:]
}
