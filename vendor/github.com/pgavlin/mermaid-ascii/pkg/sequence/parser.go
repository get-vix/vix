package sequence

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

const (
	// SequenceDiagramKeyword is the keyword that identifies a sequence diagram in Mermaid syntax.
	SequenceDiagramKeyword = "sequenceDiagram"
	// SolidArrowSyntax is the Mermaid syntax for a solid arrow with filled arrowhead (->>).
	SolidArrowSyntax = "->>"
	// DottedArrowSyntax is the Mermaid syntax for a dotted arrow with filled arrowhead (-->>).
	DottedArrowSyntax = "-->>"
)

// ParticipantGroup represents a box grouping of participants.
type ParticipantGroup struct {
	Label        string
	Participants []*Participant
}

// CreateEvent represents a create participant directive (participant box appears mid-diagram).
type CreateEvent struct {
	Participant *Participant
}

func (c *CreateEvent) elementType() string { return "create" }

// DestroyEvent represents a destroy directive (lifeline ends with X).
type DestroyEvent struct {
	Participant *Participant
}

func (d *DestroyEvent) elementType() string { return "destroy" }

// SequenceDiagram represents a parsed sequence diagram.
type SequenceDiagram struct {
	Participants []*Participant
	Messages     []*Message
	Autonumber   bool
	Elements     []Element // Ordered list of all elements (messages, notes, activations, blocks)
	Groups       []*ParticipantGroup
}

// Element is an interface for things that appear in the sequence diagram's vertical ordering.
type Element interface {
	elementType() string
}

// ParticipantType indicates how a participant is rendered in the diagram.
type ParticipantType int

const (
	// ParticipantBox renders the participant as a rectangular box.
	ParticipantBox ParticipantType = iota
	// ParticipantActor renders the participant as a stick figure.
	ParticipantActor
)

// Participant represents a participant (actor or box) in a sequence diagram.
type Participant struct {
	ID    string
	Label string
	Index int
	Type  ParticipantType
}

// Message represents a message arrow between two participants.
type Message struct {
	From       *Participant
	To         *Participant
	Label      string
	ArrowType  ArrowType
	Number     int  // Message number when autonumber is enabled (0 means no number)
	Activate   bool // +  shorthand: activate target after message
	Deactivate bool // - shorthand: deactivate source after message
}

func (m *Message) elementType() string { return "message" }

// ArrowType represents the style of arrow used for a message.
type ArrowType int

const (
	SolidArrow  ArrowType = iota // ->>  solid with filled arrowhead
	DottedArrow                  // -->> dotted with filled arrowhead
	SolidOpen                    // ->   solid with open arrowhead
	DottedOpen                   // -->  dotted with open arrowhead
	SolidCross                   // -x   solid with cross end
	DottedCross                  // --x  dotted with cross end
	SolidAsync                   // -)   solid with open arrow (async)
	DottedAsync                  // --)  dotted with open arrow (async)
)

// String returns a human-readable name for the ArrowType.
func (a ArrowType) String() string {
	switch a {
	case SolidArrow:
		return "solid"
	case DottedArrow:
		return "dotted"
	case SolidOpen:
		return "solid_open"
	case DottedOpen:
		return "dotted_open"
	case SolidCross:
		return "solid_cross"
	case DottedCross:
		return "dotted_cross"
	case SolidAsync:
		return "solid_async"
	case DottedAsync:
		return "dotted_async"
	default:
		return fmt.Sprintf("ArrowType(%d)", a)
	}
}

// IsDotted returns true if the arrow type uses a dotted line style.
func (a ArrowType) IsDotted() bool {
	return a == DottedArrow || a == DottedOpen || a == DottedCross || a == DottedAsync
}

// NotePosition indicates where a note is placed.
type NotePosition int

const (
	// NoteRightOf places the note to the right of a participant.
	NoteRightOf NotePosition = iota
	// NoteLeftOf places the note to the left of a participant.
	NoteLeftOf
	// NoteOver places the note over one or two participants.
	NoteOver
)

// Note represents a note in the sequence diagram.
type Note struct {
	Position       NotePosition
	Participant    *Participant
	EndParticipant *Participant // non-nil for "Note over A,B"
	Text           string
}

func (n *Note) elementType() string { return "note" }

// ActivationEvent represents an activate/deactivate directive.
type ActivationEvent struct {
	Participant *Participant
	Activate    bool // true = activate, false = deactivate
}

func (a *ActivationEvent) elementType() string { return "activation" }

// Block represents an interaction block (loop, alt, opt, par, critical, break, rect).
type Block struct {
	Type     string // "loop", "alt", "opt", "par", "critical", "break", "rect"
	Label    string
	Elements []Element
	Sections []*BlockSection // For alt/par/critical: additional sections (else/and/option)
}

func (b *Block) elementType() string { return "block" }

// BlockSection represents an else/and/option section within a block.
type BlockSection struct {
	Label    string
	Elements []Element
}

// IsSequenceDiagram returns true if the input text begins with the sequenceDiagram keyword.
func IsSequenceDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, SequenceDiagramKeyword)
	}
	return false
}

// seqParser implements a recursive descent parser for Mermaid sequence diagram syntax.
type seqParser struct {
	s              *parser.Scanner
	sd             *SequenceDiagram
	participantMap map[string]*Participant
}

// Known arrow types ordered by length (longest first for prefix matching).
var seqArrowOrder = []string{"-->>", "->>", "-->", "->", "--x", "-x", "--)", "-)"}

var seqArrowTypes = map[string]ArrowType{
	"-->>": DottedArrow,
	"->>":  SolidArrow,
	"-->":  DottedOpen,
	"->":   SolidOpen,
	"--x":  DottedCross,
	"-x":   SolidCross,
	"--)":  DottedAsync,
	"-)":   SolidAsync,
}

// isEndAlone checks if "end" is alone on its line.
func (p *seqParser) isEndAlone() bool {
	tok := p.s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != "end" {
		return false
	}
	saved := p.s.Save()
	p.s.Next()
	p.s.SkipWhitespace()
	next := p.s.Peek()
	p.s.Restore(saved)
	return next.Kind == parser.TokenNewline || next.Kind == parser.TokenEOF
}

// parseParticipantID parses a participant identifier (ident, number, or quoted string).
func (p *seqParser) parseParticipantID() (string, bool) {
	tok := p.s.Peek()
	switch tok.Kind {
	case parser.TokenString, parser.TokenIdent, parser.TokenNumber:
		p.s.Next()
		return tok.Text, true
	default:
		return "", false
	}
}

// parseArrow tries to parse a sequence diagram arrow from the current operator token.
// Returns (arrowType, modifier, true) if successful.
// The modifier is extracted from the operator text if it's embedded (e.g., "->>-").
func (p *seqParser) parseArrow() (ArrowType, string, bool) {
	tok := p.s.Peek()
	if tok.Kind != parser.TokenOperator {
		return 0, "", false
	}

	for _, arrow := range seqArrowOrder {
		if strings.HasPrefix(tok.Text, arrow) {
			p.s.Next()
			modifier := tok.Text[len(arrow):]
			return seqArrowTypes[arrow], modifier, true
		}
	}

	return 0, "", false
}

// parseModifier checks for a +/- activation modifier token.
func (p *seqParser) parseModifier() string {
	tok := p.s.Peek()
	if tok.Kind == parser.TokenText && tok.Text == "+" {
		p.s.Next()
		return "+"
	}
	if tok.Kind == parser.TokenOperator && tok.Text == "-" {
		p.s.Next()
		return "-"
	}
	return ""
}

// getParticipant returns an existing participant or auto-creates one.
func (p *seqParser) getParticipant(id string) *Participant {
	if pt, exists := p.participantMap[id]; exists {
		return pt
	}
	pt := &Participant{
		ID:    id,
		Label: id,
		Index: len(p.sd.Participants),
	}
	p.sd.Participants = append(p.sd.Participants, pt)
	p.participantMap[id] = pt
	return pt
}

// addParticipant adds a new participant, returning an error if duplicate.
func (p *seqParser) addParticipant(id, label string, pType ParticipantType) (*Participant, error) {
	if _, exists := p.participantMap[id]; exists {
		return nil, fmt.Errorf("line %d: duplicate participant %q", p.s.Peek().Pos.Line, id)
	}
	pt := &Participant{
		ID:    id,
		Label: label,
		Index: len(p.sd.Participants),
		Type:  pType,
	}
	p.sd.Participants = append(p.sd.Participants, pt)
	p.participantMap[id] = pt
	return pt, nil
}

// parseParticipantDecl parses: (participant|actor) ID [as Label]
func (p *seqParser) parseParticipantDecl(currentGroup *ParticipantGroup) error {
	keyword := p.s.Next().Text // "participant" or "actor"
	pType := ParticipantBox
	if keyword == "actor" {
		pType = ParticipantActor
	}

	p.s.SkipWhitespace()
	id, ok := p.parseParticipantID()
	if !ok {
		return fmt.Errorf("line %d: expected participant name", p.s.Peek().Pos.Line)
	}

	label := id
	p.s.SkipWhitespace()

	// Check for "as Label"
	if p.s.Peek().Kind == parser.TokenIdent && p.s.Peek().Text == "as" {
		p.s.Next() // consume "as"
		p.s.SkipWhitespace()
		label = strings.TrimSpace(parser.CollectLineText(p.s))
		label = strings.Trim(label, `"`)
	}

	parser.SkipToEndOfLine(p.s)

	pt, err := p.addParticipant(id, label, pType)
	if err != nil {
		return err
	}

	if currentGroup != nil {
		currentGroup.Participants = append(currentGroup.Participants, pt)
	}
	return nil
}

// tryParseMessage tries to parse a message line: FROM arrow [+|-] TO : Label
func (p *seqParser) tryParseMessage(elements *[]Element) (*Message, error) {
	saved := p.s.Save()

	fromID, ok := p.parseParticipantID()
	if !ok {
		p.s.Restore(saved)
		return nil, nil
	}

	p.s.SkipWhitespace()

	arrowType, modifier, ok := p.parseArrow()
	if !ok {
		p.s.Restore(saved)
		return nil, nil
	}

	p.s.SkipWhitespace()

	// Check for separate modifier if not embedded in arrow
	if modifier == "" {
		modifier = p.parseModifier()
	}

	p.s.SkipWhitespace()

	toID, ok := p.parseParticipantID()
	if !ok {
		p.s.Restore(saved)
		return nil, nil
	}

	p.s.SkipWhitespace()

	if p.s.Peek().Kind != parser.TokenColon {
		p.s.Restore(saved)
		return nil, nil
	}
	p.s.Next() // consume :

	p.s.SkipWhitespace()
	label := strings.TrimSpace(parser.CollectLineText(p.s))
	if p.s.Peek().Kind == parser.TokenNewline {
		p.s.Next()
	}

	from := p.getParticipant(fromID)
	to := p.getParticipant(toID)

	msgNumber := 0
	if p.sd.Autonumber {
		msgNumber = len(p.sd.Messages) + 1
	}

	msg := &Message{
		From:       from,
		To:         to,
		Label:      label,
		ArrowType:  arrowType,
		Number:     msgNumber,
		Activate:   modifier == "+",
		Deactivate: modifier == "-",
	}
	p.sd.Messages = append(p.sd.Messages, msg)
	*elements = append(*elements, msg)

	if msg.Activate {
		*elements = append(*elements, &ActivationEvent{Participant: to, Activate: true})
	}
	if msg.Deactivate {
		*elements = append(*elements, &ActivationEvent{Participant: from, Activate: false})
	}

	return msg, nil
}

// parseNote parses: Note (right of|left of|over) participant[,participant] : text
func (p *seqParser) parseNote() (*Note, error) {
	p.s.Next() // consume "Note"/"note"
	p.s.SkipWhitespace()

	var pos NotePosition
	tok := p.s.Peek()
	if tok.Kind != parser.TokenIdent {
		return nil, fmt.Errorf("line %d: expected note position (right, left, over)", tok.Pos.Line)
	}

	switch tok.Text {
	case "right":
		p.s.Next()
		p.s.SkipWhitespace()
		if p.s.Peek().Kind == parser.TokenIdent && p.s.Peek().Text == "of" {
			p.s.Next() // consume "of"
		}
		pos = NoteRightOf
	case "left":
		p.s.Next()
		p.s.SkipWhitespace()
		if p.s.Peek().Kind == parser.TokenIdent && p.s.Peek().Text == "of" {
			p.s.Next() // consume "of"
		}
		pos = NoteLeftOf
	case "over":
		p.s.Next()
		pos = NoteOver
	default:
		return nil, fmt.Errorf("line %d: expected note position (right, left, over), got %q", tok.Pos.Line, tok.Text)
	}

	p.s.SkipWhitespace()

	pID, ok := p.parseParticipantID()
	if !ok {
		return nil, fmt.Errorf("line %d: expected participant name in note", p.s.Peek().Pos.Line)
	}

	note := &Note{
		Position:    pos,
		Participant: p.getParticipant(pID),
	}

	// Check for second participant: Note over A,B
	if p.s.Peek().Kind == parser.TokenComma {
		p.s.Next() // consume ,
		p.s.SkipWhitespace()
		endID, ok := p.parseParticipantID()
		if ok {
			note.EndParticipant = p.getParticipant(endID)
		}
	}

	p.s.SkipWhitespace()

	// Expect colon
	if p.s.Peek().Kind == parser.TokenColon {
		p.s.Next()
	}

	p.s.SkipWhitespace()
	note.Text = strings.TrimSpace(parser.CollectLineText(p.s))
	if p.s.Peek().Kind == parser.TokenNewline {
		p.s.Next()
	}

	return note, nil
}

// parseActivation parses: (activate|deactivate) participant
func (p *seqParser) parseActivation() (*ActivationEvent, error) {
	keyword := p.s.Next().Text // "activate" or "deactivate"
	p.s.SkipWhitespace()

	id, ok := p.parseParticipantID()
	if !ok {
		return nil, fmt.Errorf("line %d: expected participant name after %s", p.s.Peek().Pos.Line, keyword)
	}
	parser.SkipToEndOfLine(p.s)

	return &ActivationEvent{
		Participant: p.getParticipant(id),
		Activate:    keyword == "activate",
	}, nil
}

// parseCreate parses: create (participant|actor) ID [as Label]
func (p *seqParser) parseCreate(currentGroup *ParticipantGroup) (Element, error) {
	p.s.Next() // consume "create"
	p.s.SkipWhitespace()

	// Expect participant or actor
	pType := ParticipantBox
	tok := p.s.Peek()
	if tok.Kind == parser.TokenIdent {
		if tok.Text == "actor" {
			pType = ParticipantActor
		}
		if tok.Text == "participant" || tok.Text == "actor" {
			p.s.Next()
		}
	}

	p.s.SkipWhitespace()
	id, ok := p.parseParticipantID()
	if !ok {
		return nil, fmt.Errorf("line %d: expected participant name after create", p.s.Peek().Pos.Line)
	}

	label := id
	p.s.SkipWhitespace()

	if p.s.Peek().Kind == parser.TokenIdent && p.s.Peek().Text == "as" {
		p.s.Next()
		p.s.SkipWhitespace()
		label = strings.TrimSpace(parser.CollectLineText(p.s))
		label = strings.Trim(label, `"`)
	}

	parser.SkipToEndOfLine(p.s)

	pt, err := p.addParticipant(id, label, pType)
	if err != nil {
		return nil, err
	}

	if currentGroup != nil {
		currentGroup.Participants = append(currentGroup.Participants, pt)
	}

	return &CreateEvent{Participant: pt}, nil
}

// parseDestroy parses: destroy participant
func (p *seqParser) parseDestroy() (Element, error) {
	p.s.Next() // consume "destroy"
	p.s.SkipWhitespace()

	id, ok := p.parseParticipantID()
	if !ok {
		return nil, fmt.Errorf("line %d: expected participant name after destroy", p.s.Peek().Pos.Line)
	}
	parser.SkipToEndOfLine(p.s)

	return &DestroyEvent{Participant: p.getParticipant(id)}, nil
}

// parseBox parses: box ["label"] [color]
func (p *seqParser) parseBox() *ParticipantGroup {
	p.s.Next() // consume "box"
	p.s.SkipWhitespace()

	label := ""
	if p.s.Peek().Kind == parser.TokenString {
		label = p.s.Next().Text
	}
	p.s.SkipWhitespace()
	if label == "" && p.s.Peek().Kind == parser.TokenIdent {
		label = p.s.Next().Text
	}

	parser.SkipToEndOfLine(p.s)

	return &ParticipantGroup{
		Label:        label,
		Participants: []*Participant{},
	}
}

// parseBlock parses: (loop|alt|opt|par|critical|break|rect) label NL elements (sections)* end
func (p *seqParser) parseBlock() (*Block, error) {
	keyword := p.s.Next().Text // block keyword
	p.s.SkipWhitespace()
	label := strings.TrimSpace(parser.CollectLineText(p.s))
	if p.s.Peek().Kind == parser.TokenNewline {
		p.s.Next()
	}

	block := &Block{Type: keyword, Label: label}

	blockElements, err := p.parseElements(true)
	if err != nil {
		return nil, err
	}
	block.Elements = blockElements

	// Parse sections (else/and/option) and end
	for {
		p.s.SkipNewlines()
		if p.s.AtEnd() {
			break
		}

		tok := p.s.Peek()
		if tok.Kind != parser.TokenIdent {
			break
		}

		switch tok.Text {
		case "else", "and", "option":
			p.s.Next() // consume divider keyword
			p.s.SkipWhitespace()
			sectionLabel := strings.TrimSpace(parser.CollectLineText(p.s))
			if p.s.Peek().Kind == parser.TokenNewline {
				p.s.Next()
			}
			section := &BlockSection{Label: sectionLabel}
			sectionElements, err := p.parseElements(true)
			if err != nil {
				return nil, err
			}
			section.Elements = sectionElements
			block.Sections = append(block.Sections, section)

		case "end":
			if p.isEndAlone() {
				p.s.Next()
				parser.SkipToEndOfLine(p.s)
			}
			return block, nil

		default:
			return block, nil
		}
	}

	return block, nil
}

// isBlockKeyword returns true if the identifier is a block start keyword.
func isBlockKeyword(text string) bool {
	switch text {
	case "loop", "alt", "opt", "par", "critical", "break", "rect":
		return true
	}
	return false
}

// parseElements parses sequence diagram elements until end/divider/EOF.
func (p *seqParser) parseElements(inBlock bool) ([]Element, error) {
	var elements []Element
	var currentGroup *ParticipantGroup

	for {
		p.s.SkipNewlines()
		if p.s.AtEnd() {
			return elements, nil
		}

		tok := p.s.Peek()

		// Handle "end" keyword
		if tok.Kind == parser.TokenIdent && tok.Text == "end" && p.isEndAlone() {
			if currentGroup != nil && !inBlock {
				// Close box group
				p.sd.Groups = append(p.sd.Groups, currentGroup)
				currentGroup = nil
				p.s.Next()
				parser.SkipToEndOfLine(p.s)
				continue
			}
			if inBlock {
				return elements, nil // don't consume — caller handles
			}
			// Stray "end" at top level — skip
			p.s.Next()
			parser.SkipToEndOfLine(p.s)
			continue
		}

		// Handle block dividers (only in block context)
		if inBlock && tok.Kind == parser.TokenIdent {
			if tok.Text == "else" || tok.Text == "and" || tok.Text == "option" {
				return elements, nil // don't consume — caller handles
			}
		}

		if tok.Kind != parser.TokenIdent && tok.Kind != parser.TokenString {
			// Try as message anyway (in case of quoted participant)
			if tok.Kind == parser.TokenString {
				msg, err := p.tryParseMessage(&elements)
				if err != nil {
					return nil, err
				}
				if msg != nil {
					continue
				}
			}
			parser.SkipToEndOfLine(p.s)
			continue
		}

		// Dispatch on keyword
		if tok.Kind == parser.TokenIdent {
			switch {
			case tok.Text == "autonumber":
				p.sd.Autonumber = true
				p.s.Next()
				parser.SkipToEndOfLine(p.s)
				continue

			case tok.Text == "participant" || tok.Text == "actor":
				if err := p.parseParticipantDecl(currentGroup); err != nil {
					return nil, err
				}
				continue

			case tok.Text == "box" && !inBlock:
				currentGroup = p.parseBox()
				continue

			case tok.Text == "create":
				elem, err := p.parseCreate(currentGroup)
				if err != nil {
					return nil, err
				}
				elements = append(elements, elem)
				continue

			case tok.Text == "destroy":
				elem, err := p.parseDestroy()
				if err != nil {
					return nil, err
				}
				elements = append(elements, elem)
				continue

			case tok.Text == "activate" || tok.Text == "deactivate":
				elem, err := p.parseActivation()
				if err != nil {
					return nil, err
				}
				elements = append(elements, elem)
				continue

			case strings.EqualFold(tok.Text, "note"):
				note, err := p.parseNote()
				if err != nil {
					return nil, err
				}
				elements = append(elements, note)
				continue

			case isBlockKeyword(tok.Text):
				block, err := p.parseBlock()
				if err != nil {
					return nil, err
				}
				elements = append(elements, block)
				continue
			}
		}

		// Try to parse as message (FROM arrow TO : Label)
		msg, err := p.tryParseMessage(&elements)
		if err != nil {
			return nil, err
		}
		if msg != nil {
			continue
		}

		// Unrecognized line — error
		return nil, fmt.Errorf("line %d: invalid syntax: %q",
			tok.Pos.Line, strings.TrimSpace(parser.CollectLineText(p.s)))
	}
}

// Parse parses Mermaid sequence diagram text into a SequenceDiagram model.
func Parse(input string) (*SequenceDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)

	// Skip whitespace/newlines to find the keyword
	s.SkipNewlines()
	if s.AtEnd() {
		return nil, fmt.Errorf("no content found")
	}

	// Expect "sequenceDiagram" keyword
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != SequenceDiagramKeyword {
		return nil, fmt.Errorf("expected %q keyword", SequenceDiagramKeyword)
	}
	s.Next()
	parser.SkipToEndOfLine(s)

	sd := &SequenceDiagram{
		Participants: []*Participant{},
		Messages:     []*Message{},
		Elements:     []Element{},
		Autonumber:   false,
		Groups:       []*ParticipantGroup{},
	}

	p := &seqParser{
		s:              s,
		sd:             sd,
		participantMap: make(map[string]*Participant),
	}

	elements, err := p.parseElements(false)
	if err != nil {
		return nil, err
	}
	sd.Elements = elements

	if len(sd.Participants) == 0 {
		return nil, fmt.Errorf("no participants found")
	}

	return sd, nil
}
