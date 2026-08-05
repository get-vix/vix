package ui

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createTestImage(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	// Write a minimal 1x1 PNG (valid header).
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x01, // chunk length
	}
	if err := os.WriteFile(path, png, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractImageAttachments_NoImages(t *testing.T) {
	text := "hello world, no images here"
	clean, att, errs := extractImageAttachments(text)
	if clean != text {
		t.Errorf("expected text unchanged, got %q", clean)
	}
	if len(att) != 0 {
		t.Errorf("expected no attachments, got %d", len(att))
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestExtractImageAttachments_SinglePath(t *testing.T) {
	dir := t.TempDir()
	imgPath := createTestImage(t, dir, "test.png")

	text := imgPath
	clean, att, errs := extractImageAttachments(text)
	if clean != "[Image #1]" {
		t.Errorf("expected '[Image #1]', got %q", clean)
	}
	if len(att) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(att))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if att[0].MediaType != "image/png" {
		t.Errorf("expected image/png, got %s", att[0].MediaType)
	}
	if att[0].Path != imgPath {
		t.Errorf("expected path %s, got %s", imgPath, att[0].Path)
	}
	// Verify base64 data is valid.
	if _, err := base64.StdEncoding.DecodeString(att[0].Data); err != nil {
		t.Errorf("invalid base64 data: %v", err)
	}
}

func TestExtractImageAttachments_SingleQuoted(t *testing.T) {
	dir := t.TempDir()
	imgPath := createTestImage(t, dir, "photo.jpg")

	text := "look at this '" + imgPath + "' please"
	clean, att, errs := extractImageAttachments(text)
	if clean != "look at this [Image #1] please" {
		t.Errorf("expected 'look at this [Image #1] please', got %q", clean)
	}
	if len(att) != 1 || len(errs) != 0 {
		t.Errorf("att=%d errs=%v", len(att), errs)
	}
}

func TestExtractImageAttachments_DoubleQuoted(t *testing.T) {
	dir := t.TempDir()
	imgPath := createTestImage(t, dir, "photo.jpeg")

	text := `check "` + imgPath + `" out`
	clean, att, errs := extractImageAttachments(text)
	if clean != "check [Image #1] out" {
		t.Errorf("expected 'check [Image #1] out', got %q", clean)
	}
	if len(att) != 1 || len(errs) != 0 {
		t.Errorf("att=%d errs=%v", len(att), errs)
	}
}

func TestExtractImageAttachments_EscapedSpaces(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "my folder")
	os.MkdirAll(subDir, 0755)
	imgPath := filepath.Join(subDir, "test.png")
	createTestImage(t, subDir, "test.png")

	// Simulate terminal escaping: /tmp/xxx/my\ folder/test.png
	escapedPath := filepath.Join(dir, "my\\ folder", "test.png")
	text := escapedPath
	clean, att, errs := extractImageAttachments(text)
	if clean != "[Image #1]" {
		t.Errorf("expected '[Image #1]', got %q", clean)
	}
	if len(att) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(att))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	_ = imgPath
}

func TestExtractImageAttachments_MultipleEscapedSpaces(t *testing.T) {
	// Real macOS screenshot drag-drop format: Screenshot\ 2026-03-10\ at\ 11.29.23.png
	dir := t.TempDir()
	subDir := filepath.Join(dir, "TemporaryItems")
	os.MkdirAll(subDir, 0755)
	createTestImage(t, subDir, "Screenshot 2026-03-10 at 11.29.23.png")

	// Terminal escapes every space with backslash
	escapedPath := subDir + `/Screenshot\ 2026-03-10\ at\ 11.29.23.png`
	text := escapedPath
	clean, att, errs := extractImageAttachments(text)
	if clean != "[Image #1]" {
		t.Errorf("expected '[Image #1]', got %q", clean)
	}
	if len(att) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(att))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestExtractImageAttachments_MultipleImages(t *testing.T) {
	dir := t.TempDir()
	img1 := createTestImage(t, dir, "a.png")
	img2 := createTestImage(t, dir, "b.gif")

	text := img1 + " and " + img2
	clean, att, errs := extractImageAttachments(text)
	if clean != "[Image #1] and [Image #2]" {
		t.Errorf("expected '[Image #1] and [Image #2]', got %q", clean)
	}
	if len(att) != 2 {
		t.Errorf("expected 2 attachments, got %d", len(att))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestExtractImageAttachments_NonexistentPath(t *testing.T) {
	text := "/nonexistent/path/to/image.png"
	clean, att, errs := extractImageAttachments(text)
	if clean != text {
		t.Errorf("expected text unchanged, got %q", clean)
	}
	if len(att) != 0 {
		t.Errorf("expected no attachments, got %d", len(att))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestExtractImageAttachments_NonImageExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "document.pdf")
	os.WriteFile(path, []byte("fake pdf"), 0644)

	text := path
	clean, att, errs := extractImageAttachments(text)
	if clean != text {
		t.Errorf("expected text unchanged, got %q", clean)
	}
	if len(att) != 0 {
		t.Errorf("expected no attachments, got %d", len(att))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestExtractImageAttachments_MixedTextAndImages(t *testing.T) {
	dir := t.TempDir()
	imgPath := createTestImage(t, dir, "screenshot.webp")

	text := "please check this " + imgPath + " and tell me what you see"
	clean, att, errs := extractImageAttachments(text)
	if clean != "please check this [Image #1] and tell me what you see" {
		t.Errorf("unexpected clean text: %q", clean)
	}
	if len(att) != 1 || len(errs) != 0 {
		t.Errorf("att=%d errs=%v", len(att), errs)
	}
}

func TestExtractImageAttachments_CaseInsensitiveExtension(t *testing.T) {
	dir := t.TempDir()
	imgPath := createTestImage(t, dir, "photo.PNG")

	text := imgPath
	clean, att, errs := extractImageAttachments(text)
	if clean != "[Image #1]" {
		t.Errorf("expected '[Image #1]', got %q", clean)
	}
	if len(att) != 1 || len(errs) != 0 {
		t.Errorf("att=%d errs=%v", len(att), errs)
	}
}

func TestMediaTypeFromExt(t *testing.T) {
	tests := map[string]string{
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".webp": "image/webp",
		".bmp":  "image/bmp",
	}
	for ext, want := range tests {
		got, ok := imageExtensions[ext]
		if !ok {
			t.Errorf("extension %s not found", ext)
			continue
		}
		if got != want {
			t.Errorf("imageExtensions[%s] = %s, want %s", ext, got, want)
		}
	}
}

func createTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectFileCandidates_TextAndPDF(t *testing.T) {
	dir := t.TempDir()
	txt := createTestFile(t, dir, "notes.txt", "hello")
	pdf := createTestFile(t, dir, "report.pdf", "%PDF-1.7 stub")

	cands := detectFileCandidates("see " + txt + " and " + pdf)
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %+v", len(cands), cands)
	}
	byPath := map[string]fileCandidate{}
	for _, c := range cands {
		byPath[c.Path] = c
	}
	if byPath[txt].Kind != "file" {
		t.Errorf("txt kind = %q, want file", byPath[txt].Kind)
	}
	if byPath[pdf].Kind != "pdf" {
		t.Errorf("pdf kind = %q, want pdf", byPath[pdf].Kind)
	}
	if byPath[txt].Raw != txt {
		t.Errorf("Raw = %q, want %q", byPath[txt].Raw, txt)
	}
}

func TestDetectFileCandidates_IgnoresImagesAndMissing(t *testing.T) {
	dir := t.TempDir()
	img := createTestImage(t, dir, "pic.png")
	missing := filepath.Join(dir, "gone.txt")

	cands := detectFileCandidates(img + " " + missing)
	if len(cands) != 0 {
		t.Errorf("expected no file candidates (image + missing), got %+v", cands)
	}
}

func TestExtractFileAttachments_Placeholders(t *testing.T) {
	dir := t.TempDir()
	txt := createTestFile(t, dir, "a.md", "# doc")
	pdf := createTestFile(t, dir, "b.pdf", "%PDF-1.7 stub")

	clean, atts := extractFileAttachments("read " + txt + " then " + pdf)
	if clean != "read [File #1] then [PDF #1]" {
		t.Errorf("clean = %q", clean)
	}
	if len(atts) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(atts))
	}
	for _, a := range atts {
		if a.Type != "file" {
			t.Errorf("attachment type = %q, want file", a.Type)
		}
		if a.Data != "" {
			t.Errorf("file attachment should be path-only, got Data of len %d", len(a.Data))
		}
	}
}

func TestExtractFileAttachments_None(t *testing.T) {
	clean, atts := extractFileAttachments("just some prose")
	if clean != "just some prose" || len(atts) != 0 {
		t.Errorf("clean=%q atts=%d", clean, len(atts))
	}
}

func TestUnescapeDropPath(t *testing.T) {
	cases := map[string]string{
		`/a/b`:                 `/a/b`,
		`/a\ b/c`:              `/a b/c`,
		`/com\~apple\~x/f.pdf`: `/com~apple~x/f.pdf`,
		`/a\(b\)\&c`:           `/a(b)&c`,
	}
	for in, want := range cases {
		if got := unescapeDropPath(in); got != want {
			t.Errorf("unescapeDropPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// escapeDrop mimics how a terminal escapes shell-special characters when a file
// is dragged into the input (spaces and `~`, as in iCloud `com~apple~` paths).
func escapeDrop(path string) string {
	r := strings.NewReplacer(" ", `\ `, "~", `\~`)
	return r.Replace(path)
}

func TestDetectFileCandidates_EscapedTildeAndSpace(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "com~apple~CloudDocs", "Mobile Documents")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	pdf := createTestFile(t, sub, "10-Blunders.PDF", "%PDF-1.7 stub")

	dropped := escapeDrop(pdf)
	cands := detectFileCandidates("look at " + dropped)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(cands), cands)
	}
	if cands[0].Path != pdf {
		t.Errorf("candidate path = %q, want unescaped %q", cands[0].Path, pdf)
	}
	if cands[0].Kind != "pdf" {
		t.Errorf("kind = %q, want pdf", cands[0].Kind)
	}
	if cands[0].Raw != dropped {
		t.Errorf("Raw = %q, want %q", cands[0].Raw, dropped)
	}
}

func TestExtractFileAttachments_EscapedTilde(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "com~apple~CloudDocs")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	pdf := createTestFile(t, sub, "doc.pdf", "%PDF-1.7 stub")

	clean, atts := extractFileAttachments("read " + escapeDrop(pdf))
	if clean != "read [PDF #1]" {
		t.Errorf("clean = %q", clean)
	}
	if len(atts) != 1 || atts[0].Path != pdf {
		t.Fatalf("expected attachment with unescaped path %q, got %+v", pdf, atts)
	}
}

func TestExtractImageAttachments_EscapedTilde(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "com~apple~CloudDocs")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	img := createTestImage(t, sub, "pic.png")

	clean, atts, errs := extractImageAttachments("here " + escapeDrop(img))
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if clean != "here [Image #1]" {
		t.Errorf("clean = %q", clean)
	}
	if len(atts) != 1 || atts[0].Path != img {
		t.Fatalf("expected image attachment with unescaped path %q, got %+v", img, atts)
	}
}
