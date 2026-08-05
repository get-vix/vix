package pdf

import "math"

// matrix is a PDF affine transform [a b c d e f] representing
//
//	| a b 0 |
//	| c d 0 |
//	| e f 1 |
type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

// mul returns m × n (apply m, then n).
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// textRun is a run of text with its device-space origin and effective size.
type textRun struct {
	x, y float64
	size float64
	text string
}

// textState holds the PDF text-object parameters.
type textState struct {
	font     *font
	fontSize float64
	charSp   float64
	wordSp   float64
	leading  float64
	hscale   float64 // Th, as a fraction (Tz/100)
	rise     float64
}

// contentExtractor interprets a page content stream into positioned text runs.
type contentExtractor struct {
	doc   *Document
	fonts map[Name]*font
	runs  []textRun

	ctm   matrix
	stack []matrix
	tm    matrix
	tlm   matrix
	ts    textState
}

func newContentExtractor(doc *Document, fonts map[Name]*font) *contentExtractor {
	return &contentExtractor{
		doc:   doc,
		fonts: fonts,
		ctm:   identity,
		ts:    textState{hscale: 1},
	}
}

// run interprets a decoded content stream.
func (ce *contentExtractor) run(content []byte) {
	p := newObjParser(content, ce.doc)
	var operands []Object
	for {
		tok, err := p.lex.next()
		if err != nil || tok.kind == tokEOF {
			break
		}
		switch tok.kind {
		case tokInt:
			operands = append(operands, Integer(tok.ival))
		case tokReal:
			operands = append(operands, Real(tok.fval))
		case tokString:
			operands = append(operands, String(tok.str))
		case tokName:
			operands = append(operands, Name(tok.str))
		case tokArrOpen:
			if arr, err := p.parseArray(); err == nil {
				operands = append(operands, arr)
			}
		case tokDictOpen:
			if d, err := p.parseDictOrStream(); err == nil {
				operands = append(operands, d)
			}
		case tokArrClose, tokDictClose:
			// Unbalanced; ignore.
		case tokKeyword:
			ce.op(string(tok.str), operands)
			operands = operands[:0]
		}
	}
}

func num(o Object) float64 {
	if f, ok := Float(o); ok {
		return f
	}
	return 0
}

func (ce *contentExtractor) op(name string, args []Object) {
	switch name {
	case "q":
		ce.stack = append(ce.stack, ce.ctm)
	case "Q":
		if n := len(ce.stack); n > 0 {
			ce.ctm = ce.stack[n-1]
			ce.stack = ce.stack[:n-1]
		}
	case "cm":
		if len(args) >= 6 {
			m := matrix{num(args[0]), num(args[1]), num(args[2]), num(args[3]), num(args[4]), num(args[5])}
			ce.ctm = m.mul(ce.ctm)
		}
	case "BT":
		ce.tm = identity
		ce.tlm = identity
	case "ET":
	case "Tf":
		if len(args) >= 2 {
			ce.ts.font = ce.fonts[name2(args[0])]
			ce.ts.fontSize = num(args[1])
		}
	case "Td":
		if len(args) >= 2 {
			ce.td(num(args[0]), num(args[1]))
		}
	case "TD":
		if len(args) >= 2 {
			ce.ts.leading = -num(args[1])
			ce.td(num(args[0]), num(args[1]))
		}
	case "Tm":
		if len(args) >= 6 {
			ce.tlm = matrix{num(args[0]), num(args[1]), num(args[2]), num(args[3]), num(args[4]), num(args[5])}
			ce.tm = ce.tlm
		}
	case "T*":
		ce.td(0, -ce.ts.leading)
	case "Tc":
		if len(args) >= 1 {
			ce.ts.charSp = num(args[0])
		}
	case "Tw":
		if len(args) >= 1 {
			ce.ts.wordSp = num(args[0])
		}
	case "Tz":
		if len(args) >= 1 {
			ce.ts.hscale = num(args[0]) / 100
		}
	case "TL":
		if len(args) >= 1 {
			ce.ts.leading = num(args[0])
		}
	case "Ts":
		if len(args) >= 1 {
			ce.ts.rise = num(args[0])
		}
	case "Tj":
		if len(args) >= 1 {
			if s, ok := args[0].(String); ok {
				ce.show(s)
			}
		}
	case "'":
		ce.td(0, -ce.ts.leading)
		if len(args) >= 1 {
			if s, ok := args[0].(String); ok {
				ce.show(s)
			}
		}
	case "\"":
		if len(args) >= 3 {
			ce.ts.wordSp = num(args[0])
			ce.ts.charSp = num(args[1])
			ce.td(0, -ce.ts.leading)
			if s, ok := args[2].(String); ok {
				ce.show(s)
			}
		}
	case "TJ":
		if len(args) >= 1 {
			if arr, ok := args[0].(Array); ok {
				ce.showArray(arr)
			}
		}
	}
}

func name2(o Object) Name {
	if n, ok := o.(Name); ok {
		return n
	}
	return ""
}

func (ce *contentExtractor) td(tx, ty float64) {
	t := matrix{1, 0, 0, 1, tx, ty}
	ce.tlm = t.mul(ce.tlm)
	ce.tm = ce.tlm
}

// show renders a string operand at the current text position and advances Tm.
func (ce *contentExtractor) show(s String) {
	if ce.ts.font == nil {
		return
	}
	trm := ce.tm.mul(ce.ctm)
	// Effective font size = fontSize scaled by the vertical scale of Tm×CTM.
	scaleY := math.Hypot(trm[2], trm[3])
	size := ce.ts.fontSize * scaleY
	text := ce.ts.font.decode([]byte(s))
	if text != "" {
		ce.runs = append(ce.runs, textRun{x: trm[4], y: trm[5], size: size, text: text})
	}
	ce.advance([]byte(s))
}

func (ce *contentExtractor) showArray(arr Array) {
	for _, el := range arr {
		switch v := el.(type) {
		case String:
			ce.show(v)
		case Integer:
			ce.adjust(float64(v))
		case Real:
			ce.adjust(float64(v))
		}
	}
}

// adjust applies a TJ numeric position adjustment (thousandths of an em).
func (ce *contentExtractor) adjust(a float64) {
	tx := -a / 1000 * ce.ts.fontSize * ce.ts.hscale
	ce.tm = matrix{1, 0, 0, 1, tx, 0}.mul(ce.tm)
}

// advance moves Tm horizontally by an approximate width for the shown bytes.
// Without per-glyph widths we estimate 0.5 em per character plus spacing, which
// is sufficient for line/column grouping in layout reconstruction.
func (ce *contentExtractor) advance(raw []byte) {
	step := 1
	if ce.ts.font != nil && ce.ts.font.twoByte {
		step = 2
	}
	n := len(raw) / step
	w := 0.0
	for i := 0; i+step <= len(raw); i += step {
		w += 0.5*ce.ts.fontSize + ce.ts.charSp
		if step == 1 && raw[i] == ' ' {
			w += ce.ts.wordSp
		}
	}
	_ = n
	tx := w * ce.ts.hscale
	ce.tm = matrix{1, 0, 0, 1, tx, 0}.mul(ce.tm)
}
