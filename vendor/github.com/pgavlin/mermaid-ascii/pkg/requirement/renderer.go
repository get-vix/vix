package requirement

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/canvas"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	boxPadding = 4 // 2 padding on each side
)

type reqChars struct {
	canvas.BoxChars
	Arrow string
}

var asciiChars = reqChars{BoxChars: canvas.ASCIIBox, Arrow: "-->"}
var unicodeChars = reqChars{BoxChars: canvas.UnicodeBox, Arrow: "──>"}

// Render renders a requirement diagram as ASCII/Unicode text.
func Render(d *RequirementDiagram, config *diagram.Config) (string, error) {
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

	// Render requirements
	for _, req := range d.Requirements {
		lines = append(lines, renderRequirement(req, chars)...)
		lines = append(lines, "")
	}

	// Render elements
	for _, elem := range d.Elements {
		lines = append(lines, renderReqElement(elem, chars)...)
		lines = append(lines, "")
	}

	// Render relationships
	for _, rel := range d.Relationships {
		lines = append(lines, fmt.Sprintf("%s %s %s %s", rel.Source, chars.Arrow, rel.Target, fmt.Sprintf("[%s]", rel.Type)))
	}

	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return strings.Join(lines, "\n") + "\n", nil
}

func renderRequirement(req *Requirement, chars reqChars) []string {
	contentLines := []string{
		fmt.Sprintf("<<%s>>", req.Type),
		req.Name,
	}
	if req.ID != "" {
		contentLines = append(contentLines, fmt.Sprintf("Id: %s", req.ID))
	}
	if req.Text != "" {
		contentLines = append(contentLines, fmt.Sprintf("Text: %s", req.Text))
	}
	if req.Risk != "" {
		contentLines = append(contentLines, fmt.Sprintf("Risk: %s", req.Risk))
	}
	if req.VerifyMethod != "" {
		contentLines = append(contentLines, fmt.Sprintf("Verify: %s", req.VerifyMethod))
	}

	return renderBox(contentLines, chars)
}

func renderReqElement(elem *ReqElement, chars reqChars) []string {
	contentLines := []string{
		"<<element>>",
		elem.Name,
	}
	if elem.Type != "" {
		contentLines = append(contentLines, fmt.Sprintf("Type: %s", elem.Type))
	}
	if elem.DocRef != "" {
		contentLines = append(contentLines, fmt.Sprintf("DocRef: %s", elem.DocRef))
	}

	return renderBox(contentLines, chars)
}

func renderBox(contentLines []string, chars reqChars) []string {
	maxWidth := 0
	for _, l := range contentLines {
		if len(l) > maxWidth {
			maxWidth = len(l)
		}
	}
	boxWidth := maxWidth + boxPadding

	var result []string
	result = append(result, chars.TopBorder(boxWidth))
	for _, l := range contentLines {
		result = append(result, chars.CenterText(l, boxWidth))
	}
	result = append(result, chars.BottomBorder(boxWidth))
	return result
}
