package pdf

import (
	"fmt"
	"strconv"
)

// tokKind enumerates the low-level lexical tokens of PDF syntax.
type tokKind int

const (
	tokEOF tokKind = iota
	tokInt
	tokReal
	tokString  // literal (...) or hex <...>, already decoded
	tokName    // /Name (without slash)
	tokArrOpen // [
	tokArrClose
	tokDictOpen // <<
	tokDictClose
	tokKeyword // obj, endobj, stream, R, true, false, null, ...
)

type token struct {
	kind tokKind
	// value holds the decoded payload: string bytes for tokString, the name for
	// tokName, the keyword text for tokKeyword.
	str  []byte
	ival int64
	fval float64
}

// lexer scans PDF object syntax from a byte buffer.
type lexer struct {
	buf []byte
	pos int
}

func newLexer(buf []byte) *lexer { return &lexer{buf: buf} }

func isWhite(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

func isDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// skipWhite advances past whitespace and % comments.
func (l *lexer) skipWhite() {
	for l.pos < len(l.buf) {
		b := l.buf[l.pos]
		if isWhite(b) {
			l.pos++
			continue
		}
		if b == '%' {
			for l.pos < len(l.buf) && l.buf[l.pos] != '\n' && l.buf[l.pos] != '\r' {
				l.pos++
			}
			continue
		}
		break
	}
}

// next returns the next token.
func (l *lexer) next() (token, error) {
	l.skipWhite()
	if l.pos >= len(l.buf) {
		return token{kind: tokEOF}, nil
	}
	b := l.buf[l.pos]
	switch {
	case b == '[':
		l.pos++
		return token{kind: tokArrOpen}, nil
	case b == ']':
		l.pos++
		return token{kind: tokArrClose}, nil
	case b == '<':
		if l.pos+1 < len(l.buf) && l.buf[l.pos+1] == '<' {
			l.pos += 2
			return token{kind: tokDictOpen}, nil
		}
		return l.hexString()
	case b == '>':
		if l.pos+1 < len(l.buf) && l.buf[l.pos+1] == '>' {
			l.pos += 2
			return token{kind: tokDictClose}, nil
		}
		return token{}, fmt.Errorf("pdf: unexpected '>' at %d", l.pos)
	case b == '(':
		return l.literalString()
	case b == '/':
		return l.nameToken()
	case b == '{' || b == '}':
		// Rare (Type 4 function code); treat as standalone keyword.
		l.pos++
		return token{kind: tokKeyword, str: []byte{b}}, nil
	case b == '+' || b == '-' || b == '.' || (b >= '0' && b <= '9'):
		return l.number()
	default:
		return l.keyword()
	}
}

func (l *lexer) number() (token, error) {
	start := l.pos
	isReal := false
	if b := l.buf[l.pos]; b == '+' || b == '-' {
		l.pos++
	}
	for l.pos < len(l.buf) {
		b := l.buf[l.pos]
		if b >= '0' && b <= '9' {
			l.pos++
		} else if b == '.' {
			isReal = true
			l.pos++
		} else if b == '-' || b == '+' {
			// Some producers emit malformed numbers like "1.-2"; tolerate.
			l.pos++
		} else {
			break
		}
	}
	s := string(l.buf[start:l.pos])
	if isReal {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			// Tolerate junk like "." — treat as 0.
			return token{kind: tokReal, fval: 0}, nil
		}
		return token{kind: tokReal, fval: f}, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return token{kind: tokInt, ival: 0}, nil
		}
		return token{kind: tokReal, fval: f}, nil
	}
	return token{kind: tokInt, ival: n}, nil
}

func (l *lexer) keyword() (token, error) {
	start := l.pos
	for l.pos < len(l.buf) {
		b := l.buf[l.pos]
		if isWhite(b) || isDelim(b) {
			break
		}
		l.pos++
	}
	if l.pos == start {
		// Not whitespace/delim but produced no token: skip one byte to progress.
		l.pos++
		return token{kind: tokKeyword, str: l.buf[start:l.pos]}, nil
	}
	return token{kind: tokKeyword, str: l.buf[start:l.pos]}, nil
}

func (l *lexer) nameToken() (token, error) {
	l.pos++ // consume '/'
	var out []byte
	for l.pos < len(l.buf) {
		b := l.buf[l.pos]
		if isWhite(b) || isDelim(b) {
			break
		}
		if b == '#' && l.pos+2 < len(l.buf) {
			h := hexVal(l.buf[l.pos+1])<<4 | hexVal(l.buf[l.pos+2])
			out = append(out, byte(h))
			l.pos += 3
			continue
		}
		out = append(out, b)
		l.pos++
	}
	return token{kind: tokName, str: out}, nil
}

func (l *lexer) literalString() (token, error) {
	l.pos++ // consume '('
	var out []byte
	depth := 1
	for l.pos < len(l.buf) {
		b := l.buf[l.pos]
		switch b {
		case '\\':
			l.pos++
			if l.pos >= len(l.buf) {
				break
			}
			e := l.buf[l.pos]
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, e)
			case '\r':
				if l.pos+1 < len(l.buf) && l.buf[l.pos+1] == '\n' {
					l.pos++
				}
			case '\n':
				// line continuation: emit nothing
			default:
				if e >= '0' && e <= '7' {
					// up to 3 octal digits
					oct := int(e - '0')
					for k := 0; k < 2; k++ {
						if l.pos+1 < len(l.buf) && l.buf[l.pos+1] >= '0' && l.buf[l.pos+1] <= '7' {
							l.pos++
							oct = oct*8 + int(l.buf[l.pos]-'0')
						}
					}
					out = append(out, byte(oct))
				} else {
					out = append(out, e)
				}
			}
			l.pos++
		case '(':
			depth++
			out = append(out, b)
			l.pos++
		case ')':
			depth--
			l.pos++
			if depth == 0 {
				return token{kind: tokString, str: out}, nil
			}
			out = append(out, b)
		default:
			out = append(out, b)
			l.pos++
		}
	}
	return token{kind: tokString, str: out}, nil
}

func (l *lexer) hexString() (token, error) {
	l.pos++ // consume '<'
	var out []byte
	var hi int = -1
	for l.pos < len(l.buf) {
		b := l.buf[l.pos]
		if b == '>' {
			l.pos++
			break
		}
		if isWhite(b) {
			l.pos++
			continue
		}
		v := hexVal(b)
		if v < 0 {
			l.pos++
			continue
		}
		if hi < 0 {
			hi = v
		} else {
			out = append(out, byte(hi<<4|v))
			hi = -1
		}
		l.pos++
	}
	if hi >= 0 {
		out = append(out, byte(hi<<4)) // odd digit: low nibble assumed 0
	}
	return token{kind: tokString, str: out}, nil
}

func hexVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}
