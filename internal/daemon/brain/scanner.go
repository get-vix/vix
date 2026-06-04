package brain

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

var skipDirs = map[string]bool{
	".git": true, ".vix": true, ".vix-temp": true, "__pycache__": true,
	"node_modules": true, ".venv": true, "venv": true, ".env": true,
	"env": true, ".tox": true, ".mypy_cache": true, ".pytest_cache": true,
	".ruff_cache": true, "dist": true, "build": true, ".next": true,
	".nuxt": true, "target": true, ".idea": true, ".vscode": true,
	".DS_Store": true, "coverage": true, ".coverage": true,
	"htmlcov": true, "egg-info": true,
}

var (
	extMapMu sync.Mutex
	extMap   map[string]string // extension → language name, built from settings.json
)

// InitLanguageMap loads the extension→language mapping from the given
// settings.json paths (in load order, later entries override earlier by
// language Name). Must be called before LanguageForExt (typically at brain
// init or daemon startup). Safe to call multiple times; only the first call
// takes effect.
func InitLanguageMap(settingsPaths []string) {
	extMapMu.Lock()
	defer extMapMu.Unlock()
	if extMap != nil {
		return
	}
	extMap = buildExtMap(settingsPaths)
}

// InitLanguageMapFromConfigs initializes the ext→language map directly from configs.
// Useful in tests where settings.json may not be available.
func InitLanguageMapFromConfigs(configs []lsp.LanguageConfig) {
	extMapMu.Lock()
	defer extMapMu.Unlock()
	if extMap != nil {
		return
	}
	extMap = make(map[string]string)
	for _, lc := range configs {
		for _, ext := range lc.Extensions {
			extMap[ext] = lc.Name
		}
	}
}

// buildExtMap merges language configs from the given settings.json paths
// in order (later entries override earlier by language Name).
func buildExtMap(settingsPaths []string) map[string]string {
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

	m := make(map[string]string)
	for _, lc := range merged {
		for _, ext := range lc.Extensions {
			m[ext] = lc.Name
		}
	}
	return m
}

// LanguageForExt returns the language name for a file extension (e.g. ".go" → "go").
// Returns "" if the extension is not recognized.
// Uses the LSP pool if initialized, otherwise falls back to the pre-loaded ext map.
func LanguageForExt(ext string) string {
	if pool := lsp.GetPool(); pool != nil {
		if lang := pool.LanguageForExt(ext); lang != "" {
			return lang
		}
	}
	return extMap[ext]
}

var entryPointNames = map[string]bool{
	"main.py": true, "__main__.py": true, "app.py": true, "server.py": true,
	"index.js": true, "index.ts": true, "main.js": true, "main.ts": true,
	"main.go": true, "main.rs": true, "lib.rs": true, "mod.rs": true,
	"manage.py": true, "wsgi.py": true, "asgi.py": true,
}

var configNames = map[string]bool{
	"package.json": true, "pyproject.toml": true, "setup.py": true, "setup.cfg": true,
	"requirements.txt": true, "Pipfile": true, "Cargo.toml": true,
	"go.mod": true, "go.sum": true, "Gemfile": true, "Makefile": true,
	"Dockerfile": true, "docker-compose.yml": true, "docker-compose.yaml": true,
	".env.example": true, "tsconfig.json": true,
	"webpack.config.js": true, "vite.config.ts": true, "vite.config.js": true,
	"jest.config.js": true, "jest.config.ts": true, "pytest.ini": true,
	"tox.ini": true, ".eslintrc.js": true, ".eslintrc.json": true,
	".prettierrc": true, "nginx.conf": true, "Procfile": true,
}

var testPatterns = []string{"test_", "_test.", ".test.", ".spec.", "tests/", "test/"}

func parseGitignore(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

func isIgnored(relPath, name string, isDir bool, patterns []string) bool {
	for _, pat := range patterns {
		if strings.HasSuffix(pat, "/") {
			dirPat := strings.TrimSuffix(pat, "/")
			if isDir {
				if matched, _ := doublestar.Match(dirPat, name); matched {
					return true
				}
				if matched, _ := doublestar.Match(dirPat, relPath); matched {
					return true
				}
			}
			if matched, _ := doublestar.Match(dirPat, name); matched {
				return true
			}
			continue
		}
		if matched, _ := doublestar.Match(pat, name); matched {
			return true
		}
		if matched, _ := doublestar.Match(pat, relPath); matched {
			return true
		}
	}
	return false
}

func isBinary(data []byte) bool {
	limit := 8192
	if len(data) < limit {
		limit = len(data)
	}
	return bytes.Contains(data[:limit], []byte{0})
}

func isTestFile(relPath string) bool {
	lower := strings.ToLower(relPath)
	for _, p := range testPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// ScanProject crawls the project directory and returns FileInfo for every source file.
func ScanProject(root string) []FileInfo {
	root, _ = filepath.Abs(root)
	gitignorePatterns := parseGitignore(root)
	var files []FileInfo

	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			fullPath := filepath.Join(dir, name)
			relPath, _ := filepath.Rel(root, fullPath)

			// Resolve symlinks so that symlinked directories are traversed
			// and symlinked files are treated as regular files.
			isDir := e.IsDir()
			isRegular := e.Type().IsRegular()
			if e.Type()&os.ModeSymlink != 0 {
				info, err := os.Stat(fullPath)
				if err != nil {
					continue // dangling symlink
				}
				isDir = info.IsDir()
				isRegular = info.Mode().IsRegular()
			}

			if strings.HasPrefix(name, ".") && name != ".env.example" {
				if isDir {
					continue
				}
				ext := strings.ToLower(filepath.Ext(name))
				if LanguageForExt(ext) == "" {
					if !configNames[name] {
						continue
					}
				}
			}

			if isDir {
				if skipDirs[name] {
					continue
				}
				if isIgnored(relPath, name, true, gitignorePatterns) {
					continue
				}
				walk(fullPath)
				continue
			}

			if !isRegular {
				continue
			}

			if isIgnored(relPath, name, false, gitignorePatterns) {
				continue
			}

			ext := strings.ToLower(filepath.Ext(name))
			language := LanguageForExt(ext)
			isConfig := configNames[name]
			if language == "" && !isConfig {
				continue
			}

			raw, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			if isBinary(raw) {
				continue
			}

			lineCount := bytes.Count(raw, []byte{'\n'})

			files = append(files, FileInfo{
				Path:         relPath,
				Language:     language,
				SizeBytes:    len(raw),
				LineCount:    lineCount,
				SHA256:       ContentHash(raw),
				IsEntryPoint: entryPointNames[name],
				IsConfig:     isConfig,
				IsTest:       isTestFile(relPath),
			})
		}
	}

	walk(root)
	LogInfo("Scanned %d files in %s", len(files), root)
	return files
}

// ScanSingleFile scans a single file and returns its FileInfo, or nil if not valid.
func ScanSingleFile(root, relPath string) *FileInfo {
	root, _ = filepath.Abs(root)
	fullPath := filepath.Join(root, relPath)

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		return nil
	}

	name := filepath.Base(relPath)

	// Skip dotfiles that aren't configs
	if strings.HasPrefix(name, ".") && name != ".env.example" {
		ext := strings.ToLower(filepath.Ext(name))
		if LanguageForExt(ext) == "" {
			if !configNames[name] {
				return nil
			}
		}
	}

	// Skip files in ignored dirs
	parts := strings.Split(relPath, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		if skipDirs[part] {
			return nil
		}
	}

	gitignorePatterns := parseGitignore(root)
	if isIgnored(relPath, name, false, gitignorePatterns) {
		return nil
	}

	ext := strings.ToLower(filepath.Ext(name))
	language := LanguageForExt(ext)
	isConfig := configNames[name]
	if language == "" && !isConfig {
		return nil
	}

	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return nil
	}
	if isBinary(raw) {
		return nil
	}

	lineCount := bytes.Count(raw, []byte{'\n'})
	return &FileInfo{
		Path:         relPath,
		Language:     language,
		SizeBytes:    len(raw),
		LineCount:    lineCount,
		SHA256:       ContentHash(raw),
		IsEntryPoint: entryPointNames[name],
		IsConfig:     isConfig,
		IsTest:       isTestFile(relPath),
	}
}
