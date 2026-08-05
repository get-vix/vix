// Package requirement implements parsing and rendering of requirement diagrams
// in Mermaid syntax.
package requirement

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// requirementDiagramKeyword is the Mermaid keyword that identifies a requirement diagram.
const requirementDiagramKeyword = "requirementDiagram"

// Requirement represents a requirement node.
type Requirement struct {
	Name         string
	Type         string // "requirement", "functionalRequirement", etc.
	ID           string
	Text         string
	Risk         string
	VerifyMethod string
}

// ReqElement represents an element node.
type ReqElement struct {
	Name   string
	Type   string
	DocRef string
}

// ReqRelationship represents a relationship between requirements/elements.
type ReqRelationship struct {
	Source string
	Target string
	Type   string // "satisfies", "traces", "copies", "derives", "verifies", "refines", "contains"
}

// RequirementDiagram represents a parsed requirement diagram.
type RequirementDiagram struct {
	Requirements  []*Requirement
	Elements      []*ReqElement
	Relationships []*ReqRelationship
}

// IsRequirementDiagram returns true if the input starts with requirementDiagram keyword.
func IsRequirementDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, requirementDiagramKeyword)
	}
	return false
}

var requirementTypes = map[string]bool{
	"requirement":             true,
	"functionalRequirement":   true,
	"interfaceRequirement":    true,
	"performanceRequirement":  true,
	"physicalRequirement":     true,
	"designConstraint":        true,
}

var relationshipTypes = map[string]bool{
	"traces":   true,
	"copies":   true,
	"derives":  true,
	"satisfies": true,
	"verifies": true,
	"refines":  true,
	"contains": true,
}

// Parse parses a requirement diagram.
func Parse(input string) (*RequirementDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != requirementDiagramKeyword {
		return nil, fmt.Errorf("expected %q keyword", requirementDiagramKeyword)
	}
	s.Next()
	s.SkipNewlines()

	d := &RequirementDiagram{}

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		if tok.Kind == parser.TokenIdent {
			// Requirement block: reqType name {
			if requirementTypes[tok.Text] {
				parseRequirement(s, d)
				continue
			}

			// Element block: element name {
			if tok.Text == "element" {
				parseElement(s, d)
				continue
			}
		}

		// Try relationship: name - relType -> name2
		// Names can be quoted strings or idents
		if tok.Kind == parser.TokenIdent || tok.Kind == parser.TokenString {
			if tryParseRelationship(s, d) {
				continue
			}
		}

		parser.SkipToEndOfLine(s)
	}

	return d, nil
}

func parseRequirement(s *parser.Scanner, d *RequirementDiagram) {
	reqType := s.Next().Text // consume requirement type keyword
	s.SkipWhitespace()

	name := collectName(s)
	s.SkipWhitespace()

	// Expect '{'
	if s.Peek().Kind != parser.TokenLBrace {
		parser.SkipToEndOfLine(s)
		return
	}
	s.Next()
	s.SkipNewlines()

	req := &Requirement{Type: reqType, Name: name}

	// Parse key: value pairs until '}'
	for !s.AtEnd() {
		tok := s.Peek()
		if tok.Kind == parser.TokenRBrace {
			s.Next()
			break
		}
		if tok.Kind == parser.TokenIdent {
			key := s.Next().Text
			s.SkipWhitespace()
			if s.Peek().Kind == parser.TokenColon {
				s.Next() // consume ':'
				s.SkipWhitespace()
				value := strings.Trim(strings.TrimSpace(parser.ConsumeRestOfLine(s)), `"`)
				switch key {
				case "id":
					req.ID = value
				case "text":
					req.Text = value
				case "risk":
					req.Risk = value
				case "verifymethod":
					req.VerifyMethod = value
				}
			} else {
				parser.SkipToEndOfLine(s)
			}
		} else {
			parser.SkipToEndOfLine(s)
		}
		s.SkipNewlines()
	}

	d.Requirements = append(d.Requirements, req)
}

func parseElement(s *parser.Scanner, d *RequirementDiagram) {
	s.Next() // consume "element"
	s.SkipWhitespace()

	name := collectName(s)
	s.SkipWhitespace()

	// Expect '{'
	if s.Peek().Kind != parser.TokenLBrace {
		parser.SkipToEndOfLine(s)
		return
	}
	s.Next()
	s.SkipNewlines()

	elem := &ReqElement{Name: name}

	// Parse key: value pairs until '}'
	for !s.AtEnd() {
		tok := s.Peek()
		if tok.Kind == parser.TokenRBrace {
			s.Next()
			break
		}
		if tok.Kind == parser.TokenIdent {
			key := s.Next().Text
			s.SkipWhitespace()
			if s.Peek().Kind == parser.TokenColon {
				s.Next() // consume ':'
				s.SkipWhitespace()
				value := strings.Trim(strings.TrimSpace(parser.ConsumeRestOfLine(s)), `"`)
				switch key {
				case "type":
					elem.Type = value
				case "docref", "docRef":
					elem.DocRef = value
				}
			} else {
				parser.SkipToEndOfLine(s)
			}
		} else {
			parser.SkipToEndOfLine(s)
		}
		s.SkipNewlines()
	}

	d.Elements = append(d.Elements, elem)
}

// tryParseRelationship attempts: name - relType -> name2
func tryParseRelationship(s *parser.Scanner, d *RequirementDiagram) bool {
	saved := s.Save()

	// Source name (possibly quoted)
	source := collectName(s)
	if source == "" {
		s.Restore(saved)
		return false
	}
	s.SkipWhitespace()

	// Expect "-"
	if s.Peek().Kind != parser.TokenOperator || s.Peek().Text != "-" {
		s.Restore(saved)
		return false
	}
	s.Next()
	s.SkipWhitespace()

	// Expect relationship type
	relTok := s.Peek()
	if relTok.Kind != parser.TokenIdent || !relationshipTypes[relTok.Text] {
		s.Restore(saved)
		return false
	}
	relType := s.Next().Text
	s.SkipWhitespace()

	// Expect "->"
	if s.Peek().Kind != parser.TokenOperator || s.Peek().Text != "->" {
		s.Restore(saved)
		return false
	}
	s.Next()
	s.SkipWhitespace()

	// Target name
	target := collectName(s)
	if target == "" {
		s.Restore(saved)
		return false
	}

	d.Relationships = append(d.Relationships, &ReqRelationship{
		Source: source,
		Type:   relType,
		Target: target,
	})
	return true
}

// collectName collects a name which may be a quoted string or an identifier.
func collectName(s *parser.Scanner) string {
	tok := s.Peek()
	if tok.Kind == parser.TokenString {
		return s.Next().Text
	}
	if tok.Kind == parser.TokenIdent {
		return s.Next().Text
	}
	return ""
}
