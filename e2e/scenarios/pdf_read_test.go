package scenarios

import (
	"fmt"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// pdfFixture returns a tiny, fully-ASCII PDF whose single page shows text. It
// exercises the real in-daemon PDF→Markdown conversion path of read_file.
func pdfFixture(text string) string {
	content := fmt.Sprintf("BT /F1 12 Tf 1 0 0 1 72 700 Tm (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	out := "%PDF-1.7\n"
	for i, o := range objs {
		out += fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	out += "trailer\n<< /Size 6 /Root 1 0 R >>\n%%EOF"
	return out
}

// TestReadPDFAsMarkdown drives read_file against a seeded PDF fixture and proves
// the real daemon converted it to Markdown text: the extracted string flows back
// over the wire in a tool_result and the model's confirmation renders on screen.
func TestReadPDFAsMarkdown(t *testing.T) {
	const marker = "Hello from a PDF fixture"
	h := harness.Start(t, harness.Meta{
		Category:    "files",
		Subcategory: "files.read_pdf",
		Description: "read_file converts a seeded PDF to Markdown; extracted text flows back over the wire",
		Wire:        harness.WireMessages,
	}, harness.WithWorkdirFile("doc.pdf", pdfFixture(marker)))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(
		harness.ToolUse("read_file", `{"path":"doc.pdf"}`),
		harness.Text("The PDF contains: "+marker),
	)

	h.UI.Type("read doc.pdf and tell me what it says")
	h.UI.Enter()

	h.UI.ResolveToolPrompts("The PDF contains")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-run")

	// Wire: a request carried a read_file tool_result bearing the converter's
	// provenance header, which is emitted only by the PDF→Markdown path — proof
	// the daemon converted rather than returning raw numbered bytes.
	if !anyToolResultContains(h, "converted from doc.pdf") {
		t.Fatalf("no request carried a converted-PDF tool_result (requests=%d)",
			len(h.Mock.Requests()))
	}
	if !anyToolResultContains(h, marker) {
		t.Fatalf("converted tool_result did not contain the fixture text %q", marker)
	}

	// Screen: the model's confirmation rendered.
	if !h.UI.Contains("The PDF contains") {
		t.Fatalf("final confirmation not rendered; screen:\n%s", h.UI.Snapshot())
	}
}
