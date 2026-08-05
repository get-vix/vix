package dazsecrets

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
)

const (
	protocolMajor = uint16(1)
	protocolMinor = uint16(0)
	maxPayload    = 16 << 20
	maxValue      = 8 << 20
)

var magic = [4]byte{'D', 'S', 'E', 'C'}

type request struct {
	ID               []byte  `cbor:"id"`
	MinMinor         uint16  `cbor:"min_minor"`
	MaxMinor         uint16  `cbor:"max_minor"`
	Operation        string  `cbor:"operation"`
	Service          string  `cbor:"service,omitempty"`
	Account          string  `cbor:"account,omitempty"`
	Value            *[]byte `cbor:"value,omitempty"`
	ExpectedRevision *string `cbor:"expected_revision,omitempty"`
	DeadlineMS       uint64  `cbor:"deadline_ms"`
}

type response struct {
	ID         []byte      `cbor:"id"`
	ProviderID string      `cbor:"provider_id"`
	Major      uint16      `cbor:"major"`
	Minor      uint16      `cbor:"minor"`
	OK         bool        `cbor:"ok"`
	Code       ErrorCode   `cbor:"code,omitempty"`
	Value      *[]byte     `cbor:"value,omitempty"`
	Revision   string      `cbor:"revision,omitempty"`
	Deleted    bool        `cbor:"deleted,omitempty"`
	Metadata   *[]Metadata `cbor:"metadata,omitempty"`
}

var encMode = mustEncodingMode()
var decMode = mustDecodingMode()

func mustEncodingMode() cbor.EncMode {
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func mustDecodingMode() cbor.DecMode {
	mode, err := (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, ExtraReturnErrors: cbor.ExtraDecErrorUnknownField, UTF8: cbor.UTF8RejectInvalid}).DecMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func writeFrame(w io.Writer, value any) error {
	payload, err := encMode.Marshal(value)
	if err != nil || len(payload) > maxPayload {
		return errors.New("invalid payload")
	}
	header := make([]byte, 12)
	copy(header, magic[:])
	binary.BigEndian.PutUint16(header[4:6], protocolMajor)
	binary.BigEndian.PutUint16(header[6:8], protocolMinor)
	binary.BigEndian.PutUint32(header[8:12], uint32(len(payload)))
	if _, err = w.Write(header); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

func readFrame(r io.Reader, value any) error {
	header := make([]byte, 12)
	if _, err := io.ReadFull(r, header); err != nil {
		return err
	}
	if !bytes.Equal(header[:4], magic[:]) || binary.BigEndian.Uint16(header[4:6]) != protocolMajor || binary.BigEndian.Uint16(header[6:8]) != protocolMinor {
		return errors.New("invalid header")
	}
	size := binary.BigEndian.Uint32(header[8:12])
	if size > maxPayload {
		return errors.New("oversized payload")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	if err := decMode.Unmarshal(payload, value); err != nil {
		return err
	}
	canonical, err := encMode.Marshal(value)
	if err != nil || !bytes.Equal(canonical, payload) {
		return errors.New("noncanonical payload")
	}
	return nil
}

func validateName(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) && !bytes.ContainsRune([]byte(value), 0)
}

func validateRevision(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || len(value) > 512 || value[0] < '1' || value[0] > '9' {
		return false
	}
	for _, digit := range []byte(value[1:]) {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
