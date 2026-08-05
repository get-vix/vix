package scenarios

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// encPasswordPad is the 32-byte PDF password padding constant. An empty user
// password pads to exactly these bytes.
var encPasswordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func encRC4(key, data []byte) []byte {
	c, err := rc4.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// encryptedPDFFixture builds a tiny AESV2 (V4/R4, 128-bit) PDF encrypted with an
// EMPTY user password — i.e. permissions-only encryption that every viewer opens
// without prompting. This is an independent oracle for the daemon's decryptor:
// the crypto here is written from scratch with the Go standard library.
func encryptedPDFFixture(text string) string {
	const keyLen = 16 // 128-bit
	id := []byte("0123456789abcdef")
	p := -3904

	// /O for empty owner+user password (Algorithm 3, R>=3).
	osum := md5.Sum(encPasswordPad)
	ok := osum[:]
	for i := 0; i < 50; i++ {
		s := md5.Sum(ok[:keyLen])
		ok = s[:]
	}
	orc4 := append([]byte(nil), ok[:keyLen]...)
	o := encRC4(orc4, encPasswordPad)
	for i := 1; i <= 19; i++ {
		k2 := make([]byte, len(orc4))
		for j := range orc4 {
			k2[j] = orc4[j] ^ byte(i)
		}
		o = encRC4(k2, o)
	}

	// File key (Algorithm 2, R>=3, EncryptMetadata true).
	kh := md5.New()
	kh.Write(encPasswordPad)
	kh.Write(o)
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], uint32(int32(p)))
	kh.Write(pb[:])
	kh.Write(id)
	fileKey := kh.Sum(nil)
	for i := 0; i < 50; i++ {
		s := md5.Sum(fileKey[:keyLen])
		fileKey = s[:]
	}
	fileKey = fileKey[:keyLen]

	// /U for the file key (Algorithm 5).
	uh := md5.New()
	uh.Write(encPasswordPad)
	uh.Write(id)
	u := encRC4(fileKey, uh.Sum(nil))
	for i := 1; i <= 19; i++ {
		k2 := make([]byte, len(fileKey))
		for j := range fileKey {
			k2[j] = fileKey[j] ^ byte(i)
		}
		u = encRC4(k2, u)
	}
	uEntry := make([]byte, 32)
	copy(uEntry, u[:16])

	// Per-object AESV2 key for the content stream (object 4, gen 0).
	oh := md5.New()
	oh.Write(fileKey)
	oh.Write([]byte{4, 0, 0}) // objNum LE
	oh.Write([]byte{0, 0})    // gen LE
	oh.Write([]byte{0x73, 0x41, 0x6C, 0x54})
	objKey := oh.Sum(nil)[:min(keyLen+5, 16)]

	// Encrypt the content stream: 16-byte IV prefix + AES-CBC + PKCS#7.
	content := fmt.Sprintf("BT /F1 12 Tf 1 0 0 1 72 700 Tm (%s) Tj ET", text)
	iv := bytes.Repeat([]byte{0x24}, 16)
	pad := 16 - len(content)%16
	padded := append([]byte(content), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(objKey)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	enc := append(append([]byte(nil), iv...), ct...)

	encDict := fmt.Sprintf("<< /Filter /Standard /V 4 /R 4 /Length 128 /P %d /O <%x> /U <%x> /CF << /StdCF << /CFM /AESV2 /Length 16 >> >> /StmF /StdCF /StrF /StdCF >>",
		p, o, uEntry)

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(enc), string(enc)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		encDict,
	}
	// A real xref table + startxref is required so the trailer (carrying
	// /Encrypt and /ID) is parsed; without it the reader would fall back to a
	// brute-force object scan that never sees the encryption dictionary.
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs)+1)
	for i, ob := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, ob)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Encrypt 6 0 R /ID [<%x><%x>] >>\nstartxref\n%d\n%%%%EOF",
		len(objs)+1, id, id, xrefStart)
	return buf.String()
}

// TestReadEmptyPasswordEncryptedPDF proves the daemon transparently decrypts a
// permissions-only ("empty user password") encrypted PDF: read_file converts it
// to Markdown, the decrypted text flows back over the wire, and no
// password-protected error surfaces. This is the exact scheme (AESV2/R4) that a
// distributed marketing PDF uses — previously rejected as "encrypted".
func TestReadEmptyPasswordEncryptedPDF(t *testing.T) {
	const marker = "Diversify your portfolio across global markets"
	h := harness.Start(t, harness.Meta{
		Category:    "files",
		Subcategory: "files.read_pdf_encrypted",
		Description: "read_file decrypts an empty-password (permissions-only) encrypted PDF; decrypted text flows back over the wire",
		Wire:        harness.WireMessages,
	}, harness.WithWorkdirFile("secured.pdf", encryptedPDFFixture(marker)))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(
		harness.ToolUse("read_file", `{"path":"secured.pdf"}`),
		harness.Text("The secured PDF says: "+marker),
	)

	h.UI.Type("read secured.pdf and tell me what it says")
	h.UI.Enter()

	h.UI.ResolveToolPrompts("The secured PDF says")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-run")

	// Wire: a request carried a converted-PDF tool_result containing the
	// decrypted fixture text — proof the daemon decrypted and converted rather
	// than returning a password-protected error.
	if !anyToolResultContains(h, "converted from secured.pdf") {
		t.Fatalf("no request carried a converted-PDF tool_result (requests=%d)",
			len(h.Mock.Requests()))
	}
	if !anyToolResultContains(h, marker) {
		t.Fatalf("decrypted tool_result did not contain the fixture text %q", marker)
	}
	if anyToolResultContains(h, "password-protected") {
		t.Fatalf("PDF was wrongly reported as password-protected")
	}

	// Screen: the model's confirmation rendered.
	if !h.UI.Contains("The secured PDF says") {
		t.Fatalf("final confirmation not rendered; screen:\n%s", h.UI.Snapshot())
	}
}
