package graph

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/elliotchance/orderedmap/v2"
	"github.com/pgavlin/mermaid-ascii/pkg/parser"
	log "github.com/sirupsen/logrus"
)

// Properties holds the parsed representation of a Mermaid graph definition,
// including nodes, edges, subgraphs, style classes, and layout configuration.
type Properties struct {
	data             *orderedmap.OrderedMap[string, []textEdge]
	nodeInfo         map[string]textNode // maps node id to its textNode (for shape/label info)
	styleClasses     *map[string]styleClass
	graphDirection   string
	styleType        string
	paddingX         int
	paddingY         int
	subgraphs        []*textSubgraph
	useAscii         bool
	boxBorderPadding int
	showCoords       bool
	targetWidth      int
}

// nodeShape represents the visual shape of a node in a graph diagram.
type nodeShape int

const (
	shapeRect      nodeShape = iota // A[text] or bare A - rectangle (default)
	shapeRounded                    // A(text) - rounded rectangle
	shapeStadium                    // A([text]) - stadium-shaped (rounded sides)
	shapeSubroutine                 // A[[text]] - subroutine (double vertical borders)
	shapeCylinder                   // A[(text)] - cylinder (curved top/bottom)
	shapeCircle                     // A((text)) - circle/double circle
	shapeDiamond                    // A{text} - diamond/rhombus
	shapeHexagon                    // A{{text}} - hexagon
	shapeFlag                       // A>text] - asymmetric/flag shape
)

type textNode struct {
	id         string    // unique identifier used as map key (e.g. "A" from "A[text]")
	name       string    // display label (e.g. "text" from "A[text]", or "A" for bare nodes)
	styleClass string
	shape      nodeShape
}

// EdgeType represents the type of edge connecting two nodes.
type EdgeType int

const (
	SolidArrow         EdgeType = iota // -->
	SolidLine                          // ---
	DottedArrow                        // -.->
	DottedLine                         // -.-
	ThickArrow                         // ==>
	ThickLine                          // ===
	BidirectionalArrow                 // <-->
	CrossEnd                           // --x
	CircleEnd                          // --o
)

type textEdge struct {
	parent   textNode
	child    textNode
	label    string
	edgeType EdgeType
}

type textSubgraph struct {
	id        string // unique identifier for edge references (e.g. "sg1" from "subgraph sg1 [Title]")
	name      string // display label (e.g. "Title" from "subgraph sg1 [Title]")
	nodes     []string
	nodeSet   map[string]bool
	parent    *textSubgraph
	children  []*textSubgraph
	direction string // per-subgraph direction (LR or TD), parsed but not yet applied to layout
}

// getSubgraphByID returns the subgraph with the given ID, or nil if not found.
func (gp *Properties) getSubgraphByID(id string) *textSubgraph {
	for _, sg := range gp.subgraphs {
		if sg.id == id {
			return sg
		}
	}
	return nil
}

// resolveSubgraphNode checks if a node ID refers to a subgraph, and if so,
// returns the first node inside that subgraph as a proxy. Returns the original
// node ID if it's not a subgraph reference.
func (gp *Properties) resolveSubgraphNode(nodeID string) string {
	sg := gp.getSubgraphByID(nodeID)
	if sg != nil && len(sg.nodes) > 0 {
		return sg.nodes[0]
	}
	return nodeID
}

// decodeEntityCodes replaces mermaid entity codes with their actual characters.
func decodeEntityCodes(s string) string {
	replacer := strings.NewReplacer(
		"#35;", "#",
		"#amp;", "&",
		"#lt;", "<",
		"#gt;", ">",
		"#quot;", "\"",
		"#nbsp;", " ",
	)
	return replacer.Replace(s)
}

// stripMarkdown removes basic markdown formatting from text.
func stripMarkdown(s string) string {
	// Bold: **text** -> text (must come before italic)
	bold := regexp.MustCompile(`\*\*(.+?)\*\*`)
	s = bold.ReplaceAllString(s, "$1")
	// Italic: *text* -> text
	italic := regexp.MustCompile(`\*(.+?)\*`)
	s = italic.ReplaceAllString(s, "$1")
	return s
}

// replaceBrTags replaces HTML <br>, <br/>, and <br /> tags with newlines.
var brTagRegexp = regexp.MustCompile(`<br\s*/?>`)

func replaceBrTags(s string) string {
	return brTagRegexp.ReplaceAllString(s, "\n")
}

func addNode(node textNode, data *orderedmap.OrderedMap[string, []textEdge], nodeInfo map[string]textNode) {
	if _, ok := data.Get(node.id); !ok {
		data.Set(node.id, []textEdge{})
	}
	// Always store/update node info (later definitions can override labels)
	if _, exists := nodeInfo[node.id]; !exists {
		nodeInfo[node.id] = node
	}
}

func setData(parent textNode, edge textEdge, data *orderedmap.OrderedMap[string, []textEdge], nodeInfo map[string]textNode) {
	// Check if the parent is in the map
	if children, ok := data.Get(parent.id); ok {
		// If it is, append the child to the list of children
		data.Set(parent.id, append(children, edge))
	} else {
		// If it isn't, add it to the map
		data.Set(parent.id, []textEdge{edge})
	}
	// Store node info for parent and child
	if _, exists := nodeInfo[parent.id]; !exists {
		nodeInfo[parent.id] = parent
	}
	// Check if the child is in the map
	if _, ok := data.Get(edge.child.id); ok {
		// If it is, do nothing
	} else {
		// If it isn't, add it to the map
		data.Set(edge.child.id, []textEdge{})
	}
	if _, exists := nodeInfo[edge.child.id]; !exists {
		nodeInfo[edge.child.id] = edge.child
	}
}

func setArrowWithLabelAndType(lhs, rhs []textNode, label string, edgeType EdgeType, data *orderedmap.OrderedMap[string, []textEdge], nodeInfo map[string]textNode) []textNode {
	log.Debug("Setting arrow from ", lhs, " to ", rhs, " with label ", label, " type ", edgeType)
	for _, l := range lhs {
		for _, r := range rhs {
			setData(l, textEdge{l, r, label, edgeType}, data, nodeInfo)
		}
	}
	return rhs
}

func setArrowWithLabel(lhs, rhs []textNode, label string, data *orderedmap.OrderedMap[string, []textEdge], nodeInfo map[string]textNode) []textNode {
	return setArrowWithLabelAndType(lhs, rhs, label, SolidArrow, data, nodeInfo)
}

func setArrow(lhs, rhs []textNode, data *orderedmap.OrderedMap[string, []textEdge], nodeInfo map[string]textNode) []textNode {
	return setArrowWithLabelAndType(lhs, rhs, "", SolidArrow, data, nodeInfo)
}

func setArrowOfType(lhs, rhs []textNode, edgeType EdgeType, data *orderedmap.OrderedMap[string, []textEdge], nodeInfo map[string]textNode) []textNode {
	return setArrowWithLabelAndType(lhs, rhs, "", edgeType, data, nodeInfo)
}

func setArrowWithLabelOfType(lhs, rhs []textNode, label string, edgeType EdgeType, data *orderedmap.OrderedMap[string, []textEdge], nodeInfo map[string]textNode) []textNode {
	return setArrowWithLabelAndType(lhs, rhs, label, edgeType, data, nodeInfo)
}

// graphParser implements a recursive descent parser for Mermaid graph/flowchart syntax.
type graphParser struct {
	s             *parser.Scanner
	props         *Properties
	subgraphStack []*textSubgraph
}

// edgeTypeFromOperator maps an operator token text to an EdgeType.
// Returns the EdgeType and true if recognized, or false if not an edge operator.
func edgeTypeFromOperator(op string) (EdgeType, bool) {
	switch op {
	case "-->":
		return SolidArrow, true
	case "---":
		return SolidLine, true
	case "-.->":
		return DottedArrow, true
	case "-.-":
		return DottedLine, true
	case "==>":
		return ThickArrow, true
	case "===":
		return ThickLine, true
	case "<-->":
		return BidirectionalArrow, true
	case "--x":
		return CrossEnd, true
	case "--o":
		return CircleEnd, true
	default:
		return 0, false
	}
}

// collectShapeText collects all token text until the closing delimiter, EOF, or newline.
func (p *graphParser) collectShapeText(closer parser.TokenKind) string {
	var b strings.Builder
	for {
		tok := p.s.Peek()
		if tok.Kind == closer || tok.Kind == parser.TokenEOF || tok.Kind == parser.TokenNewline {
			break
		}
		p.s.Next()
		b.WriteString(tok.Text)
	}
	return strings.TrimSpace(b.String())
}

// parseNodeID parses a node identifier (ident or number).
func (p *graphParser) parseNodeID() (string, bool) {
	tok := p.s.Peek()
	if tok.Kind == parser.TokenIdent || tok.Kind == parser.TokenNumber {
		p.s.Next()
		return tok.Text, true
	}
	return "", false
}

// parseNode parses a single node: id shape? (:::class)?
func (p *graphParser) parseNode() (textNode, bool) {
	id, ok := p.parseNodeID()
	if !ok {
		return textNode{}, false
	}

	label := id
	shape := shapeRect
	class := ""

	// Try to parse shape (immediately after ID, no whitespace)
	tok := p.s.Peek()
	switch tok.Kind {
	case parser.TokenLBracket:
		p.s.Next() // consume [
		next := p.s.Peek()
		switch next.Kind {
		case parser.TokenLBracket:
			// subroutine: [[text]]
			p.s.Next() // consume inner [
			label = p.collectShapeText(parser.TokenRBracket)
			p.s.Next() // consume inner ]
			p.s.Next() // consume outer ]
			shape = shapeSubroutine
		case parser.TokenLParen:
			// cylinder: [(text)]
			p.s.Next() // consume (
			label = p.collectShapeText(parser.TokenRParen)
			p.s.Next() // consume )
			p.s.Next() // consume ]
			shape = shapeCylinder
		case parser.TokenString:
			// quoted rect: ["text"]
			label = next.Text
			p.s.Next() // consume string
			p.s.Next() // consume ]
			shape = shapeRect
		default:
			// plain rect: [text]
			label = p.collectShapeText(parser.TokenRBracket)
			p.s.Next() // consume ]
			shape = shapeRect
		}
	case parser.TokenLParen:
		p.s.Next() // consume (
		next := p.s.Peek()
		switch next.Kind {
		case parser.TokenLParen:
			// circle: ((text))
			p.s.Next() // consume inner (
			label = p.collectShapeText(parser.TokenRParen)
			p.s.Next() // consume inner )
			p.s.Next() // consume outer )
			shape = shapeCircle
		case parser.TokenLBracket:
			// stadium: ([text])
			p.s.Next() // consume [
			label = p.collectShapeText(parser.TokenRBracket)
			p.s.Next() // consume ]
			p.s.Next() // consume )
			shape = shapeStadium
		default:
			// rounded: (text)
			label = p.collectShapeText(parser.TokenRParen)
			p.s.Next() // consume )
			shape = shapeRounded
		}
	case parser.TokenLBrace:
		p.s.Next() // consume {
		next := p.s.Peek()
		if next.Kind == parser.TokenLBrace {
			// hexagon: {{text}}
			p.s.Next() // consume inner {
			label = p.collectShapeText(parser.TokenRBrace)
			p.s.Next() // consume inner }
			p.s.Next() // consume outer }
			shape = shapeHexagon
		} else {
			// diamond: {text}
			label = p.collectShapeText(parser.TokenRBrace)
			p.s.Next() // consume }
			shape = shapeDiamond
		}
	case parser.TokenOperator:
		if tok.Text == ">" {
			// flag: >text]
			p.s.Next() // consume >
			label = p.collectShapeText(parser.TokenRBracket)
			p.s.Next() // consume ]
			shape = shapeFlag
		}
	}

	// Try to parse :::className (three consecutive colons, no whitespace)
	if p.s.Peek().Kind == parser.TokenColon {
		saved := p.s.Save()
		p.s.Next() // first :
		if p.s.Peek().Kind == parser.TokenColon {
			p.s.Next() // second :
			if p.s.Peek().Kind == parser.TokenColon {
				p.s.Next() // third :
				if p.s.Peek().Kind == parser.TokenIdent {
					class = p.s.Next().Text
				}
			}
		}
		if class == "" {
			// Not a valid ::: sequence, restore
			p.s.Restore(saved)
		}
	}

	// Post-process label
	label = decodeEntityCodes(label)
	label = stripMarkdown(label)
	label = replaceBrTags(label)

	return textNode{id: id, name: label, styleClass: class, shape: shape}, true
}

// parseNodeList parses: node ("&" node)*
func (p *graphParser) parseNodeList() ([]textNode, bool) {
	node, ok := p.parseNode()
	if !ok {
		return nil, false
	}
	nodes := []textNode{node}

	for {
		saved := p.s.Save()
		p.s.SkipWhitespace()
		if p.s.Peek().Kind == parser.TokenAmpersand {
			p.s.Next() // consume &
			p.s.SkipWhitespace()
			next, ok := p.parseNode()
			if ok {
				nodes = append(nodes, next)
				continue
			}
		}
		p.s.Restore(saved)
		break
	}
	return nodes, true
}

// tryParseEdge checks if the next tokens form an edge operator, optionally followed by |label|.
// Returns (edgeType, label, true) if an edge was found, or (0, "", false) if not.
func (p *graphParser) tryParseEdge() (EdgeType, string, bool) {
	saved := p.s.Save()
	p.s.SkipWhitespace()

	tok := p.s.Peek()
	if tok.Kind != parser.TokenOperator {
		p.s.Restore(saved)
		return 0, "", false
	}

	edgeType, ok := edgeTypeFromOperator(tok.Text)
	if !ok {
		p.s.Restore(saved)
		return 0, "", false
	}
	p.s.Next() // consume operator

	// Check for |label|
	label := ""
	if p.s.Peek().Kind == parser.TokenPipe {
		p.s.Next() // consume |
		var b strings.Builder
		for {
			t := p.s.Peek()
			if t.Kind == parser.TokenPipe || t.Kind == parser.TokenEOF || t.Kind == parser.TokenNewline {
				break
			}
			p.s.Next()
			b.WriteString(t.Text)
		}
		if p.s.Peek().Kind == parser.TokenPipe {
			p.s.Next() // consume closing |
		}
		label = strings.TrimSpace(b.String())
	}

	return edgeType, label, true
}

// parseChain parses a chain of nodes connected by edges: nodeList (edge nodeList)*
func (p *graphParser) parseChain() {
	lhs, ok := p.parseNodeList()
	if !ok {
		// Can't parse as node list — skip rest of line
		parser.SkipToEndOfLine(p.s)
		return
	}

	// Add all LHS nodes
	for _, node := range lhs {
		addNode(node, p.props.data, p.props.nodeInfo)
	}

	// Parse chain: (edge nodeList)*
	for {
		edgeType, label, hasEdge := p.tryParseEdge()
		if !hasEdge {
			break
		}
		p.s.SkipWhitespace()
		rhs, ok := p.parseNodeList()
		if !ok {
			break
		}
		for _, node := range rhs {
			addNode(node, p.props.data, p.props.nodeInfo)
		}
		setArrowWithLabelAndType(lhs, rhs, label, edgeType, p.props.data, p.props.nodeInfo)
		lhs = rhs
	}

	// Skip any remaining content on this line
	parser.SkipToEndOfLine(p.s)
}

// isEndKeyword checks if the current token is "end" alone on its line.
func (p *graphParser) isEndKeyword() bool {
	tok := p.s.Peek()
	if tok.Kind != parser.TokenIdent || !strings.EqualFold(tok.Text, "end") {
		return false
	}
	saved := p.s.Save()
	p.s.Next() // consume "end"
	p.s.SkipWhitespace()
	next := p.s.Peek()
	p.s.Restore(saved)
	return next.Kind == parser.TokenNewline || next.Kind == parser.TokenEOF
}

// parseSubgraph parses: subgraph id ("[" title "]")? NL statements "end"
func (p *graphParser) parseSubgraph() {
	p.s.Next() // consume "subgraph"
	p.s.SkipWhitespace()

	// Parse subgraph ID
	id := ""
	if p.s.Peek().Kind == parser.TokenIdent || p.s.Peek().Kind == parser.TokenNumber {
		id = p.s.Next().Text
	}

	name := id

	// Check for optional [title]
	p.s.SkipWhitespace()
	if p.s.Peek().Kind == parser.TokenLBracket {
		p.s.Next() // consume [
		name = p.collectShapeText(parser.TokenRBracket)
		if p.s.Peek().Kind == parser.TokenRBracket {
			p.s.Next() // consume ]
		}
	}

	parser.SkipToEndOfLine(p.s)

	sg := &textSubgraph{
		id:       id,
		name:     name,
		nodes:    []string{},
		nodeSet:  make(map[string]bool),
		children: []*textSubgraph{},
	}

	// Set parent relationship
	if len(p.subgraphStack) > 0 {
		parent := p.subgraphStack[len(p.subgraphStack)-1]
		sg.parent = parent
		parent.children = append(parent.children, sg)
	}

	p.subgraphStack = append(p.subgraphStack, sg)
	p.props.subgraphs = append(p.props.subgraphs, sg)
	log.Debugf("Started subgraph id=%s name=%s", id, name)

	// Parse inner statements until "end"
	p.parseStatements()

	// Consume "end"
	if p.isEndKeyword() {
		p.s.Next() // consume "end"
		parser.SkipToEndOfLine(p.s)
	}

	// Pop subgraph stack
	if len(p.subgraphStack) > 0 {
		closedSubgraph := p.subgraphStack[len(p.subgraphStack)-1]
		p.subgraphStack = p.subgraphStack[:len(p.subgraphStack)-1]
		log.Debugf("Ended subgraph %s", closedSubgraph.name)
	}
}

// parseDirection parses: direction (LR|TD|TB|BT|RL)
func (p *graphParser) parseDirection() {
	p.s.Next() // consume "direction"
	p.s.SkipWhitespace()
	if p.s.Peek().Kind == parser.TokenIdent {
		dir := strings.ToUpper(p.s.Next().Text)
		if len(p.subgraphStack) > 0 {
			currentSubgraph := p.subgraphStack[len(p.subgraphStack)-1]
			currentSubgraph.direction = dir
			log.Debugf("Set direction %s for subgraph %s", dir, currentSubgraph.name)
		}
	}
	parser.SkipToEndOfLine(p.s)
}

// parseClassDef parses: classDef className styles...
func (p *graphParser) parseClassDef() {
	p.s.Next() // consume "classDef"
	p.s.SkipWhitespace()

	// Parse class name
	className := ""
	if p.s.Peek().Kind == parser.TokenIdent {
		className = p.s.Next().Text
	}
	p.s.SkipWhitespace()

	// Collect rest of line as styles
	styles := strings.TrimSpace(parser.CollectLineText(p.s))
	if p.s.Peek().Kind == parser.TokenNewline {
		p.s.Next()
	}

	if className != "" && styles != "" {
		styleMap := make(map[string]string)
		for _, style := range strings.Split(styles, ",") {
			kv := strings.Split(style, ":")
			if len(kv) >= 2 {
				styleMap[kv[0]] = kv[1]
			}
		}
		(*p.props.styleClasses)[className] = styleClass{className, styleMap}
	}
}

// parseStyleDirective parses: style nodeId styles...
func (p *graphParser) parseStyleDirective() {
	p.s.Next() // consume "style"
	p.s.SkipWhitespace()

	// Parse node ID
	nodeID := ""
	if p.s.Peek().Kind == parser.TokenIdent || p.s.Peek().Kind == parser.TokenNumber {
		nodeID = p.s.Next().Text
	}
	p.s.SkipWhitespace()

	// Collect rest of line as styles
	styles := strings.TrimSpace(parser.CollectLineText(p.s))
	if p.s.Peek().Kind == parser.TokenNewline {
		p.s.Next()
	}

	if nodeID != "" && styles != "" {
		anonClassName := "_style_" + nodeID
		styleMap := make(map[string]string)
		for _, style := range strings.Split(styles, ",") {
			kv := strings.Split(style, ":")
			if len(kv) == 2 {
				styleMap[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
		}
		(*p.props.styleClasses)[anonClassName] = styleClass{anonClassName, styleMap}
		if info, exists := p.props.nodeInfo[nodeID]; exists {
			info.styleClass = anonClassName
			p.props.nodeInfo[nodeID] = info
		} else {
			node := textNode{id: nodeID, name: nodeID, styleClass: anonClassName, shape: shapeRect}
			addNode(node, p.props.data, p.props.nodeInfo)
		}
	}
}

// parseLinkStyle parses: linkStyle N styles... (parsed but not applied)
func (p *graphParser) parseLinkStyle() {
	p.s.Next() // consume "linkStyle"
	p.s.SkipWhitespace()

	index := ""
	if p.s.Peek().Kind == parser.TokenNumber {
		index = p.s.Next().Text
	}
	p.s.SkipWhitespace()
	styles := strings.TrimSpace(parser.CollectLineText(p.s))
	if p.s.Peek().Kind == parser.TokenNewline {
		p.s.Next()
	}
	log.Debugf("linkStyle directive parsed: index=%s styles=%s", index, styles)
}

// snapshotNodes returns a set of all current node IDs.
func (p *graphParser) snapshotNodes() map[string]bool {
	existing := make(map[string]bool)
	for el := p.props.data.Front(); el != nil; el = el.Next() {
		existing[el.Key] = true
	}
	return existing
}

// addNewNodesToSubgraphs adds any nodes not in existingNodes to all subgraphs on the stack.
func (p *graphParser) addNewNodesToSubgraphs(existingNodes map[string]bool) {
	if len(p.subgraphStack) == 0 {
		return
	}
	for el := p.props.data.Front(); el != nil; el = el.Next() {
		nodeName := el.Key
		if !existingNodes[nodeName] {
			for _, sg := range p.subgraphStack {
				if !sg.nodeSet[nodeName] {
					sg.nodes = append(sg.nodes, nodeName)
					sg.nodeSet[nodeName] = true
					log.Debugf("Added node %s to subgraph %s", nodeName, sg.name)
				}
			}
		}
	}
}

// parseStatement dispatches to the appropriate parser based on the first token.
func (p *graphParser) parseStatement() {
	tok := p.s.Peek()
	if tok.Kind == parser.TokenIdent {
		switch strings.ToLower(tok.Text) {
		case "subgraph":
			p.parseSubgraph()
			return
		case "direction":
			p.parseDirection()
			return
		case "classdef":
			p.parseClassDef()
			return
		case "style":
			p.parseStyleDirective()
			return
		case "linkstyle":
			p.parseLinkStyle()
			return
		}
	}

	// Snapshot nodes for subgraph tracking, then parse chain
	existingNodes := p.snapshotNodes()
	p.parseChain()
	p.addNewNodesToSubgraphs(existingNodes)
}

// parseStatements loops parsing statements until "end" keyword or EOF.
func (p *graphParser) parseStatements() {
	for {
		p.s.SkipNewlines()
		if p.s.AtEnd() {
			return
		}
		if p.isEndKeyword() {
			return
		}
		p.parseStatement()
	}
}

// Parse parses a Mermaid graph or flowchart definition string into a Properties struct.
func Parse(mermaid string) (*Properties, error) {
	// Normalize escaped newlines and strip test file separator
	input := strings.ReplaceAll(mermaid, "\\n", "\n")
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		if line == "---" {
			lines = lines[:i]
			break
		}
	}
	input = strings.Join(lines, "\n")

	s := parser.NewScanner(input)

	data := orderedmap.NewOrderedMap[string, []textEdge]()
	styleClasses := make(map[string]styleClass)
	nodeInfo := make(map[string]textNode)
	properties := Properties{
		data:             data,
		nodeInfo:         nodeInfo,
		styleClasses:     &styleClasses,
		graphDirection:   "",
		styleType:        "cli",
		paddingX:         5,
		paddingY:         5,
		subgraphs:        []*textSubgraph{},
		boxBorderPadding: 1,
	}

	// Parse optional padding directives
	for {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}
		tok := s.Peek()
		if tok.Kind == parser.TokenIdent && len(tok.Text) == 8 {
			lower := strings.ToLower(tok.Text)
			if lower == "paddingx" || lower == "paddingy" {
				s.Next() // consume paddingX/Y
				s.SkipWhitespace()
				// Expect = operator
				opTok := s.Next()
				if opTok.Kind != parser.TokenOperator || opTok.Text != "=" {
					return &properties, fmt.Errorf("expected '=' after %s", tok.Text)
				}
				s.SkipWhitespace()
				numTok := s.Next()
				if numTok.Kind != parser.TokenNumber {
					return &properties, fmt.Errorf("expected number after %s =", tok.Text)
				}
				val, err := strconv.Atoi(numTok.Text)
				if err != nil {
					return &properties, err
				}
				if lower == "paddingx" {
					properties.paddingX = val
				} else {
					properties.paddingY = val
				}
				parser.SkipToEndOfLine(s)
				continue
			}
		}
		break
	}

	// Parse graph/flowchart declaration
	s.SkipNewlines()
	if s.AtEnd() {
		return &properties, errors.New("missing graph definition")
	}

	kwTok := s.Next()
	if kwTok.Kind != parser.TokenIdent || (kwTok.Text != "graph" && kwTok.Text != "flowchart") {
		return &properties, fmt.Errorf("unsupported graph type '%s'. Supported types: graph TD, graph TB, graph LR, flowchart TD, flowchart TB, flowchart LR, graph BT, flowchart BT, graph RL, flowchart RL", kwTok.Text)
	}
	s.SkipWhitespace()
	dirTok := s.Next()
	if dirTok.Kind != parser.TokenIdent {
		return &properties, fmt.Errorf("unsupported graph type '%s'. Supported types: graph TD, graph TB, graph LR, flowchart TD, flowchart TB, flowchart LR, graph BT, flowchart BT, graph RL, flowchart RL", kwTok.Text)
	}
	switch strings.ToUpper(dirTok.Text) {
	case "LR":
		properties.graphDirection = "LR"
	case "TD", "TB":
		properties.graphDirection = "TD"
	case "BT":
		properties.graphDirection = "BT"
	case "RL":
		properties.graphDirection = "RL"
	default:
		return &properties, fmt.Errorf("unsupported graph type '%s %s'. Supported types: graph TD, graph TB, graph LR, flowchart TD, flowchart TB, flowchart LR, graph BT, flowchart BT, graph RL, flowchart RL", kwTok.Text, dirTok.Text)
	}
	parser.SkipToEndOfLine(s)

	// Parse statements
	gp := &graphParser{
		s:             s,
		props:         &properties,
		subgraphStack: []*textSubgraph{},
	}
	gp.parseStatements()

	// Resolve edges that reference subgraph IDs
	properties.resolveSubgraphEdges()

	// Apply "classDef default" to all nodes that don't have a class already
	if _, hasDefault := styleClasses["default"]; hasDefault {
		for nodeID, info := range properties.nodeInfo {
			if info.styleClass == "" {
				info.styleClass = "default"
				properties.nodeInfo[nodeID] = info
			}
		}
	}

	return &properties, nil
}

// resolveSubgraphEdges rewrites edges that reference subgraph IDs so they
// point to the first node inside that subgraph instead.
func (gp *Properties) resolveSubgraphEdges() {
	if len(gp.subgraphs) == 0 {
		return
	}

	// Build a set of subgraph IDs for quick lookup
	sgIDs := make(map[string]bool)
	for _, sg := range gp.subgraphs {
		sgIDs[sg.id] = true
	}

	// For each key in the data map that is a subgraph ID, move its edges
	// to the resolved node.
	keysToResolve := []string{}
	for el := gp.data.Front(); el != nil; el = el.Next() {
		if sgIDs[el.Key] {
			keysToResolve = append(keysToResolve, el.Key)
		}
	}
	for _, key := range keysToResolve {
		resolved := gp.resolveSubgraphNode(key)
		if resolved != key {
			edges, _ := gp.data.Get(key)
			gp.data.Delete(key)
			if existingEdges, ok := gp.data.Get(resolved); ok {
				gp.data.Set(resolved, append(existingEdges, edges...))
			} else {
				gp.data.Set(resolved, edges)
			}
			// Update nodeInfo: ensure resolved node exists
			if _, exists := gp.nodeInfo[resolved]; !exists {
				gp.nodeInfo[resolved] = textNode{id: resolved, name: resolved, shape: shapeRect}
			}
			delete(gp.nodeInfo, key)
			log.Debugf("Resolved subgraph edge source %s -> node %s", key, resolved)
		}
	}

	// Also resolve child references within edges
	for el := gp.data.Front(); el != nil; el = el.Next() {
		edges := el.Value
		for i, edge := range edges {
			resolvedChild := gp.resolveSubgraphNode(edge.child.id)
			if resolvedChild != edge.child.id {
				if info, exists := gp.nodeInfo[resolvedChild]; exists {
					edges[i].child = info
				} else {
					edges[i].child = textNode{id: resolvedChild, name: resolvedChild, shape: shapeRect}
					gp.nodeInfo[resolvedChild] = edges[i].child
				}
				// Ensure resolved child is in data map
				if _, ok := gp.data.Get(resolvedChild); !ok {
					gp.data.Set(resolvedChild, []textEdge{})
				}
				log.Debugf("Resolved subgraph edge target %s -> node %s", edge.child.id, resolvedChild)
			}
		}
		gp.data.Set(el.Key, edges)
	}
}
