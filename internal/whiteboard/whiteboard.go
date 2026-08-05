// Package whiteboard turns Mermaid diagrams the model emits into two things:
//
//   - a terminal-rendered ASCII/Unicode graph (RenderASCII), shown in the TUI
//     in place of the raw ```mermaid source, and
//   - a link to the browser whiteboard (AppendWhiteboardLinks), where flowcharts
//     are converted to positioned canvas "scenes" and everything else falls back
//     to an in-browser mermaid.js render.
//
// It is imported by both the daemon (link injection) and the TUI (ASCII render),
// so it lives under internal/ with no dependency on either binary.
package whiteboard

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Node mirrors the canvas node schema consumed by the web whiteboard
// (SystemDesignCanvas.__setCanvas). Coordinates and dimensions are assigned by
// the layout pass; the web canvas sizes each box to Width/Height (falling back
// to per-shape defaults when absent, for backward compatibility).
type Node struct {
	ID            string `json:"id"`
	Shape         string `json:"shape"`
	X             int    `json:"x"`
	Y             int    `json:"y"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Label         string `json:"label"`
	Color         string `json:"color"`
	BorderColor   string `json:"border_color"`
	TextAlignment string `json:"text_alignment"`
	// ParentID is the enclosing group (subgraph) node's id, or "" for a
	// top-level node. When set, X/Y are relative to the parent, matching React
	// Flow's parent/child coordinate model. Omitted for parent-less nodes so
	// pre-subgraph links are byte-identical.
	ParentID string `json:"parent_id,omitempty"`
}

// Edge mirrors the canvas edge schema. Label is new (rendered at the edge
// midpoint by CustomEdge) and omitted when empty for compact URLs.
type Edge struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	FromHandle string `json:"from_handle"`
	To         string `json:"to"`
	ToHandle   string `json:"to_handle"`
	Label      string `json:"label,omitempty"`
}

// Scene is one canvas the whiteboard page can display. The chat feature emits a
// single scene; the plan workflow emits several.
type Scene struct {
	Name    string `json:"name"`
	Context string `json:"context"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
	Code    []any  `json:"code"`
}

// ToScene converts a single Mermaid flowchart into one positioned Scene. ok is
// false when the input is not a flowchart or cannot be parsed into at least one
// node — callers should then fall back to the raw-mermaid link.
func ToScene(mermaid, name string) (Scene, bool) {
	g, ok := parseFlowchart(mermaid)
	if !ok || len(g.order) == 0 {
		return Scene{}, false
	}
	nodes, edges := layout(g)
	if name == "" {
		name = "Diagram"
	}
	return Scene{Name: name, Context: "", Nodes: nodes, Edges: edges, Code: []any{}}, true
}

// WhiteboardBase returns the origin of the local web UI for the given port, or
// "" when the web UI is disabled (port <= 0), in which case no link is emitted.
func WhiteboardBase(webPort int) string {
	if webPort <= 0 {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d", webPort)
}

// compress deflates s and base64url-encodes it. base64url avoids '%', which
// some terminals re-encode when opening an OSC-8 hyperlink (turning %5B into
// %255B), and deflate keeps big scene graphs well under URL length limits.
func compress(s string) string {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

// scenesURL builds the per-thread whiteboard URL carrying positioned scenes as a
// compressed (deflate+base64url) payload under ?scenes_z=.
func scenesURL(base, threadID string, scenes []Scene) (string, error) {
	raw, err := json.Marshal(scenes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/thread/%s/whiteboard?scenes_z=%s", base, threadID, compress(string(raw))), nil
}

// mermaidURL builds the per-thread whiteboard URL that renders raw mermaid via
// mermaid.js in the browser (the fallback for non-flowchart diagram types),
// carrying the source as a compressed payload under ?mermaid_z=.
func mermaidURL(base, threadID, mermaid string) string {
	return fmt.Sprintf("%s/thread/%s/whiteboard?mermaid_z=%s", base, threadID, compress(mermaid))
}

// LinkFor returns the whiteboard URL for a single mermaid diagram: a positioned
// ?scenes= link when it parses as a flowchart, else a ?mermaid= fallback link.
func LinkFor(base, threadID, mermaid string) (string, error) {
	if scene, ok := ToScene(mermaid, ""); ok {
		return scenesURL(base, threadID, []Scene{scene})
	}
	return mermaidURL(base, threadID, mermaid), nil
}

// MermaidScene is one authored plan-whiteboard scene: a name, narration context
// (for the voice agent) and a Mermaid flowchart. It is what the plan workflow's
// generate step emits, before layout.
type MermaidScene struct {
	Name    string `json:"name"`
	Context string `json:"context"`
	Mermaid string `json:"mermaid"`
}

// ScenesFromMermaid converts authored mermaid scenes into positioned canvas
// scenes. Each flowchart is parsed and laid out; a scene whose mermaid can't be
// parsed as a flowchart becomes an empty canvas (its name/context are kept so it
// still appears in the scene list).
func ScenesFromMermaid(items []MermaidScene) []Scene {
	out := make([]Scene, 0, len(items))
	for _, it := range items {
		sc, ok := ToScene(it.Mermaid, it.Name)
		if !ok {
			sc = Scene{Name: it.Name, Nodes: []Node{}, Edges: []Edge{}, Code: []any{}}
		}
		sc.Context = it.Context
		out = append(out, sc)
	}
	return out
}

// CompressScenes returns the compressed (deflate+base64url) JSON of scenes,
// suitable as the value of a ?scenes_z= query parameter on the whiteboard page.
func CompressScenes(scenes []Scene) (string, error) {
	raw, err := json.Marshal(scenes)
	if err != nil {
		return "", err
	}
	return compress(string(raw)), nil
}

// CompressText returns the compressed (deflate+base64url) form of s, used for
// the plan text handed to the whiteboard walkthrough.
func CompressText(s string) string {
	return compress(s)
}

const linkLabel = "See it on the whiteboard"

// AppendWhiteboardLinks scans text for ```mermaid fenced blocks and inserts a
// "[See it on the whiteboard](url)" markdown link immediately after each one.
// It is idempotent: a block that is already followed by such a link is left
// untouched, so re-finalizing or replaying a message does not duplicate links.
// When base is "" (web UI disabled) the text is returned unchanged.
func AppendWhiteboardLinks(text, base, threadID string) string {
	if base == "" || !strings.Contains(text, "```mermaid") {
		return text
	}
	blocks := findMermaidBlocks(text)
	if len(blocks) == 0 {
		return text
	}
	var b strings.Builder
	prev := 0
	for _, blk := range blocks {
		b.WriteString(text[prev:blk.end])
		if !alreadyLinked(text[blk.end:], threadID) {
			link, err := LinkFor(base, threadID, blk.content)
			if err == nil {
				sep := "\n\n"
				if strings.HasSuffix(text[:blk.end], "\n") {
					sep = "\n" // fence line already ended with a newline
				}
				b.WriteString(fmt.Sprintf("%s[%s](%s)", sep, linkLabel, link))
			}
		}
		prev = blk.end
	}
	b.WriteString(text[prev:])
	return b.String()
}

type mermaidBlock struct {
	content string // inner mermaid source
	end     int    // index just past the closing fence
}

// findMermaidBlocks locates fenced ```mermaid blocks. It matches an opening
// fence line whose info string is "mermaid" and the next line that is a bare
// closing fence.
func findMermaidBlocks(text string) []mermaidBlock {
	var out []mermaidBlock
	lines := splitKeepNL(text)
	pos := 0 // byte offset at start of current line
	inBlock := false
	var content strings.Builder
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if !inBlock {
			if isMermaidFence(trimmed) {
				inBlock = true
				content.Reset()
			}
		} else {
			if trimmed == "```" || trimmed == "~~~" {
				out = append(out, mermaidBlock{content: content.String(), end: pos + len(ln)})
				inBlock = false
			} else {
				content.WriteString(ln)
			}
		}
		pos += len(ln)
	}
	return out
}

func isMermaidFence(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "~~~") {
		return false
	}
	info := strings.TrimSpace(strings.TrimLeft(trimmed, "`~"))
	return strings.EqualFold(info, "mermaid")
}

// alreadyLinked reports whether the text right after a closing fence already
// contains a whiteboard link for this thread (ignoring leading whitespace).
func alreadyLinked(after, threadID string) bool {
	marker := "/thread/" + threadID + "/whiteboard?"
	trimmed := strings.TrimLeft(after, " \t\r\n")
	// Only look at the immediate next non-blank content.
	nl := strings.IndexByte(trimmed, '\n')
	if nl >= 0 {
		trimmed = trimmed[:nl]
	}
	return strings.Contains(trimmed, marker)
}

// splitKeepNL splits text into lines while preserving the trailing newline on
// each line so byte offsets can be reconstructed exactly.
func splitKeepNL(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}
