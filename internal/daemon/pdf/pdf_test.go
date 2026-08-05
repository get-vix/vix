package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"testing"
)

// buildPDF assembles a minimal PDF with a classic xref table from object bodies
// (1-indexed) and the given /Root object number.
func buildPDF(objs []string, root int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xrefStart := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objs)+1)
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF", len(objs)+1, root, xrefStart)
	return b.Bytes()
}

func streamObj(dictExtra, content string) string {
	return fmt.Sprintf("<< /Length %d%s >>\nstream\n%s\nendstream", len(content), dictExtra, content)
}

func zlibCompress(s string) string {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.String()
}

// standardObjs returns catalog/pages/page/font objects around a content object
// (object 4), with the page referencing font F1.
func docWithContent(content string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		content,
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	return buildPDF(objs, 1)
}

func TestToMarkdown_HeadingAndParagraph(t *testing.T) {
	content := streamObj("", `BT
/F1 24 Tf
1 0 0 1 72 720 Tm
(Hello World) Tj
/F1 12 Tf
1 0 0 1 72 680 Tm
(This is the first paragraph line) Tj
1 0 0 1 72 666 Tm
(continuing on a second line.) Tj
ET`)
	res, err := ToMarkdown(docWithContent(content))
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if res.Scanned {
		t.Fatalf("expected text, got Scanned")
	}
	if !strings.Contains(res.Markdown, "# Hello World") {
		t.Errorf("missing heading; got:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "first paragraph line continuing on a second line.") {
		t.Errorf("paragraph not merged; got:\n%s", res.Markdown)
	}
}

func TestToMarkdown_FlateContent(t *testing.T) {
	raw := `BT
/F1 12 Tf
1 0 0 1 72 700 Tm
(Compressed hello) Tj
ET`
	comp := zlibCompress(raw)
	content := streamObj(" /Filter /FlateDecode", comp)
	res, err := ToMarkdown(docWithContent(content))
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if !strings.Contains(res.Markdown, "Compressed hello") {
		t.Errorf("flate content not decoded; got:\n%q", res.Markdown)
	}
}

func TestToMarkdown_Table(t *testing.T) {
	content := streamObj("", `BT
/F1 12 Tf
1 0 0 1 72 700 Tm
(Name) Tj
1 0 0 1 300 700 Tm
(Age) Tj
1 0 0 1 72 684 Tm
(Alice) Tj
1 0 0 1 300 684 Tm
(30) Tj
ET`)
	res, err := ToMarkdown(docWithContent(content))
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if !strings.Contains(res.Markdown, "| Name | Age |") {
		t.Errorf("table header missing; got:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "| --- | --- |") {
		t.Errorf("table separator missing; got:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "| Alice | 30 |") {
		t.Errorf("table row missing; got:\n%s", res.Markdown)
	}
}

func TestToMarkdown_Encrypted(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [] /Count 0 >>",
	}
	data := buildPDF(objs, 1)
	// Inject an /Encrypt entry into the trailer.
	data = bytes.Replace(data, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 3 0 R"), 1)
	if _, err := ToMarkdown(data); err != ErrEncrypted {
		t.Errorf("expected ErrEncrypted, got %v", err)
	}
}

func TestToMarkdown_NotPDF(t *testing.T) {
	if _, err := ToMarkdown([]byte("just some text, not a pdf")); err == nil {
		t.Error("expected error for non-PDF input")
	}
}

func TestToMarkdown_Scanned(t *testing.T) {
	// A page with no /Contents (e.g. image-only) yields no extractable text.
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
	}
	res, err := ToMarkdown(buildPDF(objs, 1))
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if !res.Scanned {
		t.Errorf("expected Scanned=true for contentless page; got markdown:\n%q", res.Markdown)
	}
}

func TestToMarkdown_ToUnicodeCMap(t *testing.T) {
	// Type0 font with a ToUnicode CMap mapping 2-byte codes to letters.
	toUni := `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
2 beginbfchar
<0001> <0048>
<0002> <0069>
endbfchar
endcmap
end end`
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		streamObj("", "BT /F1 12 Tf 1 0 0 1 72 700 Tm <00010002> Tj ET"),
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /ToUnicode 6 0 R >>",
		streamObj("", toUni),
	}
	res, err := ToMarkdown(buildPDF(objs, 1))
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if !strings.Contains(res.Markdown, "Hi") {
		t.Errorf("ToUnicode decoding failed; want 'Hi', got:\n%q", res.Markdown)
	}
}
