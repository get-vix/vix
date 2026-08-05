// Package pdf is a self-contained, dependency-free PDF reader that extracts a
// text-based PDF's content and renders it as Markdown for LLM consumption.
//
// Scope (v1): digital/text-based PDFs. It parses the classic cross-reference
// table as well as PDF 1.5+ cross-reference streams and compressed object
// streams, inflates FlateDecode content, interprets the text-showing operators
// of page content streams, maps character codes to Unicode via ToUnicode CMaps
// and the standard simple-font encodings, then reconstructs paragraphs,
// headings and best-effort tables.
//
// Out of scope: scanned/image-only PDFs (no text layer — OCR is not attempted)
// and encrypted PDFs.
package pdf

// Object is any PDF object. The concrete types below form a closed set; callers
// type-switch over them.
type Object interface{}

// Null is the PDF null object.
type Null struct{}

// Boolean is a PDF boolean (true/false).
type Boolean bool

// Integer is a PDF integer object.
type Integer int64

// Real is a PDF real (floating point) object.
type Real float64

// String is a PDF string object, already decoded from its literal "(...)" or
// hexadecimal "<...>" representation into raw bytes.
type String []byte

// Name is a PDF name object without the leading slash (e.g. "Type", "Font").
type Name string

// Array is a PDF array object.
type Array []Object

// Dict is a PDF dictionary object.
type Dict map[Name]Object

// Reference is an indirect object reference ("12 0 R").
type Reference struct {
	Num int
	Gen int
}

// Stream is a PDF stream object: its dictionary plus the raw (still-encoded)
// stream bytes. Decoding happens on demand via the filter pipeline.
type Stream struct {
	Dict Dict
	Raw  []byte
}

// Float returns the numeric value of an Integer or Real, and whether the object
// was numeric at all.
func Float(o Object) (float64, bool) {
	switch v := o.(type) {
	case Integer:
		return float64(v), true
	case Real:
		return float64(v), true
	}
	return 0, false
}

// Int returns the integer value of an Integer (or truncated Real), and whether
// the object was numeric.
func Int(o Object) (int, bool) {
	switch v := o.(type) {
	case Integer:
		return int(v), true
	case Real:
		return int(v), true
	}
	return 0, false
}

// name returns the Name value of o, or "" if o is not a Name.
func name(o Object) Name {
	if n, ok := o.(Name); ok {
		return n
	}
	return ""
}
