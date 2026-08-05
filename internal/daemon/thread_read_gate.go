package daemon

import (
	"fmt"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// readGatedEditTools always require the target file to have been read first.
// write_file is intentionally NOT gated: it replaces the file wholesale, so the
// model doesn't need prior knowledge of on-disk contents.
var readGatedEditTools = map[string]bool{
	"edit_file":          true,
	"edit_minified_file": true,
}

// readTrackingTools mark their target path as read after a successful call.
// A successful edit/write leaves the model with accurate knowledge of the
// on-disk contents, so subsequent edits can proceed without re-reading.
var readTrackingTools = map[string]bool{
	"read_file":           true,
	"read_minified_file":  true,
	"edit_file":           true,
	"edit_minified_file":  true,
	"write_file":          true,
	"write_minified_file": true,
}

func (s *Thread) markFileRead(absPath string) {
	s.readFilesMu.Lock()
	defer s.readFilesMu.Unlock()
	if s.readFiles == nil {
		s.readFiles = make(map[string]bool)
	}
	s.readFiles[absPath] = true
}

func (s *Thread) hasBeenRead(absPath string) bool {
	s.readFilesMu.RLock()
	defer s.readFilesMu.RUnlock()
	return s.readFiles[absPath]
}

// enforceReadGate returns a non-nil ToolResult (with IsError=true) when an
// edit call targets a file the thread has never read. Returns nil when the
// call is allowed to proceed.
func (s *Thread) enforceReadGate(name string, params map[string]any) *ToolResult {
	if !readGatedEditTools[name] {
		return nil
	}
	pathStr, _ := params["path"].(string)
	if pathStr == "" {
		return nil
	}
	resolved, err := resolvePathInAllowed(s.cwd, s.toolAllowedDirs(), pathStr)
	if err != nil {
		return nil // let the handler surface the path error
	}
	if s.hasBeenRead(resolved) {
		return nil
	}
	return &ToolResult{
		Output: fmt.Sprintf(
			"error: %s was blocked because %q has not been read in this thread yet. Call read_file (or read_minified_file) on this path first so your change is based on the current on-disk contents.",
			name, pathStr,
		),
		IsError: true,
	}
}

// maybeMarkRead records a successful read/edit/write so that future edits
// on the same file see it as known-to-the-LLM.
func (s *Thread) maybeMarkRead(name string, params map[string]any, isError bool) {
	if isError {
		return
	}
	if !readTrackingTools[name] {
		return
	}
	pathStr, _ := params["path"].(string)
	if pathStr == "" {
		return
	}
	resolved, err := resolvePathInAllowed(s.cwd, s.toolAllowedDirs(), pathStr)
	if err != nil {
		return
	}
	s.markFileRead(resolved)
}

// rebuildReadFilesFromHistory reconstructs the read-gate set from a restored
// conversation history. Thread persistence carries s.messages but not the
// in-memory readFiles map, so a resumed thread would otherwise treat every
// previously-read file as unread and block edits on it. We replay the history
// applying the same rules as the live path (maybeMarkRead): a read/edit/write
// tool_use marks its target path as read only when its matching tool_result is
// non-error. A tool_use with no matching result (e.g. an interrupted final
// turn) is treated conservatively and left unmarked.
func (s *Thread) rebuildReadFilesFromHistory(msgs []llm.MessageParam) {
	// Collect tool_result outcomes by tool_use ID first, since a tool_use and
	// its result live in separate (adjacent) messages.
	resultErr := make(map[string]bool)
	haveResult := make(map[string]bool)
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.ToolUseID != "" {
				haveResult[b.ToolUseID] = true
				if b.IsError {
					resultErr[b.ToolUseID] = true
				}
			}
		}
	}

	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolUse || !readTrackingTools[b.Name] {
				continue
			}
			// Conservative: require a non-error result for this call.
			if !haveResult[b.ID] || resultErr[b.ID] {
				continue
			}
			pathStr, _ := b.Input["path"].(string)
			if pathStr == "" {
				continue
			}
			resolved, err := resolvePathInAllowed(s.cwd, s.toolAllowedDirs(), pathStr)
			if err != nil {
				continue
			}
			s.markFileRead(resolved)
		}
	}
}
