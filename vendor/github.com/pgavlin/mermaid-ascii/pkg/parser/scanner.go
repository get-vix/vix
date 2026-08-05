package parser

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scanner tokenizes Mermaid diagram input.
type Scanner struct {
	input  string
	pos    int // current byte offset
	line   int // current line (1-based)
	col    int // current column (1-based)
	peeked *Token
}

// ScannerPos captures the scanner's state for save/restore.
type ScannerPos struct {
	pos    int
	line   int
	col    int
	peeked *Token
}

// Save captures the current scanner state so it can be restored later.
func (s *Scanner) Save() ScannerPos {
	var peeked *Token
	if s.peeked != nil {
		copy := *s.peeked
		peeked = &copy
	}
	return ScannerPos{pos: s.pos, line: s.line, col: s.col, peeked: peeked}
}

// Restore returns the scanner to a previously saved state.
func (s *Scanner) Restore(saved ScannerPos) {
	s.pos = saved.pos
	s.line = saved.line
	s.col = saved.col
	s.peeked = saved.peeked
}

// NewScanner creates a new Scanner for the given input.
func NewScanner(input string) *Scanner {
	return &Scanner{
		input: input,
		pos:   0,
		line:  1,
		col:   1,
	}
}

// Peek returns the next token without consuming it.
func (s *Scanner) Peek() Token {
	if s.peeked != nil {
		return *s.peeked
	}
	tok := s.scan()
	s.peeked = &tok
	return tok
}

// Next returns the next token and advances the scanner.
func (s *Scanner) Next() Token {
	if s.peeked != nil {
		tok := *s.peeked
		s.peeked = nil
		return tok
	}
	return s.scan()
}

// Expect consumes the next token and returns an error if it's not the expected kind.
func (s *Scanner) Expect(kind TokenKind) (Token, error) {
	tok := s.Next()
	if tok.Kind != kind {
		return tok, Errorf(tok.Pos, "expected %s, got %s", kind, tok.Kind)
	}
	return tok, nil
}

// SkipWhitespace consumes and discards any whitespace tokens (not newlines).
func (s *Scanner) SkipWhitespace() {
	for s.Peek().Kind == TokenWhitespace {
		s.Next()
	}
}

// SkipNewlines consumes and discards any newline and whitespace tokens.
func (s *Scanner) SkipNewlines() {
	for {
		k := s.Peek().Kind
		if k == TokenNewline || k == TokenWhitespace {
			s.Next()
			continue
		}
		break
	}
}

// AtEnd returns true if the scanner is at the end of input.
func (s *Scanner) AtEnd() bool {
	return s.Peek().Kind == TokenEOF
}

// position returns the current Position.
func (s *Scanner) position() Position {
	return Position{Line: s.line, Column: s.col, Offset: s.pos}
}

// advance moves forward by one rune, updating line/column tracking.
func (s *Scanner) advance() rune {
	r, size := utf8.DecodeRuneInString(s.input[s.pos:])
	s.pos += size
	if r == '\n' {
		s.line++
		s.col = 1
	} else {
		s.col++
	}
	return r
}

// peek returns the current rune without advancing.
func (s *Scanner) peek() (rune, bool) {
	if s.pos >= len(s.input) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s.input[s.pos:])
	return r, true
}

// peekAt returns the rune at offset n bytes from current position.
func (s *Scanner) peekAt(n int) (rune, bool) {
	off := s.pos + n
	if off >= len(s.input) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s.input[off:])
	return r, true
}

// scan produces the next token.
func (s *Scanner) scan() Token {
	if s.pos >= len(s.input) {
		return Token{Kind: TokenEOF, Pos: s.position()}
	}

	pos := s.position()
	r, _ := s.peek()

	// Comments: %% to end of line
	if r == '%' {
		if r2, ok := s.peekAt(1); ok && r2 == '%' {
			s.skipToEndOfLine()
			// After skipping comment, return what follows (newline or EOF)
			return s.scan()
		}
	}

	// Newline
	if r == '\n' {
		s.advance()
		return Token{Kind: TokenNewline, Text: "\n", Pos: pos}
	}

	// Whitespace (spaces and tabs, not newlines)
	if r == ' ' || r == '\t' || r == '\r' {
		return s.scanWhitespace(pos)
	}

	// Quoted string
	if r == '"' {
		return s.scanString(pos)
	}

	// Number
	if r >= '0' && r <= '9' {
		return s.scanNumber(pos)
	}

	// Identifier
	if isIdentStart(r) {
		return s.scanIdent(pos)
	}

	// Operator characters: -, =, <, >, .  when part of arrow sequences
	if isOperatorStart(r) {
		return s.scanOperator(pos)
	}

	// Single-character punctuation
	s.advance()
	switch r {
	case '(':
		return Token{Kind: TokenLParen, Text: "(", Pos: pos}
	case ')':
		return Token{Kind: TokenRParen, Text: ")", Pos: pos}
	case '[':
		return Token{Kind: TokenLBracket, Text: "[", Pos: pos}
	case ']':
		return Token{Kind: TokenRBracket, Text: "]", Pos: pos}
	case '{':
		return Token{Kind: TokenLBrace, Text: "{", Pos: pos}
	case '}':
		return Token{Kind: TokenRBrace, Text: "}", Pos: pos}
	case ':':
		return Token{Kind: TokenColon, Text: ":", Pos: pos}
	case ',':
		return Token{Kind: TokenComma, Text: ",", Pos: pos}
	case '|':
		return Token{Kind: TokenPipe, Text: "|", Pos: pos}
	case '#':
		return Token{Kind: TokenHash, Text: "#", Pos: pos}
	case '@':
		return Token{Kind: TokenAt, Text: "@", Pos: pos}
	case '&':
		return Token{Kind: TokenAmpersand, Text: "&", Pos: pos}
	case ';':
		return Token{Kind: TokenSemicolon, Text: ";", Pos: pos}
	case '.':
		return Token{Kind: TokenDot, Text: ".", Pos: pos}
	}

	// Fallback: any unrecognized character
	return Token{Kind: TokenText, Text: string(r), Pos: pos}
}

func (s *Scanner) scanWhitespace(pos Position) Token {
	start := s.pos
	for {
		r, ok := s.peek()
		if !ok || (r != ' ' && r != '\t' && r != '\r') {
			break
		}
		s.advance()
	}
	return Token{Kind: TokenWhitespace, Text: s.input[start:s.pos], Pos: pos}
}

func (s *Scanner) scanString(pos Position) Token {
	s.advance() // consume opening quote
	var b strings.Builder
	for {
		r, ok := s.peek()
		if !ok {
			// Unterminated string — return what we have
			return Token{Kind: TokenString, Text: b.String(), Pos: pos}
		}
		if r == '\\' {
			s.advance()
			escaped, ok := s.peek()
			if !ok {
				break
			}
			s.advance()
			switch escaped {
			case '"':
				b.WriteRune('"')
			case '\\':
				b.WriteRune('\\')
			case 'n':
				b.WriteRune('\n')
			default:
				b.WriteRune('\\')
				b.WriteRune(escaped)
			}
			continue
		}
		if r == '"' {
			s.advance() // consume closing quote
			break
		}
		s.advance()
		b.WriteRune(r)
	}
	return Token{Kind: TokenString, Text: b.String(), Pos: pos}
}

func (s *Scanner) scanNumber(pos Position) Token {
	start := s.pos
	for {
		r, ok := s.peek()
		if !ok || (r < '0' || r > '9') {
			break
		}
		s.advance()
	}
	// Check for decimal point
	if r, ok := s.peek(); ok && r == '.' {
		if r2, ok := s.peekAt(1); ok && r2 >= '0' && r2 <= '9' {
			s.advance() // consume '.'
			for {
				r, ok := s.peek()
				if !ok || (r < '0' || r > '9') {
					break
				}
				s.advance()
			}
		}
	}
	return Token{Kind: TokenNumber, Text: s.input[start:s.pos], Pos: pos}
}

func (s *Scanner) scanIdent(pos Position) Token {
	start := s.pos
	for {
		r, ok := s.peek()
		if !ok || !isIdentContinue(r) {
			break
		}
		s.advance()
	}
	return Token{Kind: TokenIdent, Text: s.input[start:s.pos], Pos: pos}
}

// scanOperator greedily consumes operator-like characters to form arrows/operators.
// Operator chars: - = < > . x o (where x and o are only consumed when following - or =)
func (s *Scanner) scanOperator(pos Position) Token {
	start := s.pos
	for {
		r, ok := s.peek()
		if !ok {
			break
		}
		if r == '-' || r == '=' || r == '<' || r == '>' || r == '.' {
			s.advance()
			continue
		}
		// x, o, and ) are valid at the end of arrows (--x, --o, --)  )
		if (r == 'x' || r == 'o') && s.pos > start {
			// Only consume if preceded by operator chars
			s.advance()
			break // x and o are always terminal in arrows
		}
		if r == ')' && s.pos > start {
			// -) and --) async arrows
			s.advance()
			break
		}
		break
	}
	text := s.input[start:s.pos]
	if text == "." {
		// A lone dot is punctuation, not an operator
		return Token{Kind: TokenDot, Text: ".", Pos: pos}
	}
	return Token{Kind: TokenOperator, Text: text, Pos: pos}
}

// skipToEndOfLine advances past all characters until newline or EOF.
func (s *Scanner) skipToEndOfLine() {
	for {
		r, ok := s.peek()
		if !ok || r == '\n' {
			break
		}
		s.advance()
	}
}

func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentContinue(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isOperatorStart(r rune) bool {
	return r == '-' || r == '=' || r == '<' || r == '>'
}
