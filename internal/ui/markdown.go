package ui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/whiteboard"
)

// boldColor is the hex color applied to bold (Strong) text in markdown.
const boldColor = "#FFD080"

// styledConfig returns a copy of the base glamour StyleConfig with the Strong
// color overridden to boldColor.
func styledConfig(base string) ansi.StyleConfig {
	var cfg ansi.StyleConfig
	if base == styles.DarkStyle {
		cfg = styles.DarkStyleConfig
	} else {
		cfg = styles.LightStyleConfig
	}
	color := boldColor
	bold := true
	cfg.Strong = ansi.StylePrimitive{
		Color: &color,
		Bold:  &bold,
	}
	return cfg
}

var (
	codeBlockRe   = regexp.MustCompile("(?s)```(\\w*)\\n(.*?)```")
	ansiRe        = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	trailingPadRe = regexp.MustCompile(`(?:\x1b\[[0-9;]*m| )+$`)
	// emptyTableRe matches markdown tables with only a header row and separator (no data rows).
	// It matches: | header | ... |\n| --- | ... |\n followed by a blank line or EOF.
	emptyTableRe = regexp.MustCompile(`(?m)(\|[^\n]+\|\n\|[\s:\-|]+\|\n)(\n|\z)`)
)

// MarkdownRenderer wraps Glamour for rendering markdown to styled terminal output.
type MarkdownRenderer struct {
	renderer      *glamour.TermRenderer
	width         int
	hasDarkBG     bool
	codeBoxBorder lipgloss.Style

	// whiteboardBase and threadID enable the mermaid treatment: ```mermaid
	// code blocks are rendered as ASCII graphs with a "See it on the
	// whiteboard" link. Set per displayed thread via SetWhiteboardContext.
	// When base is empty (web UI disabled), the link is omitted.
	whiteboardBase string
	threadID       string
}

// SetWhiteboardContext records the web UI origin and thread id used to build
// whiteboard links for mermaid diagrams rendered by this renderer.
func (m *MarkdownRenderer) SetWhiteboardContext(base, threadID string) {
	m.whiteboardBase = base
	m.threadID = threadID
}

// NewMarkdownRenderer creates a new markdown renderer with the given width.
func NewMarkdownRenderer(width int, hasDarkBG bool, codeBoxBorder lipgloss.Style) *MarkdownRenderer {
	if width < 20 {
		width = 80
	}

	glamStyle := styles.DarkStyle
	if !hasDarkBG {
		glamStyle = styles.LightStyle
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(styledConfig(glamStyle)),
		glamour.WithWordWrap(width-4),
	)
	if err != nil {
		return &MarkdownRenderer{width: width, hasDarkBG: hasDarkBG, codeBoxBorder: codeBoxBorder}
	}
	return &MarkdownRenderer{renderer: r, width: width, hasDarkBG: hasDarkBG, codeBoxBorder: codeBoxBorder}
}

type codeBlock struct {
	lang string
	code string
}

// Render renders markdown text to styled terminal output.
func (m *MarkdownRenderer) Render(md string) string {
	if m.renderer == nil {
		return md + "\n"
	}

	// Strip empty markdown tables (header + separator, no data rows) that
	// glamour renders without a bottom border.
	md = emptyTableRe.ReplaceAllString(md, "$2")

	// Extract fenced code blocks and replace with placeholders.
	// Use a plain alphanumeric marker to avoid markdown interpretation
	// (e.g. __ would be treated as bold).
	var blocks []codeBlock
	replaced := codeBlockRe.ReplaceAllStringFunc(md, func(match string) string {
		sub := codeBlockRe.FindStringSubmatch(match)
		lang := sub[1]
		code := sub[2]
		code = strings.TrimRight(code, "\n")
		idx := len(blocks)
		blocks = append(blocks, codeBlock{lang: lang, code: code})
		return fmt.Sprintf("\n\nCBLK%dMARKER\n\n", idx)
	})

	out, err := m.renderer.Render(replaced)
	if err != nil {
		return md + "\n"
	}

	// Replace placeholders with rendered code boxes.
	// Glamour wraps text in ANSI codes, so we can't do a simple string replace.
	// Instead, find lines whose stripped text contains the marker and replace them.
	for i, block := range blocks {
		marker := fmt.Sprintf("CBLK%dMARKER", i)
		var box string
		if strings.EqualFold(block.lang, "mermaid") {
			box = m.renderMermaidBlock(block.code)
		} else {
			highlighted := m.glamourHighlight(block.lang, block.code)
			box = renderCodeBox(block.lang, highlighted, m.width, m.codeBoxBorder)
		}
		out = replaceMarkerLine(out, marker, box)
	}

	return out
}

// renderMermaidBlock renders a ```mermaid block as an ASCII/Unicode diagram and,
// when a whiteboard is available, appends a "See it on the whiteboard" link. If
// the diagram is wider than the terminal, it shows a compact pointer to the
// whiteboard instead of an overflowing dump. It falls back to a normal
// syntax-highlighted code box if the diagram can't be rendered.
func (m *MarkdownRenderer) renderMermaidBlock(code string) string {
	link := ""
	if m.whiteboardBase != "" && m.threadID != "" {
		if l, err := whiteboard.LinkFor(m.whiteboardBase, m.threadID, code); err == nil {
			link = l
		}
	}

	ascii, err := whiteboard.RenderASCII(code, m.width-4)
	if err != nil {
		box := renderCodeBox("mermaid", m.glamourHighlight("mermaid", code), m.width, m.codeBoxBorder)
		if link != "" {
			box += "\n\n" + m.renderWhiteboardLink(link)
		}
		return box
	}

	// When the diagram overflows the terminal and we can point at the
	// interactive whiteboard, prefer a compact note over a broken wide dump.
	if link != "" && asciiTooWide(ascii, m.width) {
		note := lipgloss.NewStyle().Faint(true).Render("  ▦ Mermaid diagram — too large to draw here.")
		return note + "\n" + m.renderWhiteboardLink(link)
	}

	// Indent the diagram by two columns to align with rendered code boxes.
	var b strings.Builder
	for _, line := range strings.Split(ascii, "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out := strings.TrimRight(b.String(), "\n")

	if link != "" {
		out += "\n\n" + m.renderWhiteboardLink(link)
	}
	return out
}

// asciiTooWide reports whether any line of the rendered diagram exceeds the
// available terminal width (leaving room for the 2-column indent).
func asciiTooWide(ascii string, width int) bool {
	limit := width - 2
	if limit < 10 {
		limit = 10
	}
	for _, line := range strings.Split(ascii, "\n") {
		if lipgloss.Width(line) > limit {
			return true
		}
	}
	return false
}

// renderWhiteboardLink renders a clickable (OSC 8) terminal hyperlink to the
// browser whiteboard.
func (m *MarkdownRenderer) renderWhiteboardLink(url string) string {
	label := "↗ See it on the whiteboard"
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#6ea8ff")).Underline(true).Render(label)
	return "  " + fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, styled)
}

// glamourHighlight renders a code block through glamour to get its native
// syntax highlighting, then strips glamour's padding/margins to return just
// the highlighted code lines.
func (m *MarkdownRenderer) glamourHighlight(lang, code string) []string {
	fence := "```"
	if lang != "" {
		fence += lang
	}
	md := fence + "\n" + code + "\n```\n"

	out, err := m.renderer.Render(md)
	if err != nil {
		return strings.Split(code, "\n")
	}

	// Glamour renders code blocks with 4-space indent (2 margin + 2 code indent)
	// and pads each line with ANSI-colored spaces. Extract code lines and strip
	// the leading indent and trailing padding.
	//
	// Strategy: skip blank lines that appear before the first code line (glamour
	// margin), but preserve blank lines that appear inside the code block.
	var lines []string
	started := false
	for _, line := range strings.Split(out, "\n") {
		stripped := ansiRe.ReplaceAllString(line, "")
		trimmed := strings.TrimRight(stripped, " ")
		if trimmed == "" {
			// Blank glamour line: skip leading margin; preserve interior blank lines.
			if started {
				lines = append(lines, "")
			}
			continue
		}
		// Code lines from glamour have 4-space indent
		if len(stripped) >= 4 && stripped[:4] == "    " {
			started = true
			lines = append(lines, stripGlamourLine(line))
		}
	}
	// Strip trailing blank lines added by glamour's bottom margin.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) == 0 {
		return strings.Split(code, "\n")
	}
	return lines
}

// stripGlamourLine removes glamour's leading 4-char indent and trailing
// ANSI-colored space padding from a highlighted code line.
func stripGlamourLine(line string) string {
	// Walk past leading ANSI codes and spaces, skipping exactly 4 visible spaces
	data := []byte(line)
	pos := 0
	spacesSkipped := 0

	for pos < len(data) && spacesSkipped < 4 {
		if pos+1 < len(data) && data[pos] == '\x1b' && data[pos+1] == '[' {
			j := pos + 2
			for j < len(data) && data[j] != 'm' {
				j++
			}
			if j < len(data) {
				j++
			}
			pos = j
		} else if data[pos] == ' ' {
			spacesSkipped++
			pos++
		} else {
			break
		}
	}

	content := string(data[pos:])

	// Strip trailing padding: glamour pads lines with ANSI-colored spaces.
	// Each padding space looks like \x1b[38;5;252m \x1b[0m.
	// Remove any trailing mix of ANSI codes and spaces.
	content = trailingPadRe.ReplaceAllString(content, "")

	// Expand tab characters to spaces so that lipgloss.Width correctly
	// measures the visual width when computing box padding. Tabs are
	// expanded to the next 4-space boundary (standard code display width).
	content = expandTabs(content, 4)

	return content
}

// expandTabs replaces tab characters in s with spaces, advancing to the next
// tabWidth-column boundary. ANSI escape sequences are skipped (zero-width).
func expandTabs(s string, tabWidth int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	i := 0
	data := []byte(s)
	for i < len(data) {
		// Skip ANSI escape sequences without advancing column.
		if i+1 < len(data) && data[i] == '\x1b' && data[i+1] == '[' {
			j := i + 2
			for j < len(data) && data[j] != 'm' {
				j++
			}
			if j < len(data) {
				j++ // consume 'm'
			}
			b.Write(data[i:j])
			i = j
			continue
		}
		if data[i] == '\t' {
			spaces := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
			i++
		} else {
			b.WriteByte(data[i])
			col++
			i++
		}
	}
	return b.String()
}

// replaceMarkerLine finds the line in text whose ANSI-stripped content contains
// marker, and replaces that entire line with replacement.
func replaceMarkerLine(text, marker, replacement string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		stripped := ansiRe.ReplaceAllString(line, "")
		if strings.Contains(stripped, marker) {
			lines[i] = replacement
			break
		}
	}
	return strings.Join(lines, "\n")
}

// UpdateWidth recreates the renderer with a new width.
func (m *MarkdownRenderer) UpdateWidth(width int) {
	if width < 20 {
		width = 80
	}
	m.width = width

	glamStyle := styles.DarkStyle
	if !m.hasDarkBG {
		glamStyle = styles.LightStyle
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(styledConfig(glamStyle)),
		glamour.WithWordWrap(width-4),
	)
	if err != nil {
		return
	}
	m.renderer = r
}

// renderCodeBox renders a code block inside a rounded box with optional language label.
// highlightedLines are pre-highlighted code lines (with ANSI codes).
func renderCodeBox(lang string, highlightedLines []string, width int, borderStyle lipgloss.Style) string {
	// Line number gutter: width of the largest line number + 1 space separator
	totalLines := len(highlightedLines)
	lineNumWidth := len(fmt.Sprintf("%d", totalLines))
	gutterWidth := lineNumWidth + 1 // digits + trailing space before code

	// Inner width: total width minus 2 indent, 2 border chars, 2 padding spaces, gutter
	innerWidth := width - 6 - gutterWidth
	if innerWidth < 10 {
		innerWidth = 10
	}

	// Build top border
	var topBorder string
	totalBorderWidth := innerWidth + 2 + gutterWidth // +2 for padding spaces inside box

	if lang != "" {
		label := " " + lang + " "
		labelWidth := lipgloss.Width(label)
		remainingDashes := totalBorderWidth - 1 - labelWidth
		if remainingDashes < 0 {
			remainingDashes = 0
		}
		topBorder = "  " + borderStyle.Render("╭─"+label+strings.Repeat("─", remainingDashes)+"╮")
	} else {
		topBorder = "  " + borderStyle.Render("╭"+strings.Repeat("─", totalBorderWidth)+"╮")
	}

	// Line number style: same dim color as the border
	lineNumStyle := borderStyle

	// Build content lines
	var contentLines []string
	for i, line := range highlightedLines {
		lineNum := fmt.Sprintf("%*d ", lineNumWidth, i+1)
		gutter := lineNumStyle.Render(lineNum)

		visualWidth := lipgloss.Width(line)
		padding := innerWidth - visualWidth
		if padding < 0 {
			padding = 0
		}
		padded := line + strings.Repeat(" ", padding)
		contentLines = append(contentLines,
			"  "+borderStyle.Render("│")+" "+gutter+padded+" "+borderStyle.Render("│"))
	}

	// Build bottom border
	bottomBorder := "  " + borderStyle.Render("╰"+strings.Repeat("─", totalBorderWidth)+"╯")

	result := topBorder + "\n"
	for _, line := range contentLines {
		result += line + "\n"
	}
	result += bottomBorder

	return result
}
