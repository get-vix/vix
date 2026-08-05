package parser

import "strings"

// ParseQuotedOrIdent consumes either a quoted string token or an identifier token,
// returning the text content. Returns an error if neither is found.
func ParseQuotedOrIdent(s *Scanner) (string, error) {
	tok := s.Peek()
	switch tok.Kind {
	case TokenString:
		s.Next()
		return tok.Text, nil
	case TokenIdent:
		s.Next()
		return tok.Text, nil
	default:
		return "", Errorf(tok.Pos, "expected identifier or quoted string, got %s", tok.Kind)
	}
}

// ConsumeRestOfLine collects all text until newline or EOF, consuming the newline.
// Returns the collected text (trimmed).
func ConsumeRestOfLine(s *Scanner) string {
	var b strings.Builder
	for {
		tok := s.Peek()
		if tok.Kind == TokenNewline || tok.Kind == TokenEOF {
			if tok.Kind == TokenNewline {
				s.Next() // consume the newline
			}
			break
		}
		s.Next()
		b.WriteString(tok.Text)
	}
	return strings.TrimSpace(b.String())
}

// SkipToEndOfLine consumes tokens until newline or EOF, discarding them.
// The newline itself is also consumed.
func SkipToEndOfLine(s *Scanner) {
	for {
		tok := s.Peek()
		if tok.Kind == TokenNewline || tok.Kind == TokenEOF {
			if tok.Kind == TokenNewline {
				s.Next()
			}
			return
		}
		s.Next()
	}
}

// PeekIdent returns true if the next non-whitespace token is an identifier
// matching the given text (case-insensitive).
func PeekIdent(s *Scanner, text string) bool {
	tok := s.Peek()
	return tok.Kind == TokenIdent && strings.EqualFold(tok.Text, text)
}

// ConsumeIdent consumes an identifier token matching the given text (case-insensitive).
// Returns an error if the next token doesn't match.
func ConsumeIdent(s *Scanner, text string) error {
	tok := s.Next()
	if tok.Kind != TokenIdent || !strings.EqualFold(tok.Text, text) {
		return Errorf(tok.Pos, "expected %q, got %s(%q)", text, tok.Kind, tok.Text)
	}
	return nil
}

// CollectLineText collects the raw text of all tokens on the current line
// (up to newline or EOF) without consuming the newline.
func CollectLineText(s *Scanner) string {
	var b strings.Builder
	for {
		tok := s.Peek()
		if tok.Kind == TokenNewline || tok.Kind == TokenEOF {
			break
		}
		s.Next()
		b.WriteString(tok.Text)
	}
	return b.String()
}
