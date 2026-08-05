// Package statediagram parses and renders Mermaid state diagrams as ASCII/Unicode art.
package statediagram

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// stateDiagramKeyword is the Mermaid keyword that identifies a state diagram.
const stateDiagramKeyword = "stateDiagram-v2"

// State represents a state in the diagram.
type State struct {
	ID       string
	Label    string
	IsStart  bool
	IsEnd    bool
	Children []*State
	Index    int
}

// Transition represents a transition between states.
type Transition struct {
	From    *State
	To      *State
	Trigger string
}

// NotePosition represents where a note is placed.
type NotePosition int

const (
	// NoteRight places the note to the right of the state.
	NoteRight NotePosition = iota
	// NoteLeft places the note to the left of the state.
	NoteLeft
)

// Note represents a note attached to a state.
type Note struct {
	Position NotePosition
	State    *State
	Text     string
}

// StateDiagram represents a parsed state diagram.
type StateDiagram struct {
	States      []*State
	Transitions []*Transition
	Notes       []*Note
	stateMap    map[string]*State
}

// IsStateDiagram returns true if the input begins with the stateDiagram-v2 keyword.
func IsStateDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, stateDiagramKeyword)
	}
	return false
}

// Parse parses a state diagram from the given input string.
func Parse(input string) (*StateDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// Expect "stateDiagram-v2"
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent {
		return nil, fmt.Errorf("expected %q keyword", stateDiagramKeyword)
	}
	// The keyword is split into tokens: "stateDiagram" then "-" then "v2"
	// Collect the full keyword text
	keyword := collectKeyword(s)
	if keyword != stateDiagramKeyword {
		return nil, fmt.Errorf("expected %q keyword", stateDiagramKeyword)
	}
	s.SkipNewlines()

	sd := &StateDiagram{
		States:      []*State{},
		Transitions: []*Transition{},
		Notes:       []*Note{},
		stateMap:    make(map[string]*State),
	}

	if err := sd.parseStatements(s, false); err != nil {
		return nil, err
	}

	if len(sd.States) == 0 {
		return nil, fmt.Errorf("no states found")
	}

	return sd, nil
}

// collectKeyword reads "stateDiagram-v2" which tokenizes as Ident("stateDiagram") Operator("-") Ident("v2").
func collectKeyword(s *parser.Scanner) string {
	var b strings.Builder
	// First token must be ident
	tok := s.Next()
	b.WriteString(tok.Text)
	// Check for "-v2" suffix
	if s.Peek().Kind == parser.TokenOperator && s.Peek().Text == "-" {
		b.WriteString(s.Next().Text)
		if s.Peek().Kind == parser.TokenIdent {
			b.WriteString(s.Next().Text)
		}
	}
	return b.String()
}

func (sd *StateDiagram) parseStatements(s *parser.Scanner, inBlock bool) error {
	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		// Closing brace ends a composite state block
		if tok.Kind == parser.TokenRBrace {
			if inBlock {
				s.Next() // consume '}'
				return nil
			}
			// Unexpected '}' at top level — skip
			s.Next()
			continue
		}

		// Try to parse a statement
		if tok.Kind == parser.TokenIdent {
			switch tok.Text {
			case "state":
				if err := sd.parseState(s); err != nil {
					return err
				}
				continue
			case "note":
				if err := sd.parseNote(s); err != nil {
					return err
				}
				continue
			}
		}

		// Try to parse a transition: stateID --> stateID [: trigger]
		if tok.Kind == parser.TokenIdent || (tok.Kind == parser.TokenLBracket) {
			if sd.tryParseTransition(s) {
				continue
			}
		}

		// Skip unknown tokens to end of line
		parser.SkipToEndOfLine(s)
	}

	if inBlock {
		return fmt.Errorf("unexpected end of input, expected '}'")
	}
	return nil
}

// parseState handles: state "Label" as id { ... }, state "Label" as id, state id { ... }, state id
func (sd *StateDiagram) parseState(s *parser.Scanner) error {
	s.Next() // consume "state"
	s.SkipWhitespace()

	var label, id string

	tok := s.Peek()
	if tok.Kind == parser.TokenString {
		// state "Label" as id [{ ... }]
		label = s.Next().Text
		s.SkipWhitespace()
		// Expect "as"
		tok = s.Peek()
		if tok.Kind == parser.TokenIdent && tok.Text == "as" {
			s.Next() // consume "as"
			s.SkipWhitespace()
			idTok := s.Peek()
			if idTok.Kind == parser.TokenIdent {
				id = s.Next().Text
			} else {
				return parser.Errorf(idTok.Pos, "expected state identifier after 'as'")
			}
		} else {
			return parser.Errorf(tok.Pos, "expected 'as' after state label")
		}
	} else if tok.Kind == parser.TokenIdent {
		// state id [{ ... }]
		id = s.Next().Text
		label = id
	} else {
		return parser.Errorf(tok.Pos, "expected state name or label")
	}

	state := sd.getOrCreateState(id)
	state.Label = label

	s.SkipWhitespace()

	// Check for opening brace (composite state)
	if s.Peek().Kind == parser.TokenLBrace {
		s.Next() // consume '{'
		s.SkipNewlines()
		return sd.parseStatements(s, true)
	}

	return nil
}

// parseNote handles: note right/left of StateID : text
func (sd *StateDiagram) parseNote(s *parser.Scanner) error {
	s.Next() // consume "note"
	s.SkipWhitespace()

	// Expect "right" or "left"
	posTok := s.Peek()
	if posTok.Kind != parser.TokenIdent || (posTok.Text != "right" && posTok.Text != "left") {
		parser.SkipToEndOfLine(s)
		return nil
	}
	posStr := s.Next().Text
	s.SkipWhitespace()

	// Expect "of"
	ofTok := s.Peek()
	if ofTok.Kind != parser.TokenIdent || ofTok.Text != "of" {
		parser.SkipToEndOfLine(s)
		return nil
	}
	s.Next() // consume "of"
	s.SkipWhitespace()

	// Expect state ID
	stateIDTok := s.Peek()
	if stateIDTok.Kind != parser.TokenIdent {
		parser.SkipToEndOfLine(s)
		return nil
	}
	stateID := s.Next().Text
	s.SkipWhitespace()

	// Expect ":"
	if s.Peek().Kind != parser.TokenColon {
		parser.SkipToEndOfLine(s)
		return nil
	}
	s.Next() // consume ":"
	s.SkipWhitespace()

	// Rest of line is the note text
	text := strings.TrimSpace(parser.ConsumeRestOfLine(s))

	state := sd.getOrCreateState(stateID)
	pos := NoteRight
	if posStr == "left" {
		pos = NoteLeft
	}
	sd.Notes = append(sd.Notes, &Note{
		Position: pos,
		State:    state,
		Text:     text,
	})
	return nil
}

// tryParseTransition attempts to parse: stateID --> stateID [: trigger]
// Returns true if successful.
func (sd *StateDiagram) tryParseTransition(s *parser.Scanner) bool {
	saved := s.Save()

	fromID := sd.parseStateID(s)
	if fromID == "" {
		s.Restore(saved)
		return false
	}
	s.SkipWhitespace()

	// Expect "-->"
	tok := s.Peek()
	if tok.Kind != parser.TokenOperator || tok.Text != "-->" {
		s.Restore(saved)
		return false
	}
	s.Next() // consume "-->"
	s.SkipWhitespace()

	toID := sd.parseStateID(s)
	if toID == "" {
		s.Restore(saved)
		return false
	}
	s.SkipWhitespace()

	// Optional ": trigger"
	var trigger string
	if s.Peek().Kind == parser.TokenColon {
		s.Next() // consume ":"
		s.SkipWhitespace()
		trigger = strings.TrimSpace(parser.ConsumeRestOfLine(s))
	}

	from := sd.getOrCreateState(fromID)
	to := sd.getOrCreateState(toID)
	sd.Transitions = append(sd.Transitions, &Transition{
		From:    from,
		To:      to,
		Trigger: trigger,
	})
	return true
}

// parseStateID parses a state identifier: either a bare ident or "[*]".
func (sd *StateDiagram) parseStateID(s *parser.Scanner) string {
	tok := s.Peek()
	if tok.Kind == parser.TokenIdent {
		return s.Next().Text
	}
	// Check for [*]
	if tok.Kind == parser.TokenLBracket {
		saved := s.Save()
		s.Next() // consume '['
		next := s.Peek()
		if next.Kind == parser.TokenOperator || next.Kind == parser.TokenText {
			// Could be '*' which tokenizes as Text
			if strings.Contains(next.Text, "*") {
				s.Next() // consume '*'
				if s.Peek().Kind == parser.TokenRBracket {
					s.Next() // consume ']'
					return "[*]"
				}
			}
		}
		s.Restore(saved)
	}
	return ""
}

func (sd *StateDiagram) getOrCreateState(id string) *State {
	if s, ok := sd.stateMap[id]; ok {
		return s
	}

	isStart := false
	isEnd := false
	label := id

	// [*] is the start/end pseudo-state
	if id == "[*]" {
		isStart = true
		isEnd = true
		label = "[*]"
	}

	s := &State{
		ID:      id,
		Label:   label,
		IsStart: isStart,
		IsEnd:   isEnd,
		Index:   len(sd.States),
	}
	sd.States = append(sd.States, s)
	sd.stateMap[id] = s
	return s
}
