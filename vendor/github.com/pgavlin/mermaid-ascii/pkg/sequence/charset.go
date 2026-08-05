package sequence

// BoxChars defines the characters used for drawing the diagram.
type BoxChars struct {
	TopLeft      rune
	TopRight     rune
	BottomLeft   rune
	BottomRight  rune
	Horizontal   rune
	Vertical     rune
	TeeDown      rune
	TeeRight     rune
	TeeLeft      rune
	Cross        rune
	ArrowRight   rune
	ArrowLeft    rune
	SolidLine    rune
	DottedLine   rune
	SelfTopRight rune
	SelfBottom   rune

	// Additional arrow type characters (2A)
	OpenArrowRight rune // > for open (non-filled) arrowhead right
	OpenArrowLeft  rune // < for open (non-filled) arrowhead left
	CrossEnd       rune // x for cross end

	// Activation box characters (2B)
	ActivationLeft  rune
	ActivationRight rune
}

// ASCII defines the box-drawing character set using plain ASCII characters.
var ASCII = BoxChars{
	TopLeft:      '+',
	TopRight:     '+',
	BottomLeft:   '+',
	BottomRight:  '+',
	Horizontal:   '-',
	Vertical:     '|',
	TeeDown:      '+',
	TeeRight:     '+',
	TeeLeft:      '+',
	Cross:        '+',
	ArrowRight:   '>',
	ArrowLeft:    '<',
	SolidLine:    '-',
	DottedLine:   '.',
	SelfTopRight: '+',
	SelfBottom:   '+',

	OpenArrowRight: '>',
	OpenArrowLeft:  '<',
	CrossEnd:       'x',

	ActivationLeft:  '|',
	ActivationRight: '|',
}

// Unicode defines the box-drawing character set using Unicode box-drawing characters.
var Unicode = BoxChars{
	TopLeft:      '┌',
	TopRight:     '┐',
	BottomLeft:   '└',
	BottomRight:  '┘',
	Horizontal:   '─',
	Vertical:     '│',
	TeeDown:      '┬',
	TeeRight:     '├',
	TeeLeft:      '┤',
	Cross:        '┼',
	ArrowRight:   '►',
	ArrowLeft:    '◄',
	SolidLine:    '─',
	DottedLine:   '┈',
	SelfTopRight: '┐',
	SelfBottom:   '┘',

	OpenArrowRight: '>',
	OpenArrowLeft:  '<',
	CrossEnd:       '×',

	ActivationLeft:  '│',
	ActivationRight: '│',
}
