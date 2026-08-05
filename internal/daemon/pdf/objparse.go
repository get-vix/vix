package pdf

import (
	"bytes"
	"fmt"
)

// objParser assembles tokens from a lexer into PDF Objects. A non-nil doc lets
// it resolve an indirect /Length when reading a stream body.
type objParser struct {
	lex *lexer
	doc *Document
}

func newObjParser(buf []byte, doc *Document) *objParser {
	return &objParser{lex: newLexer(buf), doc: doc}
}

// parseObject reads a single (possibly compound) object.
func (p *objParser) parseObject() (Object, error) {
	tok, err := p.lex.next()
	if err != nil {
		return nil, err
	}
	return p.objectFromToken(tok)
}

func (p *objParser) objectFromToken(tok token) (Object, error) {
	switch tok.kind {
	case tokEOF:
		return nil, fmt.Errorf("pdf: unexpected EOF")
	case tokInt:
		return p.maybeReference(tok.ival)
	case tokReal:
		return Real(tok.fval), nil
	case tokString:
		return String(tok.str), nil
	case tokName:
		return Name(tok.str), nil
	case tokArrOpen:
		return p.parseArray()
	case tokDictOpen:
		return p.parseDictOrStream()
	case tokKeyword:
		switch string(tok.str) {
		case "true":
			return Boolean(true), nil
		case "false":
			return Boolean(false), nil
		case "null":
			return Null{}, nil
		}
		return nil, fmt.Errorf("pdf: unexpected keyword %q", tok.str)
	}
	return nil, fmt.Errorf("pdf: unexpected token")
}

// maybeReference handles the "n g R" and "n g obj" lookahead after an integer.
func (p *objParser) maybeReference(first int64) (Object, error) {
	save := p.lex.pos
	t2, err := p.lex.next()
	if err != nil || t2.kind != tokInt {
		p.lex.pos = save
		return Integer(first), nil
	}
	save2 := p.lex.pos
	t3, err := p.lex.next()
	if err != nil || t3.kind != tokKeyword {
		p.lex.pos = save
		return Integer(first), nil
	}
	switch string(t3.str) {
	case "R":
		return Reference{Num: int(first), Gen: int(t2.ival)}, nil
	case "obj":
		// Indirect object definition: value follows.
		return p.parseObject()
	default:
		_ = save2
		p.lex.pos = save
		return Integer(first), nil
	}
}

func (p *objParser) parseArray() (Object, error) {
	var arr Array
	for {
		tok, err := p.lex.next()
		if err != nil {
			return nil, err
		}
		if tok.kind == tokArrClose {
			return arr, nil
		}
		if tok.kind == tokEOF {
			return arr, nil
		}
		obj, err := p.objectFromToken(tok)
		if err != nil {
			return nil, err
		}
		arr = append(arr, obj)
	}
}

func (p *objParser) parseDictOrStream() (Object, error) {
	d := Dict{}
	for {
		tok, err := p.lex.next()
		if err != nil {
			return nil, err
		}
		if tok.kind == tokDictClose {
			break
		}
		if tok.kind == tokEOF {
			return d, nil
		}
		if tok.kind != tokName {
			// Malformed dict key; skip and continue defensively.
			continue
		}
		key := Name(tok.str)
		val, err := p.parseObject()
		if err != nil {
			return nil, err
		}
		d[key] = val
	}
	// Check for a following "stream".
	save := p.lex.pos
	tok, err := p.lex.next()
	if err == nil && tok.kind == tokKeyword && string(tok.str) == "stream" {
		return p.readStream(d)
	}
	p.lex.pos = save
	return d, nil
}

// readStream reads raw stream bytes following the "stream" keyword.
func (p *objParser) readStream(d Dict) (Object, error) {
	// After "stream" comes CRLF or LF (a lone CR is not allowed but tolerated).
	i := p.lex.pos
	buf := p.lex.buf
	if i < len(buf) && buf[i] == '\r' {
		i++
	}
	if i < len(buf) && buf[i] == '\n' {
		i++
	}
	start := i

	length := -1
	if p.doc != nil {
		if n, ok := Int(p.doc.Resolve(d["Length"])); ok {
			length = n
		}
	} else if n, ok := Int(d["Length"]); ok {
		length = n
	}

	var raw []byte
	if length >= 0 && start+length <= len(buf) {
		raw = buf[start : start+length]
		p.lex.pos = start + length
		// Verify "endstream" follows within a short window; if not, fall back
		// to scanning (the declared Length was wrong).
		if !endstreamNear(buf, p.lex.pos) {
			raw, p.lex.pos = scanStream(buf, start)
		}
	} else {
		raw, p.lex.pos = scanStream(buf, start)
	}
	// Advance past "endstream".
	if idx := bytes.Index(buf[p.lex.pos:], []byte("endstream")); idx >= 0 && idx < 3 {
		p.lex.pos += idx + len("endstream")
	} else if idx := bytes.Index(buf[p.lex.pos:], []byte("endstream")); idx >= 0 {
		p.lex.pos += idx + len("endstream")
	}
	return Stream{Dict: d, Raw: raw}, nil
}

// endstreamNear reports whether "endstream" appears within a few bytes of pos
// (allowing for a trailing EOL after the stream data).
func endstreamNear(buf []byte, pos int) bool {
	end := pos + 4
	if end > len(buf) {
		end = len(buf)
	}
	window := buf[pos:end]
	return bytes.Contains(window, []byte("en")) || bytes.HasPrefix(bytes.TrimLeft(window, "\r\n \t"), []byte("e"))
}

// scanStream locates the stream body by searching for the "endstream" keyword.
func scanStream(buf []byte, start int) (raw []byte, endPos int) {
	idx := bytes.Index(buf[start:], []byte("endstream"))
	if idx < 0 {
		return buf[start:], len(buf)
	}
	end := start + idx
	// Trim a single trailing EOL that separates data from "endstream".
	trimEnd := end
	if trimEnd > start && buf[trimEnd-1] == '\n' {
		trimEnd--
	}
	if trimEnd > start && buf[trimEnd-1] == '\r' {
		trimEnd--
	}
	return buf[start:trimEnd], end
}
