package brain

import (
	"path/filepath"

	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

// ParseFile extracts symbols from a single file via LSP.
// Returns symbols and nil calls.
func ParseFile(filePath string, root string, language string) ([]SymbolInfo, []CallInfo) {
	pool := lsp.GetPool()
	if pool == nil {
		return nil, nil
	}

	client, err := pool.GetClient(language)
	if err != nil {
		LogError("LSP client error for %s (%s): %v", filePath, language, err)
		return nil, nil
	}
	if client == nil {
		return nil, nil // no LSP config for this language
	}

	lspSymbols, err := lsp.ExtractSymbols(client, filepath.Join(root, filePath), filePath, language)
	if err != nil {
		LogError("LSP documentSymbol failed for %s: %v", filePath, err)
		return nil, nil
	}

	var symbols []SymbolInfo
	for _, s := range lspSymbols {
		symbols = append(symbols, SymbolInfo{
			ID:         SymbolID(filePath, s.Name, s.Kind),
			Name:       s.Name,
			Kind:       s.Kind,
			FilePath:   filePath,
			LineStart:  s.StartLine,
			LineEnd:    s.EndLine,
			ReturnType: s.Detail,
			Parent:     s.Parent,
			Parameters: []string{},
			Decorators: []string{},
			Complexity: 1,
		})
	}

	if symbols == nil {
		symbols = []SymbolInfo{}
	}

	return symbols, nil
}
