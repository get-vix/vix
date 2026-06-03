package brain

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

var (
	pyImportRe     = regexp.MustCompile(`^import\s+([\w.]+)`)
	pyFromImportRe = regexp.MustCompile(`^from\s+(\.+[\w.]*|[\w.]+)\s+import`)
	jsImportFromRe = regexp.MustCompile(`import\s+.*?from\s+['"]([^'"]+)['"]`)
	jsRequireRe    = regexp.MustCompile(`require\s*\(\s*['"]([^'"]+)['"]\s*\)`)

	goImportSingleRe = regexp.MustCompile(`^import\s+"([^"]+)"`)
	goImportGroupRe  = regexp.MustCompile(`^\s+"([^"]+)"`)
)

// BuildImportGraph extracts import statements and resolves internal targets via LSP.
func BuildImportGraph(root string, files []FileInfo) []ImportInfo {
	filePaths := make(map[string]bool)
	for _, f := range files {
		filePaths[f.Path] = true
	}

	pool := lsp.GetPool()

	var imports []ImportInfo
	for _, fi := range files {
		if fi.Language == "" {
			continue
		}

		absPath := filepath.Join(root, fi.Path)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		source := string(data)

		locs := findImportLocations(source, fi.Language)
		if len(locs) == 0 {
			continue
		}

		// Try LSP resolution
		var client *lsp.Client
		if pool != nil {
			client, _ = pool.GetClient(fi.Language)
		}

		uri := "file://" + absPath
		if client != nil {
			client.DidOpen(uri, fi.Language, source)
		}

		for _, loc := range locs {
			imp := ImportInfo{
				SourceFile: fi.Path,
				Module:     loc.Module,
			}

			if client != nil {
				target, err := lsp.ResolveImport(client, uri, root, loc)
				if err == nil && target != "" {
					imp.TargetFile = target
					imp.IsExternal = false
				} else {
					imp.TargetFile = ""
					imp.IsExternal = true
				}
			} else {
				imp.TargetFile = ""
				imp.IsExternal = true
			}

			imports = append(imports, imp)
		}

		if client != nil {
			client.DidClose(uri)
		}
	}

	LogInfo("Built import graph: %d edges", len(imports))
	return imports
}

// ExtractFileImports extracts imports from a single file's source.
func ExtractFileImports(source, filePath string, filePaths map[string]bool, root, language string) []ImportInfo {
	locs := findImportLocations(source, language)
	if len(locs) == 0 {
		return nil
	}

	pool := lsp.GetPool()
	var client *lsp.Client
	if pool != nil {
		client, _ = pool.GetClient(language)
	}

	absPath := filepath.Join(root, filePath)
	uri := "file://" + absPath

	if client != nil {
		client.DidOpen(uri, language, source)
		defer client.DidClose(uri)
	}

	var imports []ImportInfo
	for _, loc := range locs {
		imp := ImportInfo{
			SourceFile: filePath,
			Module:     loc.Module,
		}

		if client != nil {
			target, err := lsp.ResolveImport(client, uri, root, loc)
			if err == nil && target != "" {
				imp.TargetFile = target
				imp.IsExternal = false
			} else {
				imp.TargetFile = ""
				imp.IsExternal = true
			}
		} else {
			imp.TargetFile = ""
			imp.IsExternal = true
		}

		imports = append(imports, imp)
	}

	return imports
}

func findImportLocations(source, language string) []lsp.ImportLocation {
	switch language {
	case "python":
		return findPythonImportLocations(source)
	case "javascript", "typescript":
		return findJSTSImportLocations(source)
	case "go":
		return findGoImportLocations(source)
	}
	return nil
}

func findPythonImportLocations(source string) []lsp.ImportLocation {
	var locs []lsp.ImportLocation
	for i, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)

		if m := pyImportRe.FindStringSubmatchIndex(trimmed); m != nil {
			// m[2]:m[3] is the capture group (module name)
			// Find the offset in the original line
			offset := strings.Index(line, trimmed)
			locs = append(locs, lsp.ImportLocation{
				Line:      i,
				Character: offset + m[2],
				Module:    trimmed[m[2]:m[3]],
			})
			continue
		}

		if m := pyFromImportRe.FindStringSubmatchIndex(trimmed); m != nil {
			offset := strings.Index(line, trimmed)
			locs = append(locs, lsp.ImportLocation{
				Line:      i,
				Character: offset + m[2],
				Module:    trimmed[m[2]:m[3]],
			})
		}
	}
	return locs
}

func findJSTSImportLocations(source string) []lsp.ImportLocation {
	var locs []lsp.ImportLocation
	lines := strings.Split(source, "\n")

	for i, line := range lines {
		for _, re := range []*regexp.Regexp{jsImportFromRe, jsRequireRe} {
			if m := re.FindStringSubmatchIndex(line); m != nil {
				// m[2]:m[3] is the capture group (module path)
				locs = append(locs, lsp.ImportLocation{
					Line:      i,
					Character: m[2],
					Module:    line[m[2]:m[3]],
				})
			}
		}
	}

	return locs
}

func findGoImportLocations(source string) []lsp.ImportLocation {
	var locs []lsp.ImportLocation
	lines := strings.Split(source, "\n")
	inGroup := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "import (") {
			inGroup = true
			continue
		}
		if inGroup && trimmed == ")" {
			inGroup = false
			continue
		}

		if inGroup {
			if m := goImportGroupRe.FindStringSubmatchIndex(line); m != nil {
				locs = append(locs, lsp.ImportLocation{
					Line:      i,
					Character: m[2],
					Module:    line[m[2]:m[3]],
				})
			}
			continue
		}

		if m := goImportSingleRe.FindStringSubmatchIndex(line); m != nil {
			locs = append(locs, lsp.ImportLocation{
				Line:      i,
				Character: m[2],
				Module:    line[m[2]:m[3]],
			})
		}
	}

	return locs
}

// FindHubFiles returns the top N most-imported internal files.
func FindHubFiles(imports []ImportInfo, topN int) []HubFile {
	counts := make(map[string]int)
	for _, imp := range imports {
		if imp.TargetFile != "" {
			counts[imp.TargetFile]++
		}
	}

	type kv struct {
		path  string
		count int
	}
	var sorted []kv
	for p, c := range counts {
		sorted = append(sorted, kv{p, c})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	if topN > len(sorted) {
		topN = len(sorted)
	}
	hubs := make([]HubFile, topN)
	for i := 0; i < topN; i++ {
		hubs[i] = HubFile{Path: sorted[i].path, ImportCount: sorted[i].count}
	}

	if len(hubs) > 0 {
		paths := make([]string, len(hubs))
		for i, h := range hubs {
			paths[i] = h.Path
		}
		LogInfo("Hub files (top %d): %v", topN, paths)
	}
	return hubs
}
