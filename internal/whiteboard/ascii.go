package whiteboard

import (
	"fmt"
	"io"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
	"github.com/pgavlin/mermaid-ascii/pkg/render"
	log "github.com/sirupsen/logrus"
)

func init() {
	// The mermaid-ascii graph renderer logs through logrus; silence it so it
	// never writes to stderr and corrupts the TUI. vix itself uses the stdlib
	// logger, so discarding logrus output here is safe.
	log.SetOutput(io.Discard)
	log.SetLevel(log.PanicLevel)
}

// asciiReplacer maps common non-ASCII punctuation the model tends to put in
// diagram labels to cleaner ASCII equivalents (e.g. "→" -> "->"). This is a
// readability nicety, not a correctness requirement: the renderer handles
// arbitrary Unicode, but "->" reads better than a lone arrow glyph in a
// terminal box.
var asciiReplacer = strings.NewReplacer(
	"→", "->", "⟶", "->", "➔", "->", "➜", "->", "➝", "->", "⇒", "=>", "⟹", "=>",
	"←", "<-", "⟵", "<-", "↔", "<->", "⇔", "<=>",
	"…", "...", "•", "*", "·", "*", "×", "x", "÷", "/",
	"–", "-", "—", "-", "‑", "-", "−", "-",
	"“", "\"", "”", "\"", "‘", "'", "’", "'",
	"≥", ">=", "≤", "<=", "≠", "!=", "≈", "~",
	"✓", "v", "✔", "v", "✗", "x", "✕", "x",
)

// sanitizeForASCII normalizes common non-ASCII punctuation to ASCII equivalents
// for readability (e.g. "→" -> "->", "…" -> "..."), but otherwise preserves
// Unicode. The mermaid-ascii renderer is now grapheme-cluster aware, so emoji,
// accented letters, and CJK pass through and render as their own display cells
// rather than being mangled into mojibake.
func sanitizeForASCII(s string) string {
	return asciiReplacer.Replace(s)
}

// RenderASCII renders a Mermaid diagram to Unicode box-drawing text sized to
// fit width columns (0 = unconstrained). It returns an error for unsupported or
// malformed diagrams so callers can fall back to showing the raw source.
//
// The underlying mermaid-ascii renderer panics on some diagram geometries (e.g.
// an index-out-of-range while drawing edges), so we recover here and surface the
// panic as an error. A single bad diagram must never take down the TUI.
func RenderASCII(mermaid string, width int) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = ""
			err = fmt.Errorf("mermaid render panicked: %v", r)
		}
	}()

	cfg := diagram.DefaultConfig()
	cfg.StyleType = "cli" // plain text, no ANSI colour
	if width > 0 {
		cfg.TargetWidth = width
	}
	rendered, rerr := render.Render(sanitizeForASCII(mermaid), cfg)
	if rerr != nil {
		return "", rerr
	}
	return strings.TrimRight(rendered, "\n"), nil
}
