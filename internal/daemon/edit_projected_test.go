package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- projectSpan unit tests (synthetic position maps) ---

func TestProjectSpanBoundarySnapping(t *testing.T) {
	// Minified output "a;b": token "a" (src 0), inserted ";" (synthetic, no
	// segment), token "b" (src 5). Mirrors an ASI semicolon between two
	// statements whose source bytes are far apart.
	segs := []srcSegment{
		{outStart: 0, length: 1, srcStart: 0},
		{outStart: 2, length: 1, srcStart: 5},
	}

	cases := []struct {
		name               string
		outStart, outEnd   int
		wantStart, wantEnd int
		wantOK             bool
	}{
		{"inside-first-token", 0, 1, 0, 1, true},
		{"full-span", 0, 3, 0, 6, true},
		{"separator-only", 1, 2, 0, 0, false},    // matches the inserted ';' alone
		{"snap-start-forward", 1, 3, 5, 6, true}, // ";b" -> snaps to b
		{"inside-second-token", 2, 3, 5, 6, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd, ok := projectSpan(segs, tc.outStart, tc.outEnd)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if gotStart != tc.wantStart || gotEnd != tc.wantEnd {
				t.Errorf("got [%d,%d), want [%d,%d)", gotStart, gotEnd, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// TestProjectSpanMapInvariant verifies, for every golden source fixture, that
// each position-map segment maps minified output bytes back to byte-identical
// source bytes, segments are monotonic, and the full-span projection covers the
// first through last token.
func TestProjectSpanMapInvariant(t *testing.T) {
	for _, ext := range extensions {
		t.Run(ext, func(t *testing.T) {
			content, err := os.ReadFile("testdata/tmp." + ext)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			out, segs, err := minifyWithTreeSitterMapped(string(content), "tmp."+ext, true)
			if err != nil {
				t.Fatalf("minify: %v", err)
			}
			if out == "" || len(segs) == 0 {
				t.Fatalf("expected non-empty minified output and segments")
			}

			prevOut, prevSrc := -1, -1
			for i, s := range segs {
				if s.outStart <= prevOut {
					t.Fatalf("seg %d outStart %d not increasing (prev %d)", i, s.outStart, prevOut)
				}
				if s.srcStart < prevSrc {
					t.Fatalf("seg %d srcStart %d not monotonic (prev %d)", i, s.srcStart, prevSrc)
				}
				prevOut, prevSrc = s.outStart, s.srcStart

				if s.outStart+s.length > len(out) || s.srcStart+s.length > len(content) {
					t.Fatalf("seg %d out of range", i)
				}
				gotOut := out[s.outStart : s.outStart+s.length]
				gotSrc := string(content[s.srcStart : s.srcStart+s.length])
				if gotOut != gotSrc {
					t.Errorf("seg %d: out %q != src %q", i, gotOut, gotSrc)
				}
			}

			last := segs[len(segs)-1]
			srcStart, srcEnd, ok := projectSpan(segs, 0, len(out))
			if !ok {
				t.Fatalf("full-span projection failed")
			}
			if srcStart != segs[0].srcStart || srcEnd != last.srcStart+last.length {
				t.Errorf("full span = [%d,%d), want [%d,%d)", srcStart, srcEnd, segs[0].srcStart, last.srcStart+last.length)
			}
		})
	}
}

// --- VfsEdit projected-splice tests ---

func writeTempSource(t *testing.T, name, content string) (cwd, base string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp source: %v", err)
	}
	return dir, name
}

func readBack(t *testing.T, cwd, base string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cwd, base))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(b)
}

const goSample = `package main

// Greeter returns a greeting.
func Oldname() string {
	return "hi"
}
`

// TestVfsEditRenamePreservesSurroundings renames a single identifier supplied in
// minified form and asserts that only that identifier's bytes change — comments,
// blank lines, and indentation are preserved exactly.
func TestVfsEditRenamePreservesSurroundings(t *testing.T) {
	cwd, base := writeTempSource(t, "rename.go", goSample)

	msg, lineOffset, err := VfsEdit(cwd, nil, "", base, "Oldname", "Newname", true)
	if err != nil {
		t.Fatalf("VfsEdit: %v", err)
	}
	if !strings.Contains(msg, "replaced 1 occurrence") {
		t.Errorf("unexpected message: %q", msg)
	}
	if want := strings.Count(goSample[:strings.Index(goSample, "Oldname")], "\n"); lineOffset != want {
		t.Errorf("lineOffset = %d, want %d", lineOffset, want)
	}

	got := readBack(t, cwd, base)
	want := strings.Replace(goSample, "Oldname", "Newname", 1)
	if got != want {
		t.Errorf("file mismatch.\n got: %q\nwant: %q", got, want)
	}
}

// TestVfsEditOneCharDiff matches a single token in minified form and changes one
// character; the whole file must differ by exactly that one character.
func TestVfsEditOneCharDiff(t *testing.T) {
	src := "package main\n\nfunc f() int {\n\tx := 12345\n\treturn x\n}\n"
	cwd, base := writeTempSource(t, "onechar.go", src)

	if _, _, err := VfsEdit(cwd, nil, "", base, "12345", "12340", true); err != nil {
		t.Fatalf("VfsEdit: %v", err)
	}

	got := readBack(t, cwd, base)
	want := strings.Replace(src, "12345", "12340", 1)
	if got != want {
		t.Fatalf("file mismatch.\n got: %q\nwant: %q", got, want)
	}
	// Confirm exactly one byte differs vs the original.
	diff := 0
	for i := 0; i < len(src) && i < len(got); i++ {
		if src[i] != got[i] {
			diff++
		}
	}
	if diff != 1 || len(src) != len(got) {
		t.Errorf("expected exactly one char difference, got %d (len %d vs %d)", diff, len(src), len(got))
	}
}

// TestVfsEditMultiTokenSpan matches a span that crosses an original line break
// and replaces it; every byte outside the matched source span must be identical.
func TestVfsEditMultiTokenSpan(t *testing.T) {
	src := "package main\n\nfunc f() int {\n\ta := g(1,\n\t\t2)\n\tkeep := true\n\t_ = keep\n\treturn a\n}\n"
	cwd, base := writeTempSource(t, "span.go", src)

	// In the minified view the call is "g(1,2)"; the source spans two lines.
	if _, _, err := VfsEdit(cwd, nil, "", base, "g(1,2)", "g(1,2,3)", true); err != nil {
		t.Fatalf("VfsEdit: %v", err)
	}

	got := readBack(t, cwd, base)
	srcSpan := "g(1,\n\t\t2)"
	want := strings.Replace(src, srcSpan, "g(1,2,3)", 1)
	if got != want {
		t.Fatalf("file mismatch.\n got: %q\nwant: %q", got, want)
	}
	// The untouched trailing lines must be present verbatim.
	if !strings.Contains(got, "\tkeep := true\n\t_ = keep\n\treturn a\n") {
		t.Errorf("surrounding lines were altered: %q", got)
	}
}

// TestVfsEditRoundTrip edits A->B then B->A and expects the original file back.
func TestVfsEditRoundTrip(t *testing.T) {
	cwd, base := writeTempSource(t, "roundtrip.go", goSample)

	if _, _, err := VfsEdit(cwd, nil, "", base, "Oldname", "Zqxwv", true); err != nil {
		t.Fatalf("A->B: %v", err)
	}
	if _, _, err := VfsEdit(cwd, nil, "", base, "Zqxwv", "Oldname", true); err != nil {
		t.Fatalf("B->A: %v", err)
	}
	if got := readBack(t, cwd, base); got != goSample {
		t.Errorf("round trip not identity.\n got: %q\nwant: %q", got, goSample)
	}
}

// TestVfsEditRoundTripFixtures runs the A->B->A round trip across every golden
// source fixture: rename a unique identifier to a sentinel, then back, and
// assert the file is byte-identical to the original. This exercises the full
// edit_minified_file path (VfsEdit) per language.
func TestVfsEditRoundTripFixtures(t *testing.T) {
	const sentinel = "Zq9RtX0"
	for _, ext := range extensions {
		t.Run(ext, func(t *testing.T) {
			content, err := os.ReadFile("testdata/tmp." + ext)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			out, segs, err := minifyWithTreeSitterMapped(string(content), "tmp."+ext, true)
			if err != nil || out == "" {
				t.Fatalf("minify: %v", err)
			}

			// Pick the longest all-word-char token that occurs exactly once in
			// the minified view (so the match is unambiguous). The longest such
			// token is almost always a real identifier rather than a keyword.
			orig := ""
			for _, s := range segs {
				tok := out[s.outStart : s.outStart+s.length]
				if len(tok) <= len(orig) || !allWordChars(tok) {
					continue
				}
				if strings.Count(out, tok) == 1 {
					orig = tok
				}
			}
			if orig == "" {
				t.Skipf("no unique identifier token in tmp.%s", ext)
			}
			if strings.Contains(out, sentinel) {
				t.Skipf("sentinel collides in tmp.%s", ext)
			}

			cwd, base := writeTempSource(t, "tmp."+ext, string(content))
			if _, _, err := VfsEdit(cwd, nil, "", base, orig, sentinel, true); err != nil {
				t.Fatalf("A->B (%q): %v", orig, err)
			}
			if got := readBack(t, cwd, base); !strings.Contains(got, sentinel) {
				t.Fatalf("sentinel not written for %q", orig)
			}
			if _, _, err := VfsEdit(cwd, nil, "", base, sentinel, orig, true); err != nil {
				t.Fatalf("B->A (%q): %v", orig, err)
			}
			if got := readBack(t, cwd, base); got != string(content) {
				t.Errorf("round trip not identity for tmp.%s (token %q)", ext, orig)
			}
		})
	}
}

func allWordChars(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isWordChar(s[i]) {
			return false
		}
	}
	return true
}

// TestVfsEditUniqueness rejects ambiguous matches and reports missing ones.
func TestVfsEditUniqueness(t *testing.T) {
	src := "package main\n\nvar dup = 1\nvar other = dup\n"
	cwd, base := writeTempSource(t, "dup.go", src)

	if _, _, err := VfsEdit(cwd, nil, "", base, "dup", "x", true); err == nil ||
		!strings.Contains(err.Error(), "found 2 times") {
		t.Errorf("expected ambiguous-match error, got %v", err)
	}
	if _, _, err := VfsEdit(cwd, nil, "", base, "nonexistent", "x", true); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

// TestVfsEditCommentWithinSpan: with comments kept, a comment is an ordinary
// token, so a span containing it is matched and replaced predictably. The
// old_string is derived from the position map so the test is self-consistent.
func TestVfsEditCommentWithinSpan(t *testing.T) {
	src := "package main\n\nfunc f() {\n\ta := 1 // note\n\t_ = a\n}\n"
	cwd, base := writeTempSource(t, "comment.go", src)

	out, segs, err := minifyWithTreeSitterMapped(src, base, true)
	if err != nil {
		t.Fatalf("minify: %v", err)
	}
	// Find the comment token, then build a span from the token before it through
	// the token after it (so the span straddles the comment).
	ci := -1
	for i, s := range segs {
		if strings.Contains(out[s.outStart:s.outStart+s.length], "// note") {
			ci = i
			break
		}
	}
	if ci <= 0 || ci+1 >= len(segs) {
		t.Fatalf("comment token not found between two code tokens (ci=%d)", ci)
	}
	startSeg, endSeg := segs[ci-1], segs[ci+1]
	oldString := out[startSeg.outStart : endSeg.outStart+endSeg.length]
	if !strings.Contains(oldString, "// note") {
		t.Fatalf("derived old_string does not include the comment: %q", oldString)
	}

	if _, _, err := VfsEdit(cwd, nil, "", base, oldString, "REPLACED", true); err != nil {
		t.Fatalf("VfsEdit: %v", err)
	}

	got := readBack(t, cwd, base)
	wantSpan := src[startSeg.srcStart : endSeg.srcStart+endSeg.length]
	want := strings.Replace(src, wantSpan, "REPLACED", 1)
	if got != want {
		t.Errorf("span with comment not replaced as expected.\n got: %q\nwant: %q", got, want)
	}
}

// TestVfsEditUTF8 verifies byte-accurate splicing around multibyte content.
func TestVfsEditUTF8(t *testing.T) {
	src := "package main\n\n// 日本語 comment\nfunc f() string { return \"héllo wörld\" }\n"
	cwd, base := writeTempSource(t, "utf8.go", src)

	if _, _, err := VfsEdit(cwd, nil, "", base, "héllo", "hello", true); err != nil {
		t.Fatalf("VfsEdit: %v", err)
	}
	got := readBack(t, cwd, base)
	want := strings.Replace(src, "héllo", "hello", 1)
	if got != want {
		t.Errorf("utf8 splice mismatch.\n got: %q\nwant: %q", got, want)
	}
}

// TestVfsEditPreservesMode keeps the executable bit on edited files.
func TestVfsEditPreservesMode(t *testing.T) {
	dir := t.TempDir()
	base := "exec.go"
	p := filepath.Join(dir, base)
	if err := os.WriteFile(p, []byte(goSample), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := VfsEdit(dir, nil, "", base, "Oldname", "Newname", true); err != nil {
		t.Fatalf("VfsEdit: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", st.Mode().Perm())
	}
}

// TestVfsEditUnsupportedFallback surfaces errVFSUnsupported for non-minifiable
// files so callers can fall back to the literal edit path.
func TestVfsEditUnsupportedFallback(t *testing.T) {
	cwd, base := writeTempSource(t, "notes.txt", "hello world\n")
	if _, _, err := VfsEdit(cwd, nil, "", base, "hello", "bye", true); err != errVFSUnsupported {
		t.Errorf("expected errVFSUnsupported, got %v", err)
	}
}
