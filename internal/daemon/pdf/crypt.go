package pdf

// This file implements the PDF Standard Security Handler for the common
// empty-password case: PDFs that declare an /Encrypt dictionary but can be
// opened without a password (permissions-only encryption). Every mainstream
// viewer opens these without prompting; vix decrypts their strings and streams
// so the text extractor can run.
//
// Coverage: Standard handler revisions 2–4 (RC4-40/128 and AESV2/128-bit AES)
// and revisions 5–6 (AESV3/256-bit AES). Only the empty user password is
// attempted — if it does not validate, the document genuinely needs a password
// and ErrEncrypted is returned, unchanged from before. All primitives come from
// the Go standard library, so the package stays dependency-free.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
)

// passwordPad is the 32-byte padding string from the PDF spec (Algorithm 2). An
// empty user password pads to exactly these bytes.
var passwordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// decryptor holds the file encryption key and cipher parameters needed to
// decrypt an encrypted PDF's strings and streams.
type decryptor struct {
	key            []byte // file encryption key
	aes            bool   // AES (AESV2/AESV3) vs RC4
	aes256         bool   // 256-bit AES (V5/R6): no per-object key salting
	encryptStrings bool
	encryptStreams bool
}

// newDecryptor derives the file key for the empty user password from the
// /Encrypt dictionary and the document /ID. It returns ErrEncrypted when the
// handler is unsupported or the empty password does not unlock the file (i.e.
// the PDF is genuinely password-protected).
func newDecryptor(d *Document, enc Dict, id []byte) (*decryptor, error) {
	if filter := name(d.Resolve(enc["Filter"])); filter != "" && filter != "Standard" {
		return nil, ErrEncrypted // only the Standard security handler is supported
	}
	v, _ := Int(d.Resolve(enc["V"]))
	r, _ := Int(d.Resolve(enc["R"]))
	length, _ := Int(d.Resolve(enc["Length"]))
	if length == 0 {
		length = 40
	}
	p, _ := Int(d.Resolve(enc["P"]))
	o := toBytes(d.Resolve(enc["O"]))
	u := toBytes(d.Resolve(enc["U"]))
	encMeta := true
	if b, ok := d.Resolve(enc["EncryptMetadata"]).(Boolean); ok {
		encMeta = bool(b)
	}

	dec := &decryptor{encryptStrings: true, encryptStreams: true}

	// For V4/V5, the cipher (RC4 vs AES) and which items are encrypted are
	// described by named crypt filters referenced from /StmF and /StrF.
	if v >= 4 {
		cf, _ := d.Resolve(enc["CF"]).(Dict)
		methodOf := func(fn Name) Name {
			if fn == "" || fn == "Identity" {
				return "Identity"
			}
			if cf != nil {
				if fd, ok := d.Resolve(cf[fn]).(Dict); ok {
					return name(d.Resolve(fd["CFM"]))
				}
			}
			return "Identity"
		}
		stmM := methodOf(name(d.Resolve(enc["StmF"])))
		strM := methodOf(name(d.Resolve(enc["StrF"])))
		dec.encryptStreams = stmM != "Identity"
		dec.encryptStrings = strM != "Identity"
		m := stmM
		if m == "Identity" {
			m = strM
		}
		switch m {
		case "AESV2":
			dec.aes = true
			length = 128
		case "AESV3":
			dec.aes = true
			dec.aes256 = true
			length = 256
		}
	}
	keyLen := length / 8

	// Revisions 5–6 use 256-bit AES with a SHA-2-based key derivation.
	if r >= 5 {
		key, ok := deriveKeyAES256(nil, u, toBytes(d.Resolve(enc["UE"])), r)
		if !ok {
			return nil, ErrEncrypted
		}
		dec.aes = true
		dec.aes256 = true
		dec.key = key
		return dec, nil
	}

	// Revisions 2–4: MD5-based key derivation, RC4 or AESV2 object ciphers.
	if len(o) < 32 {
		return nil, ErrEncrypted
	}
	key := deriveKeyStd(o, p, id, r, keyLen, encMeta)
	if !validateUserPassword(key, u, id, r) {
		return nil, ErrEncrypted // empty password did not unlock: needs a password
	}
	dec.key = key
	return dec, nil
}

// decryptObject returns a copy of o with every String and Stream decrypted,
// using the object number and generation of the containing indirect object.
func (c *decryptor) decryptObject(num, gen int, o Object) Object {
	switch v := o.(type) {
	case String:
		if c.encryptStrings {
			return String(c.decryptData(num, gen, []byte(v)))
		}
		return v
	case Stream:
		nd := make(Dict, len(v.Dict))
		for k, val := range v.Dict {
			nd[k] = c.decryptObject(num, gen, val)
		}
		raw := v.Raw
		if c.encryptStreams {
			raw = c.decryptData(num, gen, v.Raw)
		}
		return Stream{Dict: nd, Raw: raw}
	case Dict:
		nd := make(Dict, len(v))
		for k, val := range v {
			nd[k] = c.decryptObject(num, gen, val)
		}
		return nd
	case Array:
		na := make(Array, len(v))
		for i, val := range v {
			na[i] = c.decryptObject(num, gen, val)
		}
		return na
	}
	return o
}

// decryptData decrypts a single string or stream body with the per-object key.
func (c *decryptor) decryptData(num, gen int, data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	key := c.objectKey(num, gen)
	if c.aes {
		return aesCBCDecrypt(key, data)
	}
	return rc4Crypt(key, data)
}

// objectKey computes the RC4/AES key for object (num, gen). For 256-bit AES the
// file key is used directly (no per-object salting).
func (c *decryptor) objectKey(num, gen int) []byte {
	if c.aes256 {
		return c.key
	}
	h := md5.New()
	h.Write(c.key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16)})
	h.Write([]byte{byte(gen), byte(gen >> 8)})
	if c.aes {
		h.Write([]byte{0x73, 0x41, 0x6C, 0x54}) // "sAlT"
	}
	sum := h.Sum(nil)
	n := len(c.key) + 5
	if n > 16 {
		n = 16
	}
	return sum[:n]
}

// deriveKeyStd implements Algorithm 2 for revisions 2–4 (empty user password).
func deriveKeyStd(o []byte, p int, id []byte, r, keyLen int, encMeta bool) []byte {
	h := md5.New()
	h.Write(passwordPad)
	ob := make([]byte, 32)
	copy(ob, o)
	h.Write(ob)
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], uint32(int32(p)))
	h.Write(pb[:])
	h.Write(id)
	if r >= 4 && !encMeta {
		h.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	}
	sum := h.Sum(nil)
	if keyLen > len(sum) {
		keyLen = len(sum)
	}
	if r >= 3 {
		for i := 0; i < 50; i++ {
			s := md5.Sum(sum[:keyLen])
			sum = s[:]
		}
	}
	return append([]byte(nil), sum[:keyLen]...)
}

// validateUserPassword recomputes /U for the derived key and compares it with
// the stored value (Algorithm 4 for R2, Algorithm 5 for R3+).
func validateUserPassword(key, u, id []byte, r int) bool {
	if r == 2 {
		want := rc4Crypt(key, passwordPad)
		return len(u) >= 32 && bytes.Equal(want, u[:32])
	}
	h := md5.New()
	h.Write(passwordPad)
	h.Write(id)
	sum := h.Sum(nil)
	out := rc4Crypt(key, sum)
	for i := 1; i <= 19; i++ {
		k2 := make([]byte, len(key))
		for j := range key {
			k2[j] = key[j] ^ byte(i)
		}
		out = rc4Crypt(k2, out)
	}
	// Only the first 16 bytes are meaningful (the rest is arbitrary padding).
	return len(u) >= 16 && bytes.Equal(out[:16], u[:16])
}

// deriveKeyAES256 recovers the 256-bit file key for revisions 5–6 using the
// empty user password (Algorithm 2.A with the 2.B hardened hash for R6).
func deriveKeyAES256(password, u, ue []byte, r int) ([]byte, bool) {
	if len(u) < 48 || len(ue) < 32 {
		return nil, false
	}
	valSalt := u[32:40]
	keySalt := u[40:48]
	if !bytes.Equal(aes256Hash(password, valSalt, r), u[:32]) {
		return nil, false // empty password did not validate
	}
	ik := aes256Hash(password, keySalt, r)
	block, err := aes.NewCipher(ik)
	if err != nil {
		return nil, false
	}
	fileKey := make([]byte, 32)
	cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(fileKey, ue[:32])
	return fileKey, true
}

// aes256Hash is the revision-5 hash (a single SHA-256) or the revision-6
// hardened hash (Algorithm 2.B).
func aes256Hash(password, salt []byte, r int) []byte {
	if r == 5 {
		s := sha256.Sum256(append(append([]byte{}, password...), salt...))
		return s[:]
	}
	return hash2B(password, salt, nil)
}

// hash2B implements Algorithm 2.B from ISO 32000-2 (the R6 password hash).
func hash2B(password, salt, udata []byte) []byte {
	h := sha256.New()
	h.Write(password)
	h.Write(salt)
	h.Write(udata)
	k := h.Sum(nil)

	for round := 0; ; round++ {
		block := make([]byte, 0, len(password)+len(k)+len(udata))
		block = append(block, password...)
		block = append(block, k...)
		block = append(block, udata...)
		k1 := bytes.Repeat(block, 64)

		blk, err := aes.NewCipher(k[:16])
		if err != nil {
			return k[:32]
		}
		e := make([]byte, len(k1))
		cipher.NewCBCEncrypter(blk, k[16:32]).CryptBlocks(e, k1)

		var mod int
		for _, b := range e[:16] {
			mod += int(b)
		}
		switch mod % 3 {
		case 0:
			s := sha256.Sum256(e)
			k = s[:]
		case 1:
			s := sha512.Sum384(e)
			k = s[:]
		case 2:
			s := sha512.Sum512(e)
			k = s[:]
		}
		if round >= 63 && int(e[len(e)-1]) <= round-32 {
			break
		}
	}
	return k[:32]
}

// aesCBCDecrypt decrypts AES-CBC data whose first 16 bytes are the IV, stripping
// PKCS#7 padding. It returns nil on malformed input.
func aesCBCDecrypt(key, data []byte) []byte {
	if len(data) < aes.BlockSize {
		return nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	iv := data[:aes.BlockSize]
	ct := data[aes.BlockSize:]
	if n := len(ct) - len(ct)%aes.BlockSize; n != len(ct) {
		ct = ct[:n] // tolerate a truncated final block
	}
	if len(ct) == 0 {
		return nil
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	if n := len(pt); n > 0 {
		if pad := int(pt[n-1]); pad > 0 && pad <= aes.BlockSize && pad <= n {
			pt = pt[:n-pad]
		}
	}
	return pt
}

// rc4Crypt applies RC4 (symmetric: same routine encrypts and decrypts).
func rc4Crypt(key, data []byte) []byte {
	c, err := rc4.NewCipher(key)
	if err != nil {
		return data
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// toBytes returns the raw bytes of a String object, or nil.
func toBytes(o Object) []byte {
	if s, ok := o.(String); ok {
		return []byte(s)
	}
	return nil
}
