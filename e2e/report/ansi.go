package report

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
)

// ansiToHTML converts a terminal capture containing ANSI SGR escape sequences
// into HTML, wrapping coloured/styled runs in <span> elements. Non-SGR control
// sequences (cursor moves, erases, …) are stripped. Text is HTML-escaped, so
// the result is safe to embed verbatim. When the input has no escapes it just
// returns the escaped text, so it is cheap on the common (plain) path.
func ansiToHTML(s string) template.HTML {
	if !strings.ContainsRune(s, 0x1b) {
		return template.HTML(template.HTMLEscapeString(s))
	}

	var b strings.Builder
	var cur ansiStyle
	spanOpen := false

	flushTo := func(next ansiStyle) {
		if spanOpen {
			b.WriteString("</span>")
			spanOpen = false
		}
		if css := next.css(); css != "" {
			b.WriteString(`<span style="`)
			b.WriteString(css)
			b.WriteString(`">`)
			spanOpen = true
		}
		cur = next
	}

	pending := cur
	i := 0
	for i < len(s) {
		c := s[i]
		if c != 0x1b {
			// Open the pending style lazily, only when there is text to emit.
			if pending != cur || (!spanOpen && pending.css() != "") {
				flushTo(pending)
			}
			b.WriteString(escapeByte(c))
			i++
			continue
		}
		// ESC: only CSI (ESC '[') sequences are handled; others skip ESC alone.
		if i+1 >= len(s) || s[i+1] != '[' {
			i++
			continue
		}
		j := i + 2
		for j < len(s) {
			ch := s[j]
			if ch >= 0x40 && ch <= 0x7e { // final byte
				break
			}
			j++
		}
		if j >= len(s) {
			break // truncated sequence
		}
		final := s[j]
		if final == 'm' {
			pending = pending.apply(s[i+2 : j])
		}
		i = j + 1
	}
	if spanOpen {
		b.WriteString("</span>")
	}
	return template.HTML(b.String())
}

func escapeByte(c byte) string {
	switch c {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	default:
		return string(c)
	}
}

// ansiStyle is the resolved SGR state at a point in the stream.
type ansiStyle struct {
	fg, bg             string // "" = default
	bold, dim, italic  bool
	underline, reverse bool
}

func (a ansiStyle) css() string {
	fg, bg := a.fg, a.bg
	if a.reverse {
		fg, bg = bg, fg
		if fg == "" {
			fg = "#0d1117"
		}
		if bg == "" {
			bg = "#c9d1d9"
		}
	}
	var parts []string
	if fg != "" {
		parts = append(parts, "color:"+fg)
	}
	if bg != "" {
		parts = append(parts, "background:"+bg)
	}
	if a.bold {
		parts = append(parts, "font-weight:600")
	}
	if a.dim {
		parts = append(parts, "opacity:.65")
	}
	if a.italic {
		parts = append(parts, "font-style:italic")
	}
	if a.underline {
		parts = append(parts, "text-decoration:underline")
	}
	return strings.Join(parts, ";")
}

// apply folds a single SGR parameter list ("1;34", "38;5;208", …) into a.
func (a ansiStyle) apply(params string) ansiStyle {
	if params == "" {
		return ansiStyle{} // bare ESC[m == reset
	}
	codes := strings.Split(params, ";")
	for i := 0; i < len(codes); i++ {
		n, err := strconv.Atoi(codes[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			a = ansiStyle{}
		case n == 1:
			a.bold = true
		case n == 2:
			a.dim = true
		case n == 3:
			a.italic = true
		case n == 4:
			a.underline = true
		case n == 7:
			a.reverse = true
		case n == 22:
			a.bold, a.dim = false, false
		case n == 23:
			a.italic = false
		case n == 24:
			a.underline = false
		case n == 27:
			a.reverse = false
		case n >= 30 && n <= 37:
			a.fg = ansi16[n-30]
		case n >= 90 && n <= 97:
			a.fg = ansi16[8+n-90]
		case n == 39:
			a.fg = ""
		case n >= 40 && n <= 47:
			a.bg = ansi16[n-40]
		case n >= 100 && n <= 107:
			a.bg = ansi16[8+n-100]
		case n == 49:
			a.bg = ""
		case n == 38 || n == 48:
			col, adv := extendedColor(codes[i:])
			if col != "" {
				if n == 38 {
					a.fg = col
				} else {
					a.bg = col
				}
			}
			i += adv
		}
	}
	return a
}

// extendedColor parses a 256-colour (5;n) or truecolor (2;r;g;b) sub-sequence
// starting at codes[0] == "38"/"48". It returns the hex colour and how many
// extra codes were consumed past the leading selector.
func extendedColor(codes []string) (string, int) {
	if len(codes) < 2 {
		return "", 0
	}
	switch codes[1] {
	case "5":
		if len(codes) < 3 {
			return "", 1
		}
		n, err := strconv.Atoi(codes[2])
		if err != nil {
			return "", 2
		}
		return xterm256(n), 2
	case "2":
		if len(codes) < 5 {
			return "", len(codes) - 1
		}
		r, _ := strconv.Atoi(codes[2])
		g, _ := strconv.Atoi(codes[3])
		bl, _ := strconv.Atoi(codes[4])
		return fmt.Sprintf("#%02x%02x%02x", r&0xff, g&0xff, bl&0xff), 4
	}
	return "", 1
}

// ansi16 is the VS Code dark terminal palette: legible on the report's dark bg.
var ansi16 = [16]string{
	"#1e1e1e", "#cd3131", "#0dbc79", "#e5e510",
	"#2472c8", "#bc3fbc", "#11a8cd", "#cccccc",
	"#666666", "#f14c4c", "#23d18b", "#f5f543",
	"#3b8eea", "#d670d6", "#29b8db", "#ffffff",
}

func xterm256(n int) string {
	switch {
	case n < 0 || n > 255:
		return ""
	case n < 16:
		return ansi16[n]
	case n < 232:
		n -= 16
		steps := []int{0, 95, 135, 175, 215, 255}
		r := steps[(n/36)%6]
		g := steps[(n/6)%6]
		b := steps[n%6]
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	default:
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
}
