package daemon

import (
	"os"
	"testing"

	"github.com/get-vix/vix/internal/daemon/brain"
	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

func TestMain(m *testing.M) {
	// Mirror cmd/vixd/main.go: when invoked as `<binary> landlock-exec
	// ...`, act as the Landlock self-exec helper and don't run tests.
	// The Landlock test cases re-exec the test binary with this argv to
	// exercise the helper end-to-end.
	if len(os.Args) >= 2 && os.Args[1] == "landlock-exec" {
		LandlockExecMain(os.Args[2:])
		return
	}

	// Initialize the language map so treesitter and other tests can resolve extensions.
	brain.InitLanguageMapFromConfigs([]lsp.LanguageConfig{
		{Name: "go", Extensions: []string{".go"}},
		{Name: "python", Extensions: []string{".py"}},
		{Name: "javascript", Extensions: []string{".js", ".jsx"}},
		{Name: "typescript", Extensions: []string{".ts", ".tsx"}},
		{Name: "rust", Extensions: []string{".rs"}},
		{Name: "ruby", Extensions: []string{".rb"}},
		{Name: "java", Extensions: []string{".java"}},
		{Name: "kotlin", Extensions: []string{".kt", ".kts"}},
		{Name: "swift", Extensions: []string{".swift"}},
		{Name: "c", Extensions: []string{".c", ".h"}},
		{Name: "cpp", Extensions: []string{".cpp", ".hpp"}},
		{Name: "csharp", Extensions: []string{".cs"}},
		{Name: "php", Extensions: []string{".php"}},
		{Name: "shell", Extensions: []string{".sh", ".bash", ".zsh"}},
		{Name: "yaml", Extensions: []string{".yml", ".yaml"}},
		{Name: "json", Extensions: []string{".json"}},
		{Name: "toml", Extensions: []string{".toml"}},
		{Name: "html", Extensions: []string{".html"}},
		{Name: "css", Extensions: []string{".css"}},
		{Name: "scss", Extensions: []string{".scss"}},
		{Name: "sql", Extensions: []string{".sql"}},
		{Name: "markdown", Extensions: []string{".md"}},
		{Name: "lua", Extensions: []string{".lua"}},
	})
	os.Exit(m.Run())
}
