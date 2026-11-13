// Package dazsecrets invokes explicitly configured secret-provider executables.
package dazsecrets

import (
	"errors"
)

// ErrorCode is a stable provider failure classification.
type ErrorCode string

const (
	CodeNotFound    ErrorCode = "NOT_FOUND"
	CodeInvalid     ErrorCode = "INVALID"
	CodeConflict    ErrorCode = "CONFLICT"
	CodeDenied      ErrorCode = "DENIED"
	CodeUnavailable ErrorCode = "UNAVAILABLE"
	CodeDeadline    ErrorCode = "DEADLINE"
	CodeCorrupt     ErrorCode = "CORRUPT"
	CodeUnsupported ErrorCode = "UNSUPPORTED"
	CodeInternal    ErrorCode = "INTERNAL"
)

var validCodes = map[ErrorCode]struct{}{
	CodeNotFound: {}, CodeInvalid: {}, CodeConflict: {}, CodeDenied: {},
	CodeUnavailable: {}, CodeDeadline: {}, CodeCorrupt: {},
	CodeUnsupported: {}, CodeInternal: {},
}

// Error reports a redacted, typed failure. It never contains provider stderr.
type Error struct {
	Code ErrorCode
}

// Error returns the stable error code.
func (e *Error) Error() string { return "daz-secrets: " + string(e.Code) }

// IsCode reports whether err has the given stable code.
func IsCode(err error, code ErrorCode) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}

func typedError(code ErrorCode) error {
	if _, ok := validCodes[code]; !ok {
		return &Error{Code: CodeCorrupt}
	}
	return &Error{Code: code}
}

func wrapCode(code ErrorCode, _ error) error {
	return &Error{Code: code}
}

// Info describes the selected provider protocol.
type Info struct {
	ProviderID string `json:"provider_id"`
	Major      uint16 `json:"major"`
	Minor      uint16 `json:"minor"`
}

// Secret is an exact byte value and its opaque revision token.
type Secret struct {
	Value    []byte
	Revision string
}

// Mutation reports the new revision after a set.
type Mutation struct {
	Revision string `json:"revision"`
}

// Deletion reports whether an item was deleted.
type Deletion struct {
	Deleted bool `json:"deleted"`
}

// Metadata identifies an item without exposing its value.
type Metadata struct {
	Service  string `cbor:"service" json:"service"`
	Account  string `cbor:"account" json:"account"`
	Revision string `cbor:"revision" json:"revision"`
}
