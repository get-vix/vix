// Package architecture implements parsing and rendering of architecture-beta
// diagrams in Mermaid syntax.
package architecture

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// architectureBetaKeyword is the Mermaid keyword that identifies an architecture diagram.
const architectureBetaKeyword = "architecture-beta"

// Service represents a service/node in the architecture diagram.
type Service struct {
	ID    string
	Icon  string
	Label string
}

// Group represents a group of services.
type Group struct {
	ID       string
	Icon     string
	Label    string
	Services []*Service
	Groups   []*Group
}

// Connection represents a connection between two services.
type Connection struct {
	From     string
	FromEdge string
	To       string
	ToEdge   string
	Directed bool // true for -->, false for --
}

// ArchitectureDiagram represents a parsed architecture diagram.
type ArchitectureDiagram struct {
	Services    []*Service
	Groups      []*Group
	Connections []*Connection
}

// IsArchitectureDiagram returns true if the input starts with architecture-beta keyword.
func IsArchitectureDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, architectureBetaKeyword)
	}
	return false
}

// Parse parses an architecture diagram.
func Parse(input string) (*ArchitectureDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// "architecture-beta" tokenizes as Ident("architecture") Operator("-") Ident("beta")
	keyword := collectKeyword(s)
	if keyword != architectureBetaKeyword {
		return nil, fmt.Errorf("expected %q keyword", architectureBetaKeyword)
	}
	s.SkipNewlines()

	d := &ArchitectureDiagram{}

	if err := parseStatements(s, d, nil); err != nil {
		return nil, err
	}

	return d, nil
}

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

func parseStatements(s *parser.Scanner, d *ArchitectureDiagram, currentGroup *Group) error {
	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		// End of group: '}'
		if tok.Kind == parser.TokenRBrace {
			s.Next()
			return nil
		}

		if tok.Kind != parser.TokenIdent {
			parser.SkipToEndOfLine(s)
			continue
		}

		switch tok.Text {
		case "group":
			if err := parseGroup(s, d, currentGroup); err != nil {
				return err
			}
			continue
		case "service":
			parseService(s, d, currentGroup)
			continue
		case "junction":
			parseJunction(s, d, currentGroup)
			continue
		}

		// Try connection: name[:edge] --> name[:edge]
		if tryParseConnection(s, d) {
			continue
		}

		parser.SkipToEndOfLine(s)
	}
	return nil
}

// parseGroup handles: group name(icon)[label] {
func parseGroup(s *parser.Scanner, d *ArchitectureDiagram, currentGroup *Group) error {
	s.Next() // consume "group"
	s.SkipWhitespace()

	id, icon, label := parseServiceIDIconLabel(s)
	if id == "" {
		parser.SkipToEndOfLine(s)
		return nil
	}

	g := &Group{ID: id, Icon: icon, Label: label}
	if g.Label == "" {
		g.Label = g.ID
	}

	s.SkipWhitespace()
	if s.Peek().Kind == parser.TokenLBrace {
		s.Next()
	}
	s.SkipNewlines()

	if err := parseStatements(s, d, g); err != nil {
		return err
	}

	if currentGroup != nil {
		currentGroup.Groups = append(currentGroup.Groups, g)
	} else {
		d.Groups = append(d.Groups, g)
	}
	return nil
}

// parseService handles: service name(icon)[label]
func parseService(s *parser.Scanner, d *ArchitectureDiagram, currentGroup *Group) {
	s.Next() // consume "service"
	s.SkipWhitespace()

	id, icon, label := parseServiceIDIconLabel(s)
	if id == "" {
		parser.SkipToEndOfLine(s)
		return
	}

	svc := &Service{ID: id, Icon: icon, Label: label}
	if svc.Label == "" {
		svc.Label = svc.ID
	}

	if currentGroup != nil {
		currentGroup.Services = append(currentGroup.Services, svc)
	} else {
		d.Services = append(d.Services, svc)
	}
}

// parseJunction handles: junction name
func parseJunction(s *parser.Scanner, d *ArchitectureDiagram, currentGroup *Group) {
	s.Next() // consume "junction"
	s.SkipWhitespace()

	nameTok := s.Peek()
	if nameTok.Kind != parser.TokenIdent {
		parser.SkipToEndOfLine(s)
		return
	}
	name := s.Next().Text

	svc := &Service{ID: name, Label: name}
	if currentGroup != nil {
		currentGroup.Services = append(currentGroup.Services, svc)
	} else {
		d.Services = append(d.Services, svc)
	}
}

// parseServiceIDIconLabel parses: name(icon)[label]
func parseServiceIDIconLabel(s *parser.Scanner) (id, icon, label string) {
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent {
		return "", "", ""
	}
	id = s.Next().Text

	// Optional (icon)
	if s.Peek().Kind == parser.TokenLParen {
		s.Next() // consume '('
		var parts []string
		for !s.AtEnd() {
			t := s.Peek()
			if t.Kind == parser.TokenRParen {
				s.Next()
				break
			}
			if t.Kind == parser.TokenNewline || t.Kind == parser.TokenEOF {
				break
			}
			s.Next()
			parts = append(parts, t.Text)
		}
		icon = strings.Join(parts, "")
	}

	// Optional [label]
	if s.Peek().Kind == parser.TokenLBracket {
		s.Next() // consume '['
		var parts []string
		for !s.AtEnd() {
			t := s.Peek()
			if t.Kind == parser.TokenRBracket {
				s.Next()
				break
			}
			if t.Kind == parser.TokenNewline || t.Kind == parser.TokenEOF {
				break
			}
			s.Next()
			parts = append(parts, t.Text)
		}
		label = strings.Join(parts, "")
	}

	return id, icon, label
}

// tryParseConnection attempts: name[:edge] --> name[:edge] or name[:edge] -- name[:edge]
func tryParseConnection(s *parser.Scanner, d *ArchitectureDiagram) bool {
	saved := s.Save()

	fromTok := s.Peek()
	if fromTok.Kind != parser.TokenIdent {
		return false
	}
	from := s.Next().Text

	// Optional :edge
	fromEdge := ""
	if s.Peek().Kind == parser.TokenColon {
		s.Next()
		if s.Peek().Kind == parser.TokenIdent {
			fromEdge = s.Next().Text
		}
	}
	s.SkipWhitespace()

	// Expect "-->" or "--"
	opTok := s.Peek()
	if opTok.Kind != parser.TokenOperator {
		s.Restore(saved)
		return false
	}
	arrow := opTok.Text
	if arrow != "-->" && arrow != "--" {
		s.Restore(saved)
		return false
	}
	s.Next()
	s.SkipWhitespace()

	toTok := s.Peek()
	if toTok.Kind != parser.TokenIdent {
		s.Restore(saved)
		return false
	}
	to := s.Next().Text

	// Optional :edge
	toEdge := ""
	if s.Peek().Kind == parser.TokenColon {
		s.Next()
		if s.Peek().Kind == parser.TokenIdent {
			toEdge = s.Next().Text
		}
	}

	d.Connections = append(d.Connections, &Connection{
		From:     from,
		FromEdge: fromEdge,
		To:       to,
		ToEdge:   toEdge,
		Directed: arrow == "-->",
	})
	return true
}
