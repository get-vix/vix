package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalPDF builds a tiny valid PDF whose single page shows the given text.
func minimalPDF(text string) []byte {
	content := fmt.Sprintf("BT /F1 12 Tf 1 0 0 1 72 700 Tm (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for i, o := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	b.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\n%%EOF")
	return b.Bytes()
}

func TestReadFileImpl_PDFReturnsMarkdown(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, minimalPDF("Hello from PDF"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := readFileImpl(dir, []string{dir}, pdfPath, nil, nil)
	if err != nil {
		t.Fatalf("readFileImpl: %v", err)
	}
	if !strings.Contains(out, "Hello from PDF") {
		t.Errorf("expected converted text; got:\n%s", out)
	}
	if !strings.Contains(out, "converted from doc.pdf") {
		t.Errorf("expected provenance header; got:\n%s", out)
	}
	// PDF output must NOT carry the read_file line-number prefix ("    1\t").
	if strings.Contains(out, "    1\t") {
		t.Errorf("PDF output should be unnumbered; got:\n%s", out)
	}
}

func TestLooksLikePDF(t *testing.T) {
	if !looksLikePDF([]byte("%PDF-1.7\n...")) {
		t.Error("expected true for %PDF- header")
	}
	if looksLikePDF([]byte("not a pdf at all")) {
		t.Error("expected false for non-PDF")
	}
}
