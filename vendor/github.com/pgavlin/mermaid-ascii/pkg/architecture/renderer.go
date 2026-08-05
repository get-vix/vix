package architecture

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/canvas"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	boxPadding = 4 // 2 padding on each side
)

type archChars struct {
	canvas.BoxChars
	Arrow string
	Line  string
}

var asciiChars = archChars{BoxChars: canvas.ASCIIBox, Arrow: "-->", Line: "---"}
var unicodeChars = archChars{BoxChars: canvas.UnicodeBox, Arrow: "──>", Line: "───"}

// Render renders an architecture diagram as ASCII/Unicode text.
func Render(d *ArchitectureDiagram, config *diagram.Config) (string, error) {
	if d == nil {
		return "", fmt.Errorf("nil diagram")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	chars := unicodeChars
	if config.UseAscii {
		chars = asciiChars
	}

	var lines []string

	// Render top-level services
	for _, svc := range d.Services {
		lines = append(lines, renderService(svc, chars, 0)...)
		lines = append(lines, "")
	}

	// Render groups
	for _, g := range d.Groups {
		lines = append(lines, renderGroup(g, chars, 0)...)
		lines = append(lines, "")
	}

	// Render connections
	for _, conn := range d.Connections {
		lines = append(lines, renderConnection(conn, chars))
	}

	// Remove trailing empty line if present
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return strings.Join(lines, "\n") + "\n", nil
}

func renderService(svc *Service, chars archChars, indent int) []string {
	prefix := strings.Repeat("  ", indent)
	label := svc.Label
	boxWidth := len(label) + boxPadding

	var result []string
	result = append(result, prefix+chars.TopBorder(boxWidth))
	result = append(result, prefix+chars.CenterText(label, boxWidth))
	result = append(result, prefix+chars.BottomBorder(boxWidth))
	return result
}

func renderGroup(g *Group, chars archChars, indent int) []string {
	prefix := strings.Repeat("  ", indent)

	var inner []string
	for _, svc := range g.Services {
		inner = append(inner, renderService(svc, chars, indent+1)...)
	}
	for _, sub := range g.Groups {
		inner = append(inner, renderGroup(sub, chars, indent+1)...)
	}

	maxWidth := len(g.Label) + boxPadding
	for _, l := range inner {
		stripped := strings.TrimPrefix(l, prefix)
		if len(stripped)+2 > maxWidth {
			maxWidth = len(stripped) + 2
		}
	}

	var result []string
	result = append(result, prefix+chars.TopBorder(maxWidth))
	result = append(result, prefix+chars.CenterText(g.Label, maxWidth))
	result = append(result, prefix+string(chars.Vertical)+strings.Repeat(string(chars.Horizontal), maxWidth)+string(chars.Vertical))

	for _, l := range inner {
		stripped := strings.TrimPrefix(l, prefix)
		padNeeded := maxWidth - len(stripped)
		if padNeeded < 0 {
			padNeeded = 0
		}
		result = append(result, prefix+string(chars.Vertical)+stripped+strings.Repeat(" ", padNeeded)+string(chars.Vertical))
	}

	result = append(result, prefix+chars.BottomBorder(maxWidth))
	return result
}

func renderConnection(conn *Connection, chars archChars) string {
	arrow := chars.Line
	if conn.Directed {
		arrow = chars.Arrow
	}
	return fmt.Sprintf("%s %s %s", conn.From, arrow, conn.To)
}
