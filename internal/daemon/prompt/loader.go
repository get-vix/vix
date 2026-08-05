package prompt

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	// $(...) syntax for variables, file inclusion, and function calls
	dollarPattern = regexp.MustCompile(`\$\(([^)]+)\)`)
)

// Loader manages loading and caching of prompt templates with variable substitution.
type Loader struct {
	mu    sync.RWMutex
	cache map[string]string
}

var (
	globalLoader     *Loader
	globalLoaderOnce sync.Once
)

// GetLoader returns the global prompt loader singleton.
func GetLoader() *Loader {
	globalLoaderOnce.Do(func() {
		globalLoader = &Loader{
			cache: make(map[string]string),
		}
	})
	return globalLoader
}

// Load reads a prompt template from the given path, resolves placeholders, and returns
// the processed content. Supports $() syntax for variables, $(file:) for file inclusion,
// and $(call:) for function calls. If the file contains YAML frontmatter (delimited by ---),
// it is stripped before processing.
func (l *Loader) Load(templatePath string, vars map[string]string, brainDir string, funcs map[string]func() string) string {
	// Check cache first
	l.mu.RLock()
	if cached, ok := l.cache[templatePath]; ok {
		l.mu.RUnlock()
		return l.resolve(cached, vars, brainDir, funcs)
	}
	l.mu.RUnlock()

	// Load template from disk
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return "Error: prompt template not found at " + templatePath
	}

	// Strip YAML frontmatter if present
	raw := stripFrontmatter(string(content))

	// Cache the processed template (after frontmatter removal)
	l.mu.Lock()
	l.cache[templatePath] = raw
	l.mu.Unlock()

	return l.resolve(raw, vars, brainDir, funcs)
}

// resolve iteratively processes placeholders until stable (max 3 passes).
// This handles nested templates where $(file:...) loads content containing more $() refs.
func (l *Loader) resolve(template string, vars map[string]string, brainDir string, funcs map[string]func() string) string {
	result := template
	for i := 0; i < 3; i++ {
		next := l.resolveOnce(result, vars, brainDir, funcs)
		if next == result {
			break
		}
		result = next
	}
	return result
}

// resolveOnce performs a single pass of placeholder resolution.
func (l *Loader) resolveOnce(template string, vars map[string]string, brainDir string, funcs map[string]func() string) string {
	result := template

	// $(...) syntax
	result = dollarPattern.ReplaceAllStringFunc(result, func(match string) string {
		inner := match[2 : len(match)-1] // strip $( and )

		if strings.HasPrefix(inner, "file:") {
			path := strings.TrimPrefix(inner, "file:")
			if brainDir == "" {
				return match
			}
			// Try brainDir first; if it contains a separator, try multiple dirs
			for _, dir := range l.splitDirs(brainDir) {
				fullPath := filepath.Join(dir, path)
				content, err := os.ReadFile(fullPath)
				if err == nil {
					return string(content)
				}
			}
			return "[Error: file '" + path + "' doesn't exist]"
		}

		if strings.HasPrefix(inner, "call:") {
			name := strings.TrimPrefix(inner, "call:")
			if funcs == nil {
				return ""
			}
			if fn, ok := funcs[name]; ok {
				return fn()
			}
			return ""
		}

		// Variable lookup
		if vars != nil {
			if value, ok := vars[inner]; ok {
				return value
			}
		}
		return match // keep unresolved
	})

	return result
}

// Resolve processes placeholders in an inline template string.
// This is the public version of resolve(), for use by the workflow engine.
func (l *Loader) Resolve(template string, vars map[string]string, brainDir string, funcs map[string]func() string) string {
	return l.resolve(template, vars, brainDir, funcs)
}

// splitDirs splits brainDir into search directories. Multiple dirs are
// separated by null byte (\x00). Returns at least one entry.
func (l *Loader) splitDirs(brainDir string) []string {
	if brainDir == "" {
		return nil
	}
	return strings.Split(brainDir, "\x00")
}

// JoinSearchDirs joins multiple search directories into a single brainDir string.
// Pass to Load/Resolve as brainDir — $(file:) will search dirs in order.
func JoinSearchDirs(dirs ...string) string {
	var nonEmpty []string
	for _, d := range dirs {
		if d != "" {
			nonEmpty = append(nonEmpty, d)
		}
	}
	return strings.Join(nonEmpty, "\x00")
}

// ClearCache removes all cached templates, forcing a reload on next access.
func (l *Loader) ClearCache() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache = make(map[string]string)
}

// stripFrontmatter removes YAML frontmatter from the beginning of a string.
// If the string starts with "---", it removes everything up to and including
// the closing "---" on its own line. If no frontmatter is present, returns the original string.
func stripFrontmatter(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return content
	}

	// Check if the first line is "---"
	if strings.TrimSpace(lines[0]) != "---" {
		return content
	}

	// Find the closing "---"
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			// Return everything after the closing "---"
			return strings.Join(lines[i+1:], "\n")
		}
	}

	// If we didn't find a closing "---", return the original content
	return content
}
