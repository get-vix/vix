package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/get-vix/vix/internal/config"
)

// WriteChatAgentModel updates (or inserts) the `model:` line in the YAML
// frontmatter of the chat agent's .md file, so the choice persists across
// sessions. The file written is whichever layer currently sources the
// agent (highest-precedence existing copy — project before home).
//
// Returns an error when the file doesn't exist at any layer; the caller
// should log it and continue rather than failing the model switch (the
// in-memory swap already happened).
func WriteChatAgentModel(paths config.VixPaths, agentName, modelSpec string) error {
	if agentName == "" {
		return fmt.Errorf("empty agent name")
	}
	if modelSpec == "" {
		return fmt.Errorf("empty model spec")
	}

	filename := agentName + ".md"
	path := findExistingAgentPath(paths, filename)
	if path == "" {
		return fmt.Errorf("agent file %q not found in any .vix/agents/ layer", filename)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	updated, err := updateFrontmatterModel(string(raw), modelSpec)
	if err != nil {
		return fmt.Errorf("update frontmatter in %s: %w", path, err)
	}

	if updated == string(raw) {
		return nil // already at target value, nothing to write
	}

	// Preserve the file mode if possible.
	info, statErr := os.Stat(path)
	mode := os.FileMode(0o644)
	if statErr == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, []byte(updated), mode)
}

// findExistingAgentPath returns the highest-precedence on-disk path for
// the given agent filename, or "" when nothing exists. Mirrors
// Session.resolveAgentPath's iteration order so the file we WRITE is the
// same file we READ.
func findExistingAgentPath(paths config.VixPaths, filename string) string {
	dirs := paths.Agents()
	for i := len(dirs) - 1; i >= 0; i-- {
		candidate := filepath.Join(dirs[i], filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// updateFrontmatterModel returns content with the `model:` line in the
// leading YAML frontmatter set to modelSpec. Inserts the line after the
// `name:` line (or at the top of the frontmatter when no name: exists)
// when no model: line is present today. Everything else is preserved
// byte-for-byte.
//
// Returns an error when the file has no leading `---` frontmatter delimiter
// (we refuse to invent one rather than silently rewriting the body).
func updateFrontmatterModel(content, modelSpec string) (string, error) {
	const delim = "---"
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != delim {
		return "", fmt.Errorf("no leading '---' frontmatter delimiter")
	}

	// Locate the closing delimiter. Walk from line 1 to find the next "---".
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == delim {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return "", fmt.Errorf("no closing '---' frontmatter delimiter")
	}

	// Find a `model:` line within (0, closeIdx).
	modelIdx := -1
	nameIdx := -1
	for i := 1; i < closeIdx; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "model:") {
			modelIdx = i
		}
		if nameIdx == -1 && strings.HasPrefix(trimmed, "name:") {
			nameIdx = i
		}
	}

	newLine := "model: " + modelSpec

	if modelIdx != -1 {
		lines[modelIdx] = newLine
	} else {
		// Insert after name: if present, otherwise as the first line
		// inside the frontmatter.
		insertAt := 1
		if nameIdx != -1 {
			insertAt = nameIdx + 1
		}
		lines = append(lines[:insertAt], append([]string{newLine}, lines[insertAt:]...)...)
	}

	return strings.Join(lines, "\n"), nil
}
