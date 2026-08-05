package parser

import "fmt"

// TokenKind represents the type of a lexical token.
type TokenKind int

const (
	TokenEOF        TokenKind = iota
	TokenNewline              // \n
	TokenWhitespace           // spaces/tabs
	TokenIdent                // [A-Za-z_][A-Za-z0-9_]*
	TokenNumber               // integer or float
	TokenString               // "..." with quotes stripped from Text
	TokenLParen               // (
	TokenRParen               // )
	TokenLBracket             // [
	TokenRBracket             // ]
	TokenLBrace               // {
	TokenRBrace               // }
	TokenColon                // :
	TokenComma                // ,
	TokenPipe                 // |
	TokenHash                 // #
	TokenAt                   // @
	TokenAmpersand            // &
	TokenSemicolon            // ;
	TokenDot                  // .
	TokenOperator             // -->, ->>, -.-> etc. — raw text preserved, parsers interpret
	TokenText                 // fallback for unrecognized characters
)

var tokenKindNames = map[TokenKind]string{
	TokenEOF:        "EOF",
	TokenNewline:    "Newline",
	TokenWhitespace: "Whitespace",
	TokenIdent:      "Ident",
	TokenNumber:     "Number",
	TokenString:     "String",
	TokenLParen:     "LParen",
	TokenRParen:     "RParen",
	TokenLBracket:   "LBracket",
	TokenRBracket:   "RBracket",
	TokenLBrace:     "LBrace",
	TokenRBrace:     "RBrace",
	TokenColon:      "Colon",
	TokenComma:      "Comma",
	TokenPipe:       "Pipe",
	TokenHash:       "Hash",
	TokenAt:         "At",
	TokenAmpersand:  "Ampersand",
	TokenSemicolon:  "Semicolon",
	TokenDot:        "Dot",
	TokenOperator:   "Operator",
	TokenText:       "Text",
}

// String returns the name of a TokenKind.
func (k TokenKind) String() string {
	if name, ok := tokenKindNames[k]; ok {
		return name
	}
	return fmt.Sprintf("TokenKind(%d)", int(k))
}

// Position represents a source location.
type Position struct {
	Line   int // 1-based line number
	Column int // 1-based column number
	Offset int // 0-based byte offset
}

// String returns a human-readable position string.
func (p Position) String() string {
	return fmt.Sprintf("line %d, col %d", p.Line, p.Column)
}

// Token represents a single lexical token.
type Token struct {
	Kind TokenKind
	Text string
	Pos  Position
}

// String returns a debug representation of a token.
func (t Token) String() string {
	if t.Kind == TokenEOF {
		return "EOF"
	}
	return fmt.Sprintf("%s(%q)", t.Kind, t.Text)
}
