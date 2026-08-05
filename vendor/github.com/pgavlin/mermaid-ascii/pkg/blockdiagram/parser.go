// Package blockdiagram implements parsing and rendering of block-beta diagrams
// in Mermaid syntax.
package blockdiagram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// blockBetaKeyword is the Mermaid keyword that identifies a block-beta diagram.
const blockBetaKeyword = "block-beta"

// Block represents a single block in the diagram.
type Block struct {
	ID       string
	Label    string
	Children []*Block
	Columns  int  // number of columns for this container block
	Span     int  // how many columns this block spans
	IsSpace  bool // true for spacer blocks that render as empty space
}

// BlockDiagram represents a parsed block diagram.
type BlockDiagram struct {
	Columns int
	Blocks  []*Block
}

// IsBlockDiagram returns true if the input starts with block-beta keyword.
func IsBlockDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, blockBetaKeyword)
	}
	return false
}

// Parse parses a block diagram.
func Parse(input string) (*BlockDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// Expect "block-beta" which tokenizes as Ident("block") Operator("-") Ident("beta")
	keyword := collectKeyword(s)
	if keyword != blockBetaKeyword {
		return nil, fmt.Errorf("expected %q keyword", blockBetaKeyword)
	}
	s.SkipNewlines()

	d := &BlockDiagram{Columns: 1}

	if err := parseStatements(s, d, nil); err != nil {
		return nil, err
	}

	return d, nil
}

// collectKeyword reads "block-beta" which tokenizes as Ident("block") Operator("-") Ident("beta").
func collectKeyword(s *parser.Scanner) string {
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent {
		return ""
	}
	var b strings.Builder
	b.WriteString(s.Next().Text)
	if s.Peek().Kind == parser.TokenOperator && s.Peek().Text == "-" {
		b.WriteString(s.Next().Text)
		if s.Peek().Kind == parser.TokenIdent {
			b.WriteString(s.Next().Text)
		}
	}
	return b.String()
}

func parseStatements(s *parser.Scanner, d *BlockDiagram, parent *Block) error {
	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		// "end" keyword — close block
		if tok.Kind == parser.TokenIdent && tok.Text == "end" {
			saved := s.Save()
			s.Next()
			// Make sure "end" is alone on the line
			s.SkipWhitespace()
			next := s.Peek()
			if next.Kind == parser.TokenNewline || next.Kind == parser.TokenEOF {
				return nil
			}
			// Not alone — restore and parse as block name
			s.Restore(saved)
		}

		// "columns N"
		if tok.Kind == parser.TokenIdent && tok.Text == "columns" {
			s.Next()
			s.SkipWhitespace()
			numTok := s.Peek()
			if numTok.Kind == parser.TokenNumber {
				cols, _ := strconv.Atoi(s.Next().Text)
				if parent != nil {
					parent.Columns = cols
				} else {
					d.Columns = cols
				}
			}
			parser.SkipToEndOfLine(s)
			continue
		}

		// "block" keyword — start nested block
		if tok.Kind == parser.TokenIdent && tok.Text == "block" {
			saved := s.Save()
			s.Next()
			s.SkipWhitespace()

			id := ""
			// Optional ":id"
			if s.Peek().Kind == parser.TokenColon {
				s.Next()
				if s.Peek().Kind == parser.TokenIdent {
					id = s.Next().Text
				}
			}

			// Check this is end of line
			s.SkipWhitespace()
			next := s.Peek()
			if next.Kind == parser.TokenNewline || next.Kind == parser.TokenEOF {
				b := &Block{
					ID:      id,
					Label:   id,
					Columns: 1,
					Span:    1,
				}
				if b.ID == "" {
					b.ID = fmt.Sprintf("block_%d", len(d.Blocks))
					b.Label = ""
				}
				s.SkipNewlines()
				if err := parseStatements(s, d, b); err != nil {
					return err
				}
				addBlock(d, parent, b)
				continue
			}
			// Not a block statement — restore and parse as regular content
			s.Restore(saved)
		}

		// Parse blocks on the line (potentially multiple, with edge arrows between them)
		parseLine(s, d, parent)
	}
	return nil
}

// parseLine parses one line of block content, handling edges and multiple blocks.
func parseLine(s *parser.Scanner, d *BlockDiagram, parent *Block) {
	for !s.AtEnd() {
		s.SkipWhitespace()
		tok := s.Peek()
		if tok.Kind == parser.TokenNewline || tok.Kind == parser.TokenEOF {
			break
		}

		// Skip edge arrows: -->, -- "label" -->
		if tok.Kind == parser.TokenOperator {
			text := tok.Text
			if strings.Contains(text, "->") || text == "--" {
				s.Next()
				s.SkipWhitespace()
				// If it was "--", skip optional label and then "-->"
				if text == "--" {
					if s.Peek().Kind == parser.TokenString {
						s.Next() // skip label
						s.SkipWhitespace()
					}
					if s.Peek().Kind == parser.TokenOperator {
						s.Next() // skip "-->"
					}
				}
				continue
			}
		}

		// Try to parse a block token
		if b := parseBlockToken(s); b != nil {
			addBlock(d, parent, b)
		} else {
			// Skip unrecognized token
			s.Next()
		}
	}
}

// parseBlockToken parses a single block: ID[shape], space[:N], etc.
func parseBlockToken(s *parser.Scanner) *Block {
	tok := s.Peek()

	// "space" keyword (with optional :N span)
	if tok.Kind == parser.TokenIdent && tok.Text == "space" {
		s.Next()
		span := 1
		if s.Peek().Kind == parser.TokenColon {
			s.Next()
			if s.Peek().Kind == parser.TokenNumber {
				span, _ = strconv.Atoi(s.Next().Text)
			}
		}
		return &Block{
			ID:      "space",
			Label:   "",
			Span:    span,
			IsSpace: true,
		}
	}

	// Regular block: ID followed by optional shape and optional :span
	if tok.Kind != parser.TokenIdent {
		return nil
	}
	id := s.Next().Text
	label := id
	span := 1

	// Check for shape: ["text"], ("text"), [["text"]], (["text"]), [("text")], (("text"))
	next := s.Peek()
	if next.Kind == parser.TokenLBracket || next.Kind == parser.TokenLParen {
		if parsed, text := parseShapeLabel(s); parsed {
			label = text
		}
	}

	// Check for span: :N
	if s.Peek().Kind == parser.TokenColon {
		saved := s.Save()
		s.Next()
		if s.Peek().Kind == parser.TokenNumber {
			span, _ = strconv.Atoi(s.Next().Text)
		} else {
			s.Restore(saved)
		}
	}

	return &Block{
		ID:    id,
		Label: label,
		Span:  span,
	}
}

// parseShapeLabel parses shape syntax like ["text"], ("text"), [["text"]], etc.
// Returns (true, label) if parsed, (false, "") otherwise.
func parseShapeLabel(s *parser.Scanner) (bool, string) {
	saved := s.Save()
	open := s.Next() // consume '[' or '('

	// Check for nested brackets: [[ or [( or ([
	inner := s.Peek()
	if (open.Kind == parser.TokenLBracket && (inner.Kind == parser.TokenLBracket || inner.Kind == parser.TokenLParen)) ||
		(open.Kind == parser.TokenLParen && (inner.Kind == parser.TokenLBracket || inner.Kind == parser.TokenLParen)) {
		s.Next() // consume inner bracket
		// Expect string
		if s.Peek().Kind == parser.TokenString {
			text := s.Next().Text
			// Consume two closing brackets
			closers := 0
			for closers < 2 {
				c := s.Peek()
				if c.Kind == parser.TokenRBracket || c.Kind == parser.TokenRParen {
					s.Next()
					closers++
				} else {
					break
				}
			}
			if closers == 2 {
				return true, text
			}
		}
		s.Restore(saved)
		return false, ""
	}

	// Simple: ["text"] or ("text")
	if s.Peek().Kind == parser.TokenString {
		text := s.Next().Text
		closer := s.Peek()
		if closer.Kind == parser.TokenRBracket || closer.Kind == parser.TokenRParen {
			s.Next()
			return true, text
		}
	}

	s.Restore(saved)
	return false, ""
}

// tokenizeBlockLine splits a line into block tokens, respecting bracket syntax.
// E.g., `A["text with spaces"] B` → ["A[\"text with spaces\"]", "B"]
func tokenizeBlockLine(line string) []string {
	var tokens []string
	var current strings.Builder
	depth := 0 // track bracket/paren nesting
	inQuote := false

	for i := 0; i < len(line); i++ {
		ch := line[i]
		if ch == '"' {
			inQuote = !inQuote
			current.WriteByte(ch)
			continue
		}
		if inQuote {
			current.WriteByte(ch)
			continue
		}
		if ch == '[' || ch == '(' {
			depth++
			current.WriteByte(ch)
			continue
		}
		if ch == ']' || ch == ')' {
			depth--
			current.WriteByte(ch)
			continue
		}
		if ch == ' ' || ch == '\t' {
			if depth == 0 {
				if current.Len() > 0 {
					tokens = append(tokens, current.String())
					current.Reset()
				}
				continue
			}
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// addBlock adds a block to the parent or diagram top-level.
func addBlock(d *BlockDiagram, parent *Block, b *Block) {
	if parent != nil {
		parent.Children = append(parent.Children, b)
	} else {
		d.Blocks = append(d.Blocks, b)
	}
}
