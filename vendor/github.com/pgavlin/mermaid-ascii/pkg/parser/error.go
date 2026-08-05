package parser

import "fmt"

// ParseError represents a parsing error with position information.
type ParseError struct {
	Pos     Position
	Message string
}

// Error implements the error interface.
func (e *ParseError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos, e.Message)
}

// Errorf creates a new ParseError with a formatted message.
func Errorf(pos Position, format string, args ...any) *ParseError {
	return &ParseError{
		Pos:     pos,
		Message: fmt.Sprintf(format, args...),
	}
}
