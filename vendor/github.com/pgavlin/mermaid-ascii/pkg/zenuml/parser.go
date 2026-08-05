// Package zenuml implements parsing and rendering of ZenUML sequence diagrams
// in Mermaid syntax.
package zenuml

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// zenUMLKeyword is the Mermaid keyword that identifies a ZenUML diagram.
const zenUMLKeyword = "zenuml"

// MessageType distinguishes sync, async, and return messages.
type MessageType int

const (
	// SyncMessage represents a synchronous message call.
	SyncMessage MessageType = iota
	// AsyncMessage represents an asynchronous message call.
	AsyncMessage
	// ReturnMessage represents a return message from a call.
	ReturnMessage
)

// Participant represents a participant in the ZenUML diagram.
type Participant struct {
	TypeName string // declared type, e.g. "Client"
	ID       string // identifier, e.g. "client"
	Index    int
}

// Message represents a message/call in the ZenUML diagram.
type Message struct {
	From   *Participant
	To     *Participant
	Method string
	Args   string
	Type   MessageType
	Label  string     // used for return value text
	Nested []*Message // nested messages inside async blocks
}

// ZenUMLDiagram represents a parsed ZenUML diagram.
type ZenUMLDiagram struct {
	Participants []*Participant
	Messages     []*Message
}

// IsZenUML returns true if the input starts with the zenuml keyword.
func IsZenUML(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, zenUMLKeyword)
	}
	return false
}

type zenParser struct {
	d    *ZenUMLDiagram
	pMap map[string]*Participant
}

// Parse parses ZenUML input text into a ZenUMLDiagram.
func Parse(input string) (*ZenUMLDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// Expect "zenuml" keyword
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != zenUMLKeyword {
		return nil, fmt.Errorf("expected %q keyword", zenUMLKeyword)
	}
	s.Next()
	s.SkipNewlines()

	p := &zenParser{
		d: &ZenUMLDiagram{
			Participants: []*Participant{},
			Messages:     []*Message{},
		},
		pMap: make(map[string]*Participant),
	}

	messages, err := p.parseStatements(s, false)
	if err != nil {
		return nil, err
	}
	p.d.Messages = messages

	if len(p.d.Participants) == 0 {
		return nil, fmt.Errorf("no participants found")
	}

	return p.d, nil
}

func (p *zenParser) parseStatements(s *parser.Scanner, inBlock bool) ([]*Message, error) {
	var messages []*Message

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		// Close brace — end of block
		if tok.Kind == parser.TokenRBrace {
			if inBlock {
				s.Next() // consume '}'
				// Check for continuation: } else {, } catch {, } finally {
				// Don't consume continuation here — let the caller handle it
				return messages, nil
			}
			s.Next()
			continue
		}

		// Annotator: @Type Name
		if tok.Kind == parser.TokenAt {
			if err := p.parseAnnotator(s); err != nil {
				return nil, err
			}
			continue
		}

		if tok.Kind != parser.TokenIdent {
			parser.SkipToEndOfLine(s)
			continue
		}

		// Keyword-based dispatch
		switch tok.Text {
		case "return":
			msg := p.parseReturn(s, messages)
			messages = append(messages, msg)
			continue
		case "new":
			msg, err := p.parseNew(s)
			if err != nil {
				return nil, err
			}
			messages = append(messages, msg)
			continue
		case "while", "if", "for", "forEach", "loop", "opt", "par", "try":
			nested, err := p.parseControlFlow(s)
			if err != nil {
				return nil, err
			}
			messages = append(messages, nested...)
			continue
		}

		// Try arrow message: A->B: ...
		if arrowMsg, ok := p.tryParseArrowMessage(s); ok {
			messages = append(messages, arrowMsg)
			continue
		}

		// Try dot-call message: target.method(args) [{ ... }]
		if dotMsg, ok := p.tryParseDotCall(s); ok {
			messages = append(messages, dotMsg)
			continue
		}

		// Try alias: A as Name
		if p.tryParseAlias(s) {
			continue
		}

		// Try participant declaration: Type Name (two idents, first not reserved)
		if p.tryParseParticipantDecl(s) {
			continue
		}

		// Standalone participant: single ident
		if p.tryParseStandaloneParticipant(s) {
			continue
		}

		// Unknown — skip to end of line
		parser.SkipToEndOfLine(s)
	}

	return messages, nil
}

// parseAnnotator handles: @TypeName ID
func (p *zenParser) parseAnnotator(s *parser.Scanner) error {
	s.Next() // consume '@'
	// No whitespace skip — type name follows immediately
	typeTok := s.Peek()
	if typeTok.Kind != parser.TokenIdent {
		parser.SkipToEndOfLine(s)
		return nil
	}
	typeName := s.Next().Text
	s.SkipWhitespace()

	idTok := s.Peek()
	if idTok.Kind != parser.TokenIdent {
		parser.SkipToEndOfLine(s)
		return nil
	}
	id := s.Next().Text

	if _, exists := p.pMap[id]; !exists {
		pt := &Participant{
			TypeName: typeName,
			ID:       id,
			Index:    len(p.d.Participants),
		}
		p.d.Participants = append(p.d.Participants, pt)
		p.pMap[id] = pt
	}
	return nil
}

// parseReturn handles: return value
func (p *zenParser) parseReturn(s *parser.Scanner, priorMessages []*Message) *Message {
	s.Next() // consume "return"
	s.SkipWhitespace()
	label := strings.TrimSpace(parser.ConsumeRestOfLine(s))

	var from, to *Participant
	if len(priorMessages) > 0 {
		last := priorMessages[len(priorMessages)-1]
		from = last.To
		to = last.From
	} else if len(p.d.Participants) > 0 {
		from = p.d.Participants[0]
	}

	return &Message{
		From:  from,
		To:    to,
		Label: label,
		Type:  ReturnMessage,
	}
}

// parseNew handles: new Target or new Target(args)
func (p *zenParser) parseNew(s *parser.Scanner) (*Message, error) {
	s.Next() // consume "new"
	s.SkipWhitespace()

	targetTok := s.Peek()
	if targetTok.Kind != parser.TokenIdent {
		return nil, parser.Errorf(targetTok.Pos, "expected target after 'new'")
	}
	target := s.Next().Text

	var args string
	if s.Peek().Kind == parser.TokenLParen {
		s.Next() // consume '('
		args = p.collectUntilRParen(s)
	}

	to := p.getOrCreateParticipant(target)
	from := inferCaller(p.d, to)

	return &Message{
		From:   from,
		To:     to,
		Method: "new",
		Args:   args,
		Type:   SyncMessage,
	}, nil
}

// parseControlFlow handles: while(cond) { ... }, if(cond) { ... } else { ... }, etc.
func (p *zenParser) parseControlFlow(s *parser.Scanner) ([]*Message, error) {
	s.Next() // consume the keyword (while/if/for/etc.)
	s.SkipWhitespace()

	// Optional condition in parens
	if s.Peek().Kind == parser.TokenLParen {
		s.Next() // consume '('
		p.skipUntilRParen(s)
		s.SkipWhitespace()
	}

	// Expect '{'
	if s.Peek().Kind != parser.TokenLBrace {
		parser.SkipToEndOfLine(s)
		return nil, nil
	}
	s.Next() // consume '{'

	nested, err := p.parseStatements(s, true)
	if err != nil {
		return nil, err
	}
	var messages []*Message
	messages = append(messages, nested...)

	// Handle continuations: } else {, } catch {, } finally {
	for {
		s.SkipWhitespace()
		tok := s.Peek()
		if tok.Kind != parser.TokenIdent {
			break
		}
		if tok.Text != "else" && tok.Text != "catch" && tok.Text != "finally" {
			break
		}
		s.Next() // consume continuation keyword
		s.SkipWhitespace()

		// Optional condition in parens (e.g., catch(e))
		if s.Peek().Kind == parser.TokenLParen {
			s.Next()
			p.skipUntilRParen(s)
			s.SkipWhitespace()
		}

		// Expect '{'
		if s.Peek().Kind != parser.TokenLBrace {
			break
		}
		s.Next() // consume '{'

		nested2, err := p.parseStatements(s, true)
		if err != nil {
			return nil, err
		}
		messages = append(messages, nested2...)
	}

	return messages, nil
}

// tryParseArrowMessage attempts: A->B: message [{ ... }]
func (p *zenParser) tryParseArrowMessage(s *parser.Scanner) (*Message, bool) {
	saved := s.Save()

	fromTok := s.Peek()
	if fromTok.Kind != parser.TokenIdent {
		return nil, false
	}
	fromID := s.Next().Text

	// Expect "->" operator
	opTok := s.Peek()
	if opTok.Kind != parser.TokenOperator || opTok.Text != "->" {
		s.Restore(saved)
		return nil, false
	}
	s.Next() // consume "->"

	toTok := s.Peek()
	if toTok.Kind != parser.TokenIdent {
		s.Restore(saved)
		return nil, false
	}
	toID := s.Next().Text
	s.SkipWhitespace()

	// Expect ":"
	if s.Peek().Kind != parser.TokenColon {
		s.Restore(saved)
		return nil, false
	}
	s.Next() // consume ":"
	s.SkipWhitespace()

	// Collect label text until newline or '{'
	label := p.collectLabelUntilBrace(s)

	fromP := p.getOrCreateParticipant(fromID)
	toP := p.getOrCreateParticipant(toID)

	// Check for block
	if s.Peek().Kind == parser.TokenLBrace {
		s.Next() // consume '{'
		nested, err := p.parseStatements(s, true)
		if err != nil {
			return nil, false
		}
		return &Message{
			From:   fromP,
			To:     toP,
			Label:  label,
			Type:   AsyncMessage,
			Nested: nested,
		}, true
	}

	return &Message{
		From:  fromP,
		To:    toP,
		Label: label,
		Type:  SyncMessage,
	}, true
}

// tryParseDotCall attempts: target.method(args) [{ ... }]
func (p *zenParser) tryParseDotCall(s *parser.Scanner) (*Message, bool) {
	saved := s.Save()

	targetTok := s.Peek()
	if targetTok.Kind != parser.TokenIdent {
		return nil, false
	}
	target := s.Next().Text

	// Expect '.'
	if s.Peek().Kind != parser.TokenDot {
		s.Restore(saved)
		return nil, false
	}
	s.Next() // consume '.'

	methodTok := s.Peek()
	if methodTok.Kind != parser.TokenIdent {
		s.Restore(saved)
		return nil, false
	}
	method := s.Next().Text

	// Expect '('
	if s.Peek().Kind != parser.TokenLParen {
		s.Restore(saved)
		return nil, false
	}
	s.Next() // consume '('
	args := p.collectUntilRParen(s)
	s.SkipWhitespace()

	to := p.getOrCreateParticipant(target)
	from := inferCaller(p.d, to)

	// Check for async block '{'
	if s.Peek().Kind == parser.TokenLBrace {
		s.Next() // consume '{'
		nested, err := p.parseStatements(s, true)
		if err != nil {
			return nil, false
		}
		return &Message{
			From:   from,
			To:     to,
			Method: method,
			Args:   args,
			Type:   AsyncMessage,
			Nested: nested,
		}, true
	}

	return &Message{
		From:   from,
		To:     to,
		Method: method,
		Args:   args,
		Type:   SyncMessage,
	}, true
}

// tryParseAlias attempts: A as DisplayName
func (p *zenParser) tryParseAlias(s *parser.Scanner) bool {
	saved := s.Save()

	idTok := s.Peek()
	if idTok.Kind != parser.TokenIdent {
		return false
	}
	id := s.Next().Text
	s.SkipWhitespace()

	asTok := s.Peek()
	if asTok.Kind != parser.TokenIdent || asTok.Text != "as" {
		s.Restore(saved)
		return false
	}
	s.Next() // consume "as"
	s.SkipWhitespace()

	nameTok := s.Peek()
	if nameTok.Kind != parser.TokenIdent {
		s.Restore(saved)
		return false
	}
	displayName := s.Next().Text

	if _, exists := p.pMap[id]; !exists {
		pt := &Participant{
			TypeName: displayName,
			ID:       id,
			Index:    len(p.d.Participants),
		}
		p.d.Participants = append(p.d.Participants, pt)
		p.pMap[id] = pt
	}
	return true
}

// tryParseParticipantDecl attempts: TypeName ID (two idents, first not reserved)
func (p *zenParser) tryParseParticipantDecl(s *parser.Scanner) bool {
	saved := s.Save()

	typeTok := s.Peek()
	if typeTok.Kind != parser.TokenIdent {
		return false
	}
	typeName := s.Next().Text

	if isReservedWord(typeName) {
		s.Restore(saved)
		return false
	}

	s.SkipWhitespace()

	idTok := s.Peek()
	if idTok.Kind != parser.TokenIdent {
		s.Restore(saved)
		return false
	}

	id := idTok.Text
	if id == "as" {
		s.Restore(saved)
		return false
	}

	// Make sure this is end of meaningful content on the line
	// (i.e. next non-whitespace is newline or EOF)
	s.Next() // consume id

	// Peek to see if there's more on the line (like a dot call)
	next := s.Peek()
	if next.Kind == parser.TokenDot || next.Kind == parser.TokenLParen || next.Kind == parser.TokenOperator {
		s.Restore(saved)
		return false
	}

	if _, exists := p.pMap[id]; !exists {
		pt := &Participant{
			TypeName: typeName,
			ID:       id,
			Index:    len(p.d.Participants),
		}
		p.d.Participants = append(p.d.Participants, pt)
		p.pMap[id] = pt
	}
	return true
}

// tryParseStandaloneParticipant attempts: a single ident (not reserved)
func (p *zenParser) tryParseStandaloneParticipant(s *parser.Scanner) bool {
	saved := s.Save()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent {
		return false
	}
	id := s.Next().Text

	if isReservedWord(id) {
		s.Restore(saved)
		return false
	}

	// Make sure it's the only thing on the line
	next := s.Peek()
	if next.Kind != parser.TokenNewline && next.Kind != parser.TokenEOF && next.Kind != parser.TokenWhitespace {
		s.Restore(saved)
		return false
	}
	// If whitespace, check what follows
	if next.Kind == parser.TokenWhitespace {
		s.SkipWhitespace()
		after := s.Peek()
		if after.Kind != parser.TokenNewline && after.Kind != parser.TokenEOF {
			s.Restore(saved)
			return false
		}
	}

	if _, exists := p.pMap[id]; !exists {
		pt := &Participant{
			TypeName: id,
			ID:       id,
			Index:    len(p.d.Participants),
		}
		p.d.Participants = append(p.d.Participants, pt)
		p.pMap[id] = pt
	}
	return true
}

// collectUntilRParen collects text between parens (after '(' is already consumed).
func (p *zenParser) collectUntilRParen(s *parser.Scanner) string {
	var parts []string
	for !s.AtEnd() {
		tok := s.Peek()
		if tok.Kind == parser.TokenRParen {
			s.Next()
			break
		}
		if tok.Kind == parser.TokenNewline {
			break
		}
		s.Next()
		parts = append(parts, tok.Text)
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

// skipUntilRParen skips tokens until ')' (after '(' is already consumed).
func (p *zenParser) skipUntilRParen(s *parser.Scanner) {
	for !s.AtEnd() {
		tok := s.Peek()
		if tok.Kind == parser.TokenRParen {
			s.Next()
			return
		}
		if tok.Kind == parser.TokenNewline {
			return
		}
		s.Next()
	}
}

// collectLabelUntilBrace collects text until '{' or newline/EOF.
func (p *zenParser) collectLabelUntilBrace(s *parser.Scanner) string {
	var parts []string
	for !s.AtEnd() {
		tok := s.Peek()
		if tok.Kind == parser.TokenLBrace || tok.Kind == parser.TokenNewline || tok.Kind == parser.TokenEOF {
			break
		}
		s.Next()
		parts = append(parts, tok.Text)
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

// inferCaller returns the first declared participant as the default caller,
// as long as it is not the same as the target. If only one participant exists,
// it returns that participant (self-call).
func inferCaller(d *ZenUMLDiagram, _ *Participant) *Participant {
	if len(d.Participants) == 0 {
		return nil
	}
	return d.Participants[0]
}

func (p *zenParser) getOrCreateParticipant(id string) *Participant {
	if pt, exists := p.pMap[id]; exists {
		return pt
	}
	pt := &Participant{
		TypeName: id,
		ID:       id,
		Index:    len(p.d.Participants),
	}
	p.d.Participants = append(p.d.Participants, pt)
	p.pMap[id] = pt
	return pt
}

func isReservedWord(s string) bool {
	switch strings.ToLower(s) {
	case "return", "zenuml",
		"while", "if", "for", "foreach", "loop", "opt", "par", "try",
		"new", "else", "catch", "finally":
		return true
	}
	return false
}
