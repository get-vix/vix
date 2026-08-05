package canvas

import "strings"

// BoxChars holds characters for drawing boxes.
type BoxChars struct {
	TopLeft     rune
	TopRight    rune
	BottomLeft  rune
	BottomRight rune
	Horizontal  rune
	Vertical    rune
	TeeLeft     rune
	TeeRight    rune
	TeeUp       rune
	TeeDown     rune
	Cross       rune
}

// Select returns UnicodeBox or ASCIIBox based on the useAscii flag.
func Select(useAscii bool) BoxChars {
	if useAscii {
		return ASCIIBox
	}
	return UnicodeBox
}

// TopBorder returns a top border line: ┌────┐
func (b BoxChars) TopBorder(width int) string {
	return string(b.TopLeft) + strings.Repeat(string(b.Horizontal), width) + string(b.TopRight)
}

// BottomBorder returns a bottom border line: └────┘
func (b BoxChars) BottomBorder(width int) string {
	return string(b.BottomLeft) + strings.Repeat(string(b.Horizontal), width) + string(b.BottomRight)
}

// CenterText returns a line with centered text between vertical borders: │ text │
func (b BoxChars) CenterText(text string, width int) string {
	pad := width - len(text)
	left := pad / 2
	right := pad - left
	return string(b.Vertical) + strings.Repeat(" ", left) + text + strings.Repeat(" ", right) + string(b.Vertical)
}

// UnicodeBox provides Unicode box-drawing characters.
var UnicodeBox = BoxChars{
	TopLeft:     '┌',
	TopRight:    '┐',
	BottomLeft:  '└',
	BottomRight: '┘',
	Horizontal:  '─',
	Vertical:    '│',
	TeeLeft:     '┤',
	TeeRight:    '├',
	TeeUp:       '┴',
	TeeDown:     '┬',
	Cross:       '┼',
}

// ASCIIBox provides plain ASCII box-drawing characters.
var ASCIIBox = BoxChars{
	TopLeft:     '+',
	TopRight:    '+',
	BottomLeft:  '+',
	BottomRight: '+',
	Horizontal:  '-',
	Vertical:    '|',
	TeeLeft:     '+',
	TeeRight:    '+',
	TeeUp:       '+',
	TeeDown:     '+',
	Cross:       '+',
}
