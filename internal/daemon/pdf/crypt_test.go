package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
)

// The tests below build empty-password encrypted PDFs from scratch (the
// encryption side of the Standard Security Handler) and verify the reader
// decrypts them. Fixtures reuse the package's own primitives (passwordPad,
// rc4Crypt, deriveKeyStd, hash2B) so the round-trip is self-checking.

const encTestContent = `BT
/F1 24 Tf
1 0 0 1 72 720 Tm
(Secret Heading) Tj
/F1 12 Tf
1 0 0 1 72 680 Tm
(Encrypted body text here.) Tj
ET`

func pkcs7pad(data []byte, bs int) []byte {
	n := bs - len(data)%bs
	return append(append([]byte{}, data...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func aesCBCEncrypt(key, iv, pt []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, pt)
	return ct
}

// encryptContent encrypts a stream/string body the way a producer would, using
// the per-object key implied by dec.
func encryptContent(dec *decryptor, num, gen int, pt []byte) []byte {
	key := dec.objectKey(num, gen)
	if dec.aes {
		iv := bytes.Repeat([]byte{0x24}, 16)
		return append(append([]byte{}, iv...), aesCBCEncrypt(key, iv, pkcs7pad(pt, 16))...)
	}
	return rc4Crypt(key, pt)
}

// computeOEntry produces the /O entry for empty owner and user passwords.
func computeOEntry(r, keyLen int) []byte {
	sum := md5.Sum(passwordPad)
	k := sum[:]
	if r >= 3 {
		for i := 0; i < 50; i++ {
			s := md5.Sum(k[:keyLen])
			k = s[:]
		}
	}
	rc4key := append([]byte(nil), k[:keyLen]...)
	out := rc4Crypt(rc4key, passwordPad)
	if r >= 3 {
		for i := 1; i <= 19; i++ {
			k2 := make([]byte, len(rc4key))
			for j := range rc4key {
				k2[j] = rc4key[j] ^ byte(i)
			}
			out = rc4Crypt(k2, out)
		}
	}
	return out
}

// computeUEntry produces the /U entry for the given file key (empty password).
func computeUEntry(fileKey, id []byte, r int) []byte {
	if r == 2 {
		return rc4Crypt(fileKey, passwordPad)
	}
	h := md5.New()
	h.Write(passwordPad)
	h.Write(id)
	out := rc4Crypt(fileKey, h.Sum(nil))
	for i := 1; i <= 19; i++ {
		k2 := make([]byte, len(fileKey))
		for j := range fileKey {
			k2[j] = fileKey[j] ^ byte(i)
		}
		out = rc4Crypt(k2, out)
	}
	res := make([]byte, 32) // first 16 bytes meaningful, rest arbitrary padding
	copy(res, out[:16])
	return res
}

// buildStdEncryptedPDF assembles an empty-password encrypted PDF for revisions
// 2–4 (RC4 or AESV2). corruptU simulates a real (non-empty) password.
func buildStdEncryptedPDF(v, r, keyBits int, isAES, corruptU bool) []byte {
	keyLen := keyBits / 8
	id := []byte("0123456789abcdef")
	p := -3904
	o := computeOEntry(r, keyLen)
	fileKey := deriveKeyStd(o, p, id, r, keyLen, true)
	u := computeUEntry(fileKey, id, r)
	if corruptU {
		u[0] ^= 0xFF
	}
	dec := &decryptor{key: fileKey, aes: isAES, encryptStreams: true, encryptStrings: true}
	enc := encryptContent(dec, 4, 0, []byte(encTestContent))

	cfPart := ""
	if v >= 4 {
		cfm := "AESV2"
		cfPart = fmt.Sprintf(" /CF << /StdCF << /CFM /%s /Length %d >> >> /StmF /StdCF /StrF /StdCF", cfm, keyLen)
	}
	encDict := fmt.Sprintf("<< /Filter /Standard /V %d /R %d /Length %d /P %d /O <%x> /U <%x>%s >>",
		v, r, keyBits, p, o, u, cfPart)

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(enc), string(enc)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		encDict,
	}
	data := buildPDF(objs, 1)
	return injectTrailer(data, id)
}

// buildAES256PDF assembles an empty-password AESV3 (V5/R6) PDF.
func buildAES256PDF(corruptU bool) []byte {
	fileKey := make([]byte, 32)
	valSalt := make([]byte, 8)
	keySalt := make([]byte, 8)
	rand.Read(fileKey)
	rand.Read(valSalt)
	rand.Read(keySalt)

	uHash := hash2B(nil, valSalt, nil)
	u := append(append(append([]byte(nil), uHash...), valSalt...), keySalt...) // 48 bytes
	if corruptU {
		u[0] ^= 0xFF
	}
	ik := hash2B(nil, keySalt, nil)
	ue := aesCBCEncrypt(ik, make([]byte, 16), fileKey) // 32 bytes, no padding
	o := make([]byte, 48)                              // unused by decryption
	oe := make([]byte, 32)

	dec := &decryptor{key: fileKey, aes: true, aes256: true, encryptStreams: true, encryptStrings: true}
	enc := encryptContent(dec, 4, 0, []byte(encTestContent))

	encDict := fmt.Sprintf("<< /Filter /Standard /V 5 /R 6 /Length 256 /P -3904 /O <%x> /U <%x> /OE <%x> /UE <%x> /CF << /StdCF << /CFM /AESV3 /Length 32 >> >> /StmF /StdCF /StrF /StdCF >>",
		o, u, oe, ue)

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(enc), string(enc)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		encDict,
	}
	data := buildPDF(objs, 1)
	return injectTrailer(data, []byte("0123456789abcdef"))
}

// injectTrailer adds /Encrypt (object 6) and /ID to the trailer buildPDF wrote.
func injectTrailer(data, id []byte) []byte {
	repl := fmt.Sprintf("/Root 1 0 R /Encrypt 6 0 R /ID [<%x><%x>]", id, id)
	return bytes.Replace(data, []byte("/Root 1 0 R"), []byte(repl), 1)
}

func assertDecrypts(t *testing.T, data []byte) {
	t.Helper()
	res, err := ToMarkdown(data)
	if err != nil {
		t.Fatalf("ToMarkdown: unexpected error %v", err)
	}
	if res.Scanned {
		t.Fatalf("expected extractable text, got Scanned")
	}
	if !strings.Contains(res.Markdown, "# Secret Heading") {
		t.Errorf("heading missing; got:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "Encrypted body text here.") {
		t.Errorf("body missing; got:\n%s", res.Markdown)
	}
}

func TestDecrypt_RC4_R2_40bit(t *testing.T) {
	assertDecrypts(t, buildStdEncryptedPDF(1, 2, 40, false, false))
}
func TestDecrypt_RC4_R3_128bit(t *testing.T) {
	assertDecrypts(t, buildStdEncryptedPDF(2, 3, 128, false, false))
}
func TestDecrypt_AESV2_R4(t *testing.T) {
	assertDecrypts(t, buildStdEncryptedPDF(4, 4, 128, true, false))
}
func TestDecrypt_AESV3_R6(t *testing.T) { assertDecrypts(t, buildAES256PDF(false)) }

// A genuinely password-protected PDF (the empty password fails validation) must
// still surface ErrEncrypted rather than emitting garbage.
func TestDecrypt_WrongPasswordStdStillEncrypted(t *testing.T) {
	if _, err := ToMarkdown(buildStdEncryptedPDF(4, 4, 128, true, true)); err != ErrEncrypted {
		t.Errorf("expected ErrEncrypted, got %v", err)
	}
}

func TestDecrypt_WrongPasswordAES256StillEncrypted(t *testing.T) {
	if _, err := ToMarkdown(buildAES256PDF(true)); err != ErrEncrypted {
		t.Errorf("expected ErrEncrypted, got %v", err)
	}
}

// An unsupported security handler must surface ErrEncrypted.
func TestDecrypt_UnsupportedHandler(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [] /Count 0 >>",
		"<< /Filter /FooBarHandler /V 4 /R 4 >>",
	}
	data := buildPDF(objs, 1)
	data = bytes.Replace(data, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 3 0 R"), 1)
	if _, err := ToMarkdown(data); err != ErrEncrypted {
		t.Errorf("expected ErrEncrypted, got %v", err)
	}
}
