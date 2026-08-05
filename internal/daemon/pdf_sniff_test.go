package daemon

import (
	"strings"
	"testing"
)

// TestLooksLikePDF_NoFalsePositiveOnSource guards the tightened content sniffing:
// a source file (or any text) that merely contains the bytes "%PDF-" somewhere
// in its body must NOT be treated as a PDF. This previously misclassified the
// pdf package's own parser.go, which embeds "%PDF-" in a string literal.
func TestLooksLikePDF_NoFalsePositiveOnSource(t *testing.T) {
	goSource := "package pdf\n\n" +
		strings.Repeat("// a line of code\n", 40) +
		"\tif !bytes.HasPrefix(data, []byte(\"%PDF-\")) {\n" +
		"\t\treturn nil\n\t}\n"
	if looksLikePDF([]byte(goSource)) {
		t.Error("source file containing %PDF- mid-body was misclassified as a PDF")
	}
}

// TestLooksLikePDF_HeaderVariants covers real headers the sniffer must accept.
func TestLooksLikePDF_HeaderVariants(t *testing.T) {
	cases := map[string]struct {
		in   []byte
		want bool
	}{
		"plain header":       {[]byte("%PDF-1.7\n..."), true},
		"leading whitespace": {[]byte("\r\n  %PDF-1.4\n"), true},
		"utf8 bom":           {append([]byte{0xEF, 0xBB, 0xBF}, []byte("%PDF-1.5")...), true},
		"small junk prefix":  {append([]byte{0x00, 0x01, 0x02}, []byte("%PDF-1.6")...), true},
		"plain text":         {[]byte("not a pdf at all"), false},
		"header deep inside": {append([]byte(strings.Repeat("x", 200)), []byte("%PDF-1.7")...), false},
	}
	for name, c := range cases {
		if got := looksLikePDF(c.in); got != c.want {
			t.Errorf("%s: looksLikePDF = %v, want %v", name, got, c.want)
		}
	}
}
