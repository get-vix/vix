package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

var daemonCtx context.Context

// RegisterBrainHandlers registers brain.* command handlers with the daemon.
func RegisterBrainHandlers(register func(string, func(map[string]any) (map[string]any, error)), cred config.Credential, ctx context.Context) {
	daemonCtx = ctx
	register("brain.init", func(data map[string]any) (map[string]any, error) {
		return doBrainInit(data, cred)
	})
	register("brain.update_files", func(data map[string]any) (map[string]any, error) {
		return doBrainUpdateFiles(data, cred)
	})
}

func doBrainInit(data map[string]any, cred config.Credential) (map[string]any, error) {
	params, _ := data["params"].(map[string]any)
	projectPath, _ := params["project_path"].(string)
	if projectPath == "" {
		projectPath = "."
	}
	root, _ := filepath.Abs(projectPath)

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return map[string]any{"status": "error", "message": fmt.Sprintf("Not a directory: %s", root)}, nil
	}

	// Resolve brain directory from the caller, falling back to the legacy
	// cwd/.vix layout if unset.
	brainDir, _ := params["brain_dir"].(string)
	if brainDir == "" {
		brainDir = filepath.Join(root, ".vix")
	}
	os.MkdirAll(brainDir, 0o755)

	// Resolve the languages.json paths to consult for the ext→language map and
	// LSP server configs, falling back to the home-level config/languages.json
	// if the caller did not supply them. Languages are home-only (not layered
	// with the project), so this is normally a single path.
	var languagesPaths []string
	if raw, ok := params["languages_paths"].([]string); ok {
		languagesPaths = raw
	} else if raw, ok := params["languages_paths"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				languagesPaths = append(languagesPaths, s)
			}
		}
	}
	if len(languagesPaths) == 0 {
		home := config.HomeVixDir()
		if home != "" {
			languagesPaths = append(languagesPaths, filepath.Join(home, "config", "languages.json"))
		}
	}

	// Load language→extension map and initialize LSP pool
	InitLanguageMap(languagesPaths)
	lsp.InitPool(daemonCtx, root, languagesPaths...)

	LogInfo("Brain init complete for %s", root)

	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"project_name": filepath.Base(root),
			"brain_path":   brainDir,
		},
	}, nil
}

func doBrainUpdateFiles(data map[string]any, cred config.Credential) (map[string]any, error) {
	start := time.Now()
	params, _ := data["params"].(map[string]any)

	projectPath, _ := params["project_path"].(string)
	if projectPath == "" {
		projectPath = "."
	}
	root, _ := filepath.Abs(projectPath)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return map[string]any{"status": "error", "message": fmt.Sprintf("Not a directory: %s", root)}, nil
	}

	brainDir, _ := params["brain_dir"].(string)
	if brainDir == "" {
		brainDir = filepath.Join(root, ".vix")
	}
	if err := os.MkdirAll(brainDir, 0o755); err != nil {
		return map[string]any{"status": "error", "message": fmt.Sprintf("Failed to create brain dir: %v", err)}, nil
	}

	files := stringSliceParam(params["files"])
	if len(files) == 0 {
		return map[string]any{"status": "error", "message": "missing 'files'"}, nil
	}

	index, err := loadBrainIndex(brainDir)
	if err != nil {
		return map[string]any{"status": "error", "message": fmt.Sprintf("Failed to load brain index: %v", err)}, nil
	}
	if index.Project.RootPath == "" {
		index.Project.RootPath = root
	}
	if index.Project.Name == "" {
		index.Project.Name = filepath.Base(root)
	}

	updated := 0
	removed := 0
	for _, filePath := range files {
		relPath, ok := normalizeProjectPath(root, filePath)
		if !ok {
			continue
		}

		fileInfo := ScanSingleFile(root, relPath)
		if fileInfo == nil {
			if removeFileFromIndex(index, relPath) {
				removed++
			}
			continue
		}

		upsertFileInfo(index, *fileInfo)
		refreshFileAnalysis(index, root, *fileInfo)
		updated++
	}

	recomputeProjectMetadata(index, root)
	if err := saveBrainIndex(brainDir, index); err != nil {
		return map[string]any{"status": "error", "message": fmt.Sprintf("Failed to save brain index: %v", err)}, nil
	}

	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"updated_files":    updated,
			"removed_files":    removed,
			"duration_seconds": time.Since(start).Seconds(),
		},
	}, nil
}

func stringSliceParam(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeProjectPath(root, filePath string) (string, bool) {
	if filePath == "" {
		return "", false
	}
	var absPath string
	if filepath.IsAbs(filePath) {
		absPath = filepath.Clean(filePath)
	} else {
		absPath = filepath.Join(root, filepath.Clean(filePath))
	}
	relPath, err := filepath.Rel(root, absPath)
	if err != nil || relPath == "." || relPath == "" || relPath == ".." || len(relPath) >= 3 && relPath[:3] == "../" {
		return "", false
	}
	return relPath, true
}

func loadBrainIndex(brainDir string) (*BrainIndex, error) {
	path := filepath.Join(brainDir, "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &BrainIndex{}, nil
		}
		return nil, err
	}
	var index BrainIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return &index, nil
}

func saveBrainIndex(brainDir string, index *BrainIndex) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(brainDir, "index.json"), append(data, '\n'), 0o644)
}

func upsertFileInfo(index *BrainIndex, fileInfo FileInfo) {
	for i, existing := range index.Files {
		if existing.Path == fileInfo.Path {
			index.Files[i] = fileInfo
			return
		}
	}
	index.Files = append(index.Files, fileInfo)
}

func removeFileFromIndex(index *BrainIndex, relPath string) bool {
	found := false
	files := index.Files[:0]
	for _, fileInfo := range index.Files {
		if fileInfo.Path == relPath {
			found = true
			continue
		}
		files = append(files, fileInfo)
	}
	index.Files = files
	removeFileAnalysis(index, relPath)
	return found
}

func refreshFileAnalysis(index *BrainIndex, root string, fileInfo FileInfo) {
	removeFileAnalysis(index, fileInfo.Path)
	if fileInfo.Language == "" {
		return
	}

	symbols, calls := ParseFile(fileInfo.Path, root, fileInfo.Language)
	index.Symbols = append(index.Symbols, symbols...)
	index.Calls = append(index.Calls, calls...)

	data, err := os.ReadFile(filepath.Join(root, fileInfo.Path))
	if err != nil {
		return
	}
	filePaths := make(map[string]bool, len(index.Files))
	for _, file := range index.Files {
		filePaths[file.Path] = true
	}
	index.Imports = append(index.Imports, ExtractFileImports(string(data), fileInfo.Path, filePaths, root, fileInfo.Language)...)
}

func removeFileAnalysis(index *BrainIndex, relPath string) {
	symbols := index.Symbols[:0]
	for _, symbol := range index.Symbols {
		if symbol.FilePath != relPath {
			symbols = append(symbols, symbol)
		}
	}
	index.Symbols = symbols

	calls := index.Calls[:0]
	for _, call := range index.Calls {
		if call.FilePath != relPath {
			calls = append(calls, call)
		}
	}
	index.Calls = calls

	imports := index.Imports[:0]
	for _, imp := range index.Imports {
		if imp.SourceFile != relPath && imp.TargetFile != relPath {
			imports = append(imports, imp)
		}
	}
	index.Imports = imports
}

func recomputeProjectMetadata(index *BrainIndex, root string) {
	sort.Slice(index.Files, func(i, j int) bool { return index.Files[i].Path < index.Files[j].Path })
	sort.Slice(index.Symbols, func(i, j int) bool { return index.Symbols[i].ID < index.Symbols[j].ID })
	sort.Slice(index.Imports, func(i, j int) bool {
		if index.Imports[i].SourceFile == index.Imports[j].SourceFile {
			return index.Imports[i].Module < index.Imports[j].Module
		}
		return index.Imports[i].SourceFile < index.Imports[j].SourceFile
	})

	languages := make(map[string]int)
	totalLines := 0
	entryPoints := make([]string, 0)
	configFiles := make([]string, 0)
	for _, fileInfo := range index.Files {
		if fileInfo.Language != "" {
			languages[fileInfo.Language]++
		}
		totalLines += fileInfo.LineCount
		if fileInfo.IsEntryPoint {
			entryPoints = append(entryPoints, fileInfo.Path)
		}
		if fileInfo.IsConfig {
			configFiles = append(configFiles, fileInfo.Path)
		}
	}
	sort.Strings(entryPoints)
	sort.Strings(configFiles)

	externalDeps := ParseDependencies(root)
	frameworks := DetectFrameworks(root, index.Files, externalDeps)
	index.Project = ProjectMeta{
		Name:              filepath.Base(root),
		RootPath:          root,
		TotalFiles:        len(index.Files),
		TotalLines:        totalLines,
		Languages:         languages,
		EntryPoints:       entryPoints,
		ConfigFiles:       configFiles,
		ExternalDeps:      externalDeps,
		Frameworks:        frameworks.Frameworks,
		Patterns:          frameworks.Patterns,
		TestingFrameworks: frameworks.Testing,
		CICD:              frameworks.CICD,
	}
	index.HubFiles = FindHubFiles(index.Imports, 10)
}
