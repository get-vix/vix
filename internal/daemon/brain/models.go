package brain

// FileInfo holds metadata for a single project file.
type FileInfo struct {
	Path         string `json:"path"`
	Language     string `json:"language"`
	SizeBytes    int    `json:"size_bytes"`
	LineCount    int    `json:"line_count"`
	SHA256       string `json:"sha256"`
	IsEntryPoint bool   `json:"is_entry_point"`
	IsConfig     bool   `json:"is_config"`
	IsTest       bool   `json:"is_test"`
}

// SymbolInfo represents an extracted symbol (function, class, method).
type SymbolInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"` // LSP SymbolKind name: "Function", "Method", "Class", "Struct", "Interface", etc.
	FilePath   string   `json:"file_path"`
	LineStart  int      `json:"line_start"`
	LineEnd    int      `json:"line_end"`
	Parameters []string `json:"parameters"`
	ReturnType string   `json:"return_type"`
	Decorators []string `json:"decorators"`
	Docstring  string   `json:"docstring"`
	Complexity int      `json:"complexity"`
	Parent     string   `json:"parent"`
}

// ImportInfo represents an import edge in the dependency graph.
type ImportInfo struct {
	SourceFile string `json:"source_file"`
	TargetFile string `json:"target_file"`
	Module     string `json:"module"`
	IsExternal bool   `json:"is_external"`
}

// CallInfo represents a function/method call.
type CallInfo struct {
	CallerID   string `json:"caller_id"`
	CalleeName string `json:"callee_name"`
	FilePath   string `json:"file_path"`
}

// ProjectMeta holds project-level metadata.
type ProjectMeta struct {
	Name              string         `json:"name"`
	RootPath          string         `json:"root_path"`
	TotalFiles        int            `json:"total_files"`
	TotalLines        int            `json:"total_lines"`
	Languages         map[string]int `json:"languages"`
	EntryPoints       []string       `json:"entry_points"`
	ConfigFiles       []string       `json:"config_files"`
	ExternalDeps      []string       `json:"external_deps"`
	Frameworks        []string       `json:"frameworks"`
	Patterns          []string       `json:"patterns"`
	TestingFrameworks []string       `json:"testing_frameworks"`
	CICD              []string       `json:"ci_cd"`
}

// HubFile represents a highly-imported file.
type HubFile struct {
	Path        string `json:"path"`
	ImportCount int    `json:"import_count"`
}

// BrainIndex is the root model serialized to index.json.
type BrainIndex struct {
	Project  ProjectMeta  `json:"project"`
	Files    []FileInfo   `json:"files"`
	Symbols  []SymbolInfo `json:"symbols"`
	Imports  []ImportInfo `json:"imports"`
	Calls    []CallInfo   `json:"calls"`
	HubFiles []HubFile    `json:"hub_files"`
}
