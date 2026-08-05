package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// buildTextPDF assembles a minimal single-page PDF with an extractable text
// layer ("Hello PDF"), mirroring the helpers in internal/daemon/pdf.
func buildTextPDF() []byte {
	content := "BT\n/F1 24 Tf\n1 0 0 1 72 720 Tm\n(Hello PDF) Tj\nET"
	stream := fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		stream,
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", len(objs)+1, xrefStart)
	return b.Bytes()
}

func TestResolveFileAttachment_Text(t *testing.T) {
	status, reason, text := resolveFileAttachment("/tmp/notes.txt", []byte("hello world\n"))
	if status != attachmentOK {
		t.Fatalf("status = %q reason = %q, want ok", status, reason)
	}
	if text != "hello world\n" {
		t.Errorf("text = %q, want verbatim content", text)
	}
}

func TestResolveFileAttachment_NonUTF8(t *testing.T) {
	status, reason, _ := resolveFileAttachment("/tmp/data.txt", []byte{0xff, 0xfe, 0x00, 0x01})
	if status != attachmentInvalid {
		t.Fatalf("status = %q, want invalid", status)
	}
	if !strings.Contains(reason, "UTF-8") {
		t.Errorf("reason = %q, want UTF-8 complaint", reason)
	}
}

func TestResolveFileAttachment_PDF(t *testing.T) {
	status, reason, text := resolveFileAttachment("/tmp/doc.pdf", buildTextPDF())
	if status != attachmentOK {
		t.Fatalf("status = %q reason = %q, want ok", status, reason)
	}
	if !strings.Contains(text, "Hello PDF") {
		t.Errorf("extracted text = %q, want it to contain 'Hello PDF'", text)
	}
}

func TestResolveFileAttachment_PDFGarbage(t *testing.T) {
	status, _, _ := resolveFileAttachment("/tmp/doc.pdf", []byte("%PDF-1.7\nnot really a pdf"))
	if status != attachmentInvalid {
		t.Fatalf("status = %q, want invalid for unparseable PDF", status)
	}
}

func TestAddUserMessage_FileEmbed(t *testing.T) {
	s := &Thread{}
	s.AddUserMessage("summarize this", protocol.Attachment{
		Type: "file", MediaType: "text/plain", Data: "the file body", Path: "/tmp/doc.pdf",
	})
	if len(s.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(s.messages))
	}
	blocks := s.messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (text + file), got %d", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "[File: /tmp/doc.pdf]") {
		t.Errorf("main text missing file reference: %q", blocks[0].Text)
	}
	fb := blocks[1].Text
	if !strings.Contains(fb, "[Attached file: doc.pdf]") ||
		!strings.Contains(fb, "the file body") ||
		!strings.Contains(fb, "[End of doc.pdf]") {
		t.Errorf("file block malformed: %q", fb)
	}
}

func TestAddUserMessage_ImageStillImageBlock(t *testing.T) {
	s := &Thread{}
	s.AddUserMessage("look", protocol.Attachment{
		Type: "image", MediaType: "image/png", Data: "AAAA", Path: "/tmp/a.png",
	})
	blocks := s.messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[1].Type != llm.BlockImage {
		t.Errorf("image attachment should produce an image block, got %v", blocks[1].Type)
	}
}

func TestAddUserMessage_EmptyTextKeepsRefs(t *testing.T) {
	s := &Thread{}
	s.AddUserMessage("", protocol.Attachment{
		Type: "image", MediaType: "image/png", Data: "AAAA", Path: "/tmp/a.png",
	})
	blocks := s.messages[0].Content
	// An attachment-only send must persist the reference line (path) rather
	// than a bare "[Attachment]" placeholder, so replay can recover the name.
	if got := blocks[0].Text; got != "[Image: /tmp/a.png]" {
		t.Errorf("empty-text main block = %q, want %q", got, "[Image: /tmp/a.png]")
	}
}

func TestReadAttachmentFile_AllowedAndDenied(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(ok, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(secrets, "api.txt")
	if err := os.WriteFile(secret, []byte("SUPER_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newIntegrationThread(t, root, []string{secrets})

	raw, err := s.readAttachmentFile(ok)
	if err != nil {
		t.Fatalf("allowed file should read: %v", err)
	}
	if string(raw) != "hello" {
		t.Errorf("got %q, want hello", raw)
	}

	if _, err := s.readAttachmentFile(secret); err == nil {
		t.Error("denied file should be refused")
	}
}

// A user-dragged attachment may live anywhere on disk (e.g. an iCloud path
// outside the project), so readAttachmentFile must NOT enforce the working
// directory the way read_file does. It stays gated only by the deny list.
func TestReadAttachmentFile_OutsideWorkingDirAllowed(t *testing.T) {
	root := testRoot(t)
	outside := testRoot(t) // a sibling temp dir, not under root and not allowed
	external := filepath.Join(outside, "book.txt")
	if err := os.WriteFile(external, []byte("outside content"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newIntegrationThread(t, root, nil)

	raw, err := s.readAttachmentFile(external)
	if err != nil {
		t.Fatalf("attachment outside working dir should be allowed, got: %v", err)
	}
	if string(raw) != "outside content" {
		t.Errorf("got %q, want %q", raw, "outside content")
	}
}

func TestResolveUserAttachmentPath(t *testing.T) {
	root := testRoot(t)
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Absolute existing path resolves to itself (symlink-eval'd).
	got, err := resolveUserAttachmentPath(root, f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want, _ := filepath.EvalSymlinks(f); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Relative path joins against cwd.
	got, err = resolveUserAttachmentPath(root, "a.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want, _ := filepath.EvalSymlinks(f); got != want {
		t.Errorf("relative: got %q, want %q", got, want)
	}

	// Missing file is a clear not-found error.
	if _, err := resolveUserAttachmentPath(root, filepath.Join(root, "nope.txt")); err == nil {
		t.Error("expected error for missing file")
	}
}
