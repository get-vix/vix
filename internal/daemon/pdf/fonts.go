package pdf

import (
	"unicode/utf16"
)

// font decodes character codes from a page's text-showing operators into
// Unicode text. It combines an optional ToUnicode CMap (authoritative) with the
// font's simple-encoding table (fallback).
type font struct {
	twoByte   bool              // Type0/CID fonts address glyphs with 2-byte codes
	toUni     map[uint32]string // code -> Unicode (from /ToUnicode)
	simple    [256]rune         // simple-font byte -> rune
	hasSimple bool
}

// buildFont constructs a font from a font dictionary.
func (d *Document) buildFont(dict Dict) *font {
	f := &font{}
	sub := name(d.Resolve(dict["Subtype"]))
	if sub == "Type0" {
		f.twoByte = true
		// Descendant CIDFonts with Identity encoding use 2-byte codes; the
		// ToUnicode CMap (if present) provides the text.
	} else {
		f.hasSimple = true
		f.simple = winAnsi // sensible default
		d.applyEncoding(f, dict["Encoding"])
	}
	if tu := d.Resolve(dict["ToUnicode"]); tu != nil {
		if st, ok := tu.(Stream); ok {
			if data, err := d.decodeStream(st); err == nil {
				f.toUni = parseToUnicode(data)
			}
		}
	}
	return f
}

// applyEncoding sets the simple-font encoding table from a name or a dictionary
// with /BaseEncoding and /Differences.
func (d *Document) applyEncoding(f *font, enc Object) {
	enc = d.Resolve(enc)
	switch v := enc.(type) {
	case Name:
		f.simple = baseEncoding(v)
	case Dict:
		if be := name(d.Resolve(v["BaseEncoding"])); be != "" {
			f.simple = baseEncoding(be)
		}
		if diffs, ok := d.Resolve(v["Differences"]).(Array); ok {
			code := 0
			for _, item := range diffs {
				item = d.Resolve(item)
				if n, ok := Int(item); ok {
					code = n
					continue
				}
				if gn, ok := item.(Name); ok && code >= 0 && code < 256 {
					if r := glyphToRune(string(gn)); r != 0 {
						f.simple[code] = r
					}
					code++
				}
			}
		}
	}
}

func baseEncoding(n Name) [256]rune {
	// WinAnsi covers printable ASCII identically to Standard/MacRoman, so it is
	// a safe default for all named base encodings in v1.
	return winAnsi
}

// decode converts raw string-operand bytes to text.
func (f *font) decode(raw []byte) string {
	var sb []rune
	step := 1
	if f.twoByte {
		step = 2
	}
	for i := 0; i+step <= len(raw); i += step {
		var code uint32
		if step == 2 {
			code = uint32(raw[i])<<8 | uint32(raw[i+1])
		} else {
			code = uint32(raw[i])
		}
		if f.toUni != nil {
			if s, ok := f.toUni[code]; ok {
				sb = append(sb, []rune(s)...)
				continue
			}
		}
		if f.hasSimple && code < 256 {
			if r := f.simple[byte(code)]; r != 0 {
				sb = append(sb, r)
				continue
			}
		}
		if !f.twoByte && code >= 0x20 && code < 0x7F {
			sb = append(sb, rune(code))
		}
	}
	return string(sb)
}

// parseToUnicode parses a ToUnicode CMap stream into a code->text map.
func parseToUnicode(data []byte) map[uint32]string {
	out := map[uint32]string{}
	lex := newLexer(data)
	var stack []token
	for {
		tok, err := lex.next()
		if err != nil || tok.kind == tokEOF {
			break
		}
		if tok.kind == tokKeyword {
			switch string(tok.str) {
			case "beginbfchar":
				parseBFChar(lex, out)
				stack = stack[:0]
			case "beginbfrange":
				parseBFRange(lex, out)
				stack = stack[:0]
			default:
				stack = stack[:0]
			}
			continue
		}
		stack = append(stack, tok)
		if len(stack) > 8 {
			stack = stack[1:]
		}
	}
	return out
}

func parseBFChar(lex *lexer, out map[uint32]string) {
	for {
		src, err := lex.next()
		if err != nil {
			return
		}
		if src.kind == tokKeyword && string(src.str) == "endbfchar" {
			return
		}
		if src.kind != tokString {
			return
		}
		dst, err := lex.next()
		if err != nil || dst.kind != tokString {
			return
		}
		out[beCode(src.str)] = utf16BE(dst.str)
	}
}

func parseBFRange(lex *lexer, out map[uint32]string) {
	for {
		lo, err := lex.next()
		if err != nil {
			return
		}
		if lo.kind == tokKeyword && string(lo.str) == "endbfrange" {
			return
		}
		if lo.kind != tokString {
			return
		}
		hi, err := lex.next()
		if err != nil || hi.kind != tokString {
			return
		}
		dst, err := lex.next()
		if err != nil {
			return
		}
		loC, hiC := beCode(lo.str), beCode(hi.str)
		switch dst.kind {
		case tokString:
			base := []rune(utf16BE(dst.str))
			for c := loC; c <= hiC && c-loC < 65536; c++ {
				if len(base) == 0 {
					break
				}
				r := make([]rune, len(base))
				copy(r, base)
				r[len(r)-1] += rune(c - loC)
				out[c] = string(r)
			}
		case tokArrOpen:
			c := loC
			for {
				el, err := lex.next()
				if err != nil || el.kind == tokArrClose {
					break
				}
				if el.kind == tokString {
					out[c] = utf16BE(el.str)
					c++
				}
			}
		}
	}
}

// beCode interprets up to 4 bytes as a big-endian integer code.
func beCode(b []byte) uint32 {
	var v uint32
	for _, c := range b {
		v = v<<8 | uint32(c)
	}
	return v
}

// utf16BE decodes big-endian UTF-16 bytes to a Go string.
func utf16BE(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := 0; i < len(u); i++ {
		u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	return string(utf16.Decode(u))
}
