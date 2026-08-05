package ui

import (
	"strings"
	"testing"
)

// stripANSI removes ANSI escape sequences (SGR and OSC 8 hyperlinks) so tests
// can assert on visible content and embedded URLs.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func TestRenderMermaidBlockFlowchart(t *testing.T) {
	md := NewMarkdownRenderer(80, true, NewStyles(true).CodeBoxBorderStyle)
	md.SetWhiteboardContext("http://localhost:1337", "thread-xyz")

	out := md.Render("Here:\n\n```mermaid\ngraph LR\nA-->B\n```\n")

	// The raw mermaid source must not be shown as-is (it is replaced by ASCII).
	if strings.Contains(stripANSI(out), "graph LR") {
		t.Errorf("expected mermaid source to be replaced by an ASCII diagram, got:\n%s", stripANSI(out))
	}
	// Node labels should appear in the ASCII rendering.
	if !strings.Contains(stripANSI(out), "A") || !strings.Contains(stripANSI(out), "B") {
		t.Errorf("expected node labels in ASCII output:\n%s", stripANSI(out))
	}
	// The whiteboard link (a ?scenes_z= URL for a flowchart) must be embedded.
	if !strings.Contains(out, "/thread/thread-xyz/whiteboard?scenes_z=") {
		t.Errorf("expected whiteboard scenes link, got:\n%s", out)
	}
	if !strings.Contains(stripANSI(out), "See it on the whiteboard") {
		t.Errorf("expected link label, got:\n%s", stripANSI(out))
	}
}

func TestRenderMermaidBlockNoLinkWhenDisabled(t *testing.T) {
	md := NewMarkdownRenderer(80, true, NewStyles(true).CodeBoxBorderStyle)
	// No whiteboard context set (web UI disabled): ASCII still renders, no link.
	out := md.Render("```mermaid\ngraph LR\nA-->B\n```\n")
	if strings.Contains(out, "whiteboard?") {
		t.Errorf("expected no link when whiteboard disabled, got:\n%s", out)
	}
	if !strings.Contains(stripANSI(out), "A") {
		t.Errorf("expected ASCII diagram even without a link:\n%s", stripANSI(out))
	}
}

func TestRenderMermaidNonFlowchartUsesMermaidLink(t *testing.T) {
	md := NewMarkdownRenderer(80, true, NewStyles(true).CodeBoxBorderStyle)
	md.SetWhiteboardContext("http://localhost:1337", "t9")
	out := md.Render("```mermaid\nsequenceDiagram\nAlice->>Bob: Hi\n```\n")
	if !strings.Contains(out, "/thread/t9/whiteboard?mermaid_z=") {
		t.Errorf("expected mermaid fallback link for sequence diagram, got:\n%s", out)
	}
}

func TestRenderNonMermaidCodeUnaffected(t *testing.T) {
	md := NewMarkdownRenderer(80, true, NewStyles(true).CodeBoxBorderStyle)
	md.SetWhiteboardContext("http://localhost:1337", "t1")
	out := md.Render("```go\nfmt.Println(\"hi\")\n```\n")
	if strings.Contains(out, "whiteboard?") {
		t.Errorf("non-mermaid code should not get a whiteboard link:\n%s", out)
	}
	if !strings.Contains(stripANSI(out), "Println") {
		t.Errorf("expected go code to render normally:\n%s", stripANSI(out))
	}
}

func TestAsciiTooWide(t *testing.T) {
	narrow := "┌───┐\n│ A │\n└───┘"
	if asciiTooWide(narrow, 80) {
		t.Errorf("small diagram should fit width 80")
	}
	wide := strings.Repeat("─", 200)
	if !asciiTooWide(wide, 80) {
		t.Errorf("a 200-column line should exceed width 80")
	}
}
