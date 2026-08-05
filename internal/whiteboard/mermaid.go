package whiteboard

import (
	"regexp"
	"strings"
)

// Shape identifiers used by the canvas (SystemDesignCanvas node types).
const (
	shapeRectangle = "rectangle"
	shapeDiamond   = "diamond"
	shapeDatabase  = "database"
	shapeGroup     = "group" // subgraph container (React Flow parent node)
)

type pNode struct {
	id          string
	shape       string
	label       string
	color       string // explicit fill from a style/classDef (overrides shape default)
	borderColor string // explicit stroke from a style/classDef
	cluster     string // innermost enclosing subgraph id ("" = top level)
}

// cluster is a parsed subgraph: a titled container that groups member nodes and
// can nest inside another cluster. Membership is recorded on pNode.cluster
// (innermost wins); nesting is recorded on cluster.parent. An edge may target a
// cluster id, in which case it attaches to the cluster's group node.
type cluster struct {
	id          string
	label       string
	parent      string // enclosing cluster id ("" = top level)
	direction   string // per-subgraph direction, if given (else inherits g.direction)
	color       string // fill from a classDef/style targeting the subgraph id
	borderColor string // stroke from a classDef/style targeting the subgraph id
}

// nodeStyle holds the color attributes we lift from a classDef or style
// statement. Only fill (→ node color) and stroke (→ border color) map onto the
// canvas; other attributes (stroke-width, text color, …) have no canvas field.
type nodeStyle struct {
	fill   string
	stroke string
}

type pEdge struct {
	from  string
	to    string
	label string
}

type flowGraph struct {
	direction string // "TD", "BT", "LR", "RL"
	order     []string
	nodes     map[string]*pNode
	edges     []pEdge
	classDefs map[string]nodeStyle // classDef name -> style

	clusters     map[string]*cluster // subgraph id -> cluster
	clusterOrder []string            // declaration order (outer before inner)
	clusterStack []string            // transient: subgraph nesting during parse
	subgraphSeq  int                 // transient: counter for auto-generated ids
}

// classAssign is a pending "class <ids> <name>" statement, applied after the
// main parse so it can reference nodes declared on later lines.
type classAssign struct {
	ids   []string
	class string
}

// styleStmt is a pending "style <id> <attrs>" statement, applied (after class
// assignments) so a direct style wins over a class, matching Mermaid.
type styleStmt struct {
	id    string
	style nodeStyle
}

var (
	headerRe = regexp.MustCompile(`(?i)^\s*(graph|flowchart)\b\s*(TB|TD|BT|LR|RL)?`)

	// Inline edge-label forms normalized to the pipe form before splitting:
	//   A -- text --> B   ==>  A -->|text| B
	inlineDashLabel = regexp.MustCompile(`--+\s+([^|>]+?)\s+--+>`)
	inlineDotLabel  = regexp.MustCompile(`-\.\s+([^|>]+?)\s+\.-*>`)
	inlineEqLabel   = regexp.MustCompile(`==+\s+([^|>]+?)\s+==+>`)

	// A connector plus an optional |label|. Order matters: more specific
	// (arrow) alternatives precede plainer ones so leftmost-first wins.
	connRe = regexp.MustCompile(`\s*(-\.-*>|-\.-+|--+>|--+|==+>|==+)\s*(?:\|([^|]*)\|\s*)?`)

	// A node reference: identifier plus an optional shape wrapper.
	nodeRefRe = regexp.MustCompile(`^([A-Za-z0-9_.\-]+)\s*(.*)$`)

	// HTML line breaks the model puts in labels (<br>, <br/>, <br />). Converted
	// to real newlines so the canvas renders separate lines instead of literal
	// text and node sizing can account for line count.
	brRe = regexp.MustCompile(`(?i)<br\s*/?>`)

	// Accepted color forms for sanitizeColor (values flow into canvas CSS).
	hexColorRe  = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	rgbColorRe  = regexp.MustCompile(`^rgba?\([0-9.,%]+\)$`)
	nameColorRe = regexp.MustCompile(`^[a-zA-Z]{1,20}$`)

	// Statement-leading keywords we skip entirely. style/classDef/class are NOT
	// here: they are routed to the styling collectors before this check. Nor are
	// subgraph/end/direction: they are handled explicitly to build clusters.
	skipPrefixes = []string{"click ", "linkstyle", "%%"}
)

// parseFlowchart parses the flowchart/graph subset we can map onto the canvas.
// ok is false when the header is not a flowchart. A best-effort graph is
// returned for recognized flowcharts even if some statements are skipped.
func parseFlowchart(src string) (flowGraph, bool) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	g := flowGraph{direction: "TD", nodes: map[string]*pNode{}, classDefs: defaultClassDefs(), clusters: map[string]*cluster{}}

	// Header line.
	headerFound := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		m := headerRe.FindStringSubmatch(t)
		if m == nil {
			return flowGraph{}, false
		}
		if d := strings.ToUpper(m[2]); d != "" {
			if d == "TB" {
				d = "TD"
			}
			g.direction = d
		}
		headerFound = true
		break
	}
	if !headerFound {
		return flowGraph{}, false
	}

	headerSkipped := false
	// Styling statements are collected and applied after the main parse so they
	// can reference nodes declared on later lines (forward references).
	var classAssigns []classAssign
	var styleStmts []styleStmt
	for _, raw := range lines {
		line := stripComment(raw)
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !headerSkipped && headerRe.MatchString(t) {
			headerSkipped = true
			continue
		}
		// A statement line may hold several statements separated by ';'.
		for _, stmt := range strings.Split(t, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			low := strings.ToLower(stmt)
			switch {
			case strings.HasPrefix(low, "classdef "):
				g.parseClassDef(stmt)
			case strings.HasPrefix(low, "class "):
				if ca, ok := parseClassAssign(stmt); ok {
					classAssigns = append(classAssigns, ca)
				}
			case strings.HasPrefix(low, "style "):
				if s, ok := parseStyleStmt(stmt); ok {
					styleStmts = append(styleStmts, s)
				}
			case low == "subgraph" || strings.HasPrefix(low, "subgraph ") || strings.HasPrefix(low, "subgraph["):
				g.openSubgraph(stmt)
			case low == "end":
				g.closeSubgraph()
			case strings.HasPrefix(low, "direction "):
				g.setDirection(stmt)
			case shouldSkip(stmt):
				continue
			default:
				g.parseStatement(stmt)
			}
		}
	}
	g.applyStyling(classAssigns, styleStmts)
	g.pruneClusterNodes()
	return g, true
}

func (g *flowGraph) parseStatement(stmt string) {
	stmt = inlineDashLabel.ReplaceAllString(stmt, "-->|$1|")
	stmt = inlineDotLabel.ReplaceAllString(stmt, "-.->|$1|")
	stmt = inlineEqLabel.ReplaceAllString(stmt, "==>|$1|")

	idx := connRe.FindAllStringSubmatchIndex(stmt, -1)
	if len(idx) == 0 {
		// No connector: a bare node declaration such as "A[Label]".
		for _, ref := range g.parseRefs(stmt) {
			_ = ref
		}
		return
	}

	// Segments are the text spans between connectors; labels[i] belongs to the
	// connector that follows segment i.
	var segments []string
	var labels []string
	segStart := 0
	for _, m := range idx {
		segments = append(segments, stmt[segStart:m[0]])
		label := ""
		if m[4] >= 0 {
			label = strings.TrimSpace(stmt[m[4]:m[5]])
		}
		labels = append(labels, label)
		segStart = m[1]
	}
	segments = append(segments, stmt[segStart:])

	// Resolve each segment to one or more node ids (split on '&').
	groups := make([][]string, len(segments))
	for i, seg := range segments {
		groups[i] = g.parseRefs(seg)
	}

	for i := 0; i+1 < len(groups); i++ {
		for _, from := range groups[i] {
			for _, to := range groups[i+1] {
				if from == "" || to == "" {
					continue
				}
				g.edges = append(g.edges, pEdge{from: from, to: to, label: labels[i]})
			}
		}
	}
}

// parseRefs turns a segment (possibly "A & B") into registered node ids.
func (g *flowGraph) parseRefs(seg string) []string {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return nil
	}
	var ids []string
	for _, part := range strings.Split(seg, "&") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, shape, label := parseNodeRef(part)
		if id == "" {
			continue
		}
		g.register(id, shape, label)
		ids = append(ids, id)
	}
	return ids
}

func (g *flowGraph) register(id, shape, label string) {
	n, ok := g.nodes[id]
	if !ok {
		n = &pNode{id: id, shape: shapeRectangle, label: id, cluster: g.curCluster()}
		g.nodes[id] = n
		g.order = append(g.order, id)
	}
	// First non-default definition wins for shape and label.
	if shape != "" {
		n.shape = shape
	}
	if label != "" {
		n.label = label
	}
}

// curCluster returns the innermost subgraph currently open during parsing, or ""
// at the top level.
func (g *flowGraph) curCluster() string {
	if n := len(g.clusterStack); n > 0 {
		return g.clusterStack[n-1]
	}
	return ""
}

// openSubgraph handles a "subgraph id[Title]" / "subgraph Title" statement: it
// records the cluster (nesting under any enclosing subgraph) and pushes it onto
// the stack so subsequent nodes/edges become members.
func (g *flowGraph) openSubgraph(stmt string) {
	rest := strings.TrimSpace(stmt[len("subgraph"):])
	id, title := g.parseSubgraphHeader(rest)
	if _, exists := g.clusters[id]; !exists {
		g.clusters[id] = &cluster{id: id, label: title, parent: g.curCluster()}
		g.clusterOrder = append(g.clusterOrder, id)
	}
	g.clusterStack = append(g.clusterStack, id)
}

// closeSubgraph pops the innermost open subgraph ("end"). A stray "end" is a
// no-op.
func (g *flowGraph) closeSubgraph() {
	if n := len(g.clusterStack); n > 0 {
		g.clusterStack = g.clusterStack[:n-1]
	}
}

// setDirection applies a "direction X" statement to the innermost open subgraph,
// or to the whole graph at the top level.
func (g *flowGraph) setDirection(stmt string) {
	fields := strings.Fields(stmt)
	if len(fields) < 2 {
		return
	}
	d := strings.ToUpper(fields[1])
	if d == "TB" {
		d = "TD"
	}
	switch d {
	case "TD", "BT", "LR", "RL":
	default:
		return
	}
	if c := g.curCluster(); c != "" {
		g.clusters[c].direction = d
	} else {
		g.direction = d
	}
}

// parseSubgraphHeader extracts an id and title from the text after "subgraph".
// Forms: "id", "id[Title]" / "id[\"Title\"]", "\"Title\"" or bare "two words"
// (both get a generated id). Mirrors Mermaid's leniency.
func (g *flowGraph) parseSubgraphHeader(rest string) (id, title string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return g.genSubgraphID(), ""
	}
	m := nodeRefRe.FindStringSubmatch(rest)
	if m == nil {
		// No leading identifier token (e.g. `subgraph "My Title"`).
		return g.genSubgraphID(), cleanLabel(rest)
	}
	id = m[1]
	wrap := strings.TrimSpace(m[2])
	if wrap == "" {
		return id, id
	}
	if _, label := shapeFromWrapper(wrap); label != "" {
		return id, label
	}
	// Trailing words that aren't a shape wrapper: a bare multi-word title.
	return g.genSubgraphID(), cleanLabel(rest)
}

func (g *flowGraph) genSubgraphID() string {
	id := "__sg" + itoa(g.subgraphSeq)
	g.subgraphSeq++
	return id
}

// pruneClusterNodes removes any leaf node accidentally registered for a subgraph
// id — e.g. an edge "A --> CI" that references the cluster before it is declared.
// The edge keeps the id and resolves to the cluster's group node at layout time.
func (g *flowGraph) pruneClusterNodes() {
	for id := range g.clusters {
		if _, ok := g.nodes[id]; !ok {
			continue
		}
		delete(g.nodes, id)
		for i, oid := range g.order {
			if oid == id {
				g.order = append(g.order[:i], g.order[i+1:]...)
				break
			}
		}
	}
}

// defaultClassDefs seeds built-in style classes so a diagram can color the
// common workflow roles with just `class <ids> fanOut|fanIn|decision` — no
// classDef line needed. An explicit classDef of the same name in the source
// overrides the built-in (it is written into the map after this seed).
func defaultClassDefs() map[string]nodeStyle {
	return map[string]nodeStyle{
		"fanOut":   {fill: "#8b5cf6", stroke: "#6d28d9"}, // purple
		"fanIn":    {fill: "#0ea5e9", stroke: "#0369a1"}, // blue
		"decision": {fill: "#f59e0b", stroke: "#b45309"}, // amber (if / branch)
	}
}

// parseClassDef records a "classDef name[,name...] fill:#f9f,stroke:#333" entry.
func (g *flowGraph) parseClassDef(stmt string) {
	rest := strings.TrimSpace(stmt[len("classDef"):])
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return
	}
	attrs := parseStyleAttrs(strings.Join(fields[1:], " "))
	for _, name := range strings.Split(fields[0], ",") {
		if name = strings.TrimSpace(name); name != "" {
			g.classDefs[name] = attrs
		}
	}
}

// parseClassAssign parses "class id1,id2 className" into a pending assignment.
func parseClassAssign(stmt string) (classAssign, bool) {
	fields := strings.Fields(strings.TrimSpace(stmt[len("class"):]))
	if len(fields) < 2 {
		return classAssign{}, false
	}
	class := fields[len(fields)-1]
	var ids []string
	for _, id := range strings.Split(strings.Join(fields[:len(fields)-1], " "), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return classAssign{}, false
	}
	return classAssign{ids: ids, class: class}, true
}

// parseStyleStmt parses "style id fill:#f96,stroke:#333" into a pending style.
func parseStyleStmt(stmt string) (styleStmt, bool) {
	fields := strings.Fields(strings.TrimSpace(stmt[len("style"):]))
	if len(fields) < 2 {
		return styleStmt{}, false
	}
	return styleStmt{id: fields[0], style: parseStyleAttrs(strings.Join(fields[1:], " "))}, true
}

// parseStyleAttrs pulls fill/stroke (sanitized) out of a comma-separated
// attribute list like "fill:#f96,stroke:#333,stroke-width:2px".
func parseStyleAttrs(s string) nodeStyle {
	var ns nodeStyle
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		val := sanitizeColor(kv[1])
		if val == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "fill":
			ns.fill = val
		case "stroke":
			ns.stroke = val
		}
	}
	return ns
}

// applyStyling resolves collected class assignments (via classDefs) and then
// direct style statements onto their nodes. Direct styles run last so they win
// over a class, matching Mermaid precedence. Nodes named only by a style are
// registered with the default shape.
func (g *flowGraph) applyStyling(classAssigns []classAssign, styleStmts []styleStmt) {
	apply := func(id string, ns nodeStyle) {
		if ns.fill == "" && ns.stroke == "" {
			return
		}
		// A style/class targeting a subgraph id colors the container, not a node.
		if c, ok := g.clusters[id]; ok {
			if ns.fill != "" {
				c.color = ns.fill
			}
			if ns.stroke != "" {
				c.borderColor = ns.stroke
			}
			return
		}
		n, ok := g.nodes[id]
		if !ok {
			g.register(id, "", "")
			n = g.nodes[id]
		}
		if ns.fill != "" {
			n.color = ns.fill
		}
		if ns.stroke != "" {
			n.borderColor = ns.stroke
		}
	}
	for _, ca := range classAssigns {
		ns, ok := g.classDefs[ca.class]
		if !ok {
			continue
		}
		for _, id := range ca.ids {
			apply(id, ns)
		}
	}
	for _, s := range styleStmts {
		apply(s.id, s.style)
	}
}

// sanitizeColor accepts only recognized CSS color forms (hex, rgb()/rgba(), or
// a short color keyword) so a crafted fill:/stroke: value can't inject arbitrary
// CSS into the browser canvas. Anything else returns "" (node keeps its default).
func sanitizeColor(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if s == "" || len(s) > 32 {
		return ""
	}
	if hexColorRe.MatchString(s) {
		return s
	}
	if compact := strings.ReplaceAll(s, " ", ""); rgbColorRe.MatchString(compact) {
		return compact
	}
	if nameColorRe.MatchString(s) {
		return strings.ToLower(s)
	}
	return ""
}

// parseNodeRef extracts the id, canvas shape and label from a node reference
// like "A[Label]", "B{Cond}", "C[(DB)]" or a bare "D".
func parseNodeRef(ref string) (id, shape, label string) {
	m := nodeRefRe.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return "", "", ""
	}
	id = m[1]
	wrap := strings.TrimSpace(m[2])
	if wrap == "" {
		return id, "", ""
	}
	shape, label = shapeFromWrapper(wrap)
	return id, shape, label
}

// shapeFromWrapper maps a mermaid shape wrapper to a canvas shape + label.
func shapeFromWrapper(w string) (shape, label string) {
	inner := func(open, close string) (string, bool) {
		if strings.HasPrefix(w, open) && strings.HasSuffix(w, close) && len(w) >= len(open)+len(close) {
			return w[len(open) : len(w)-len(close)], true
		}
		return "", false
	}
	// Order: check compound delimiters before single ones.
	switch {
	case hasWrap(w, "[(", ")]"):
		s, _ := inner("[(", ")]")
		return shapeDatabase, cleanLabel(s)
	case hasWrap(w, "((", "))"):
		s, _ := inner("((", "))")
		return shapeRectangle, cleanLabel(s)
	case hasWrap(w, "([", "])"):
		s, _ := inner("([", "])")
		return shapeRectangle, cleanLabel(s)
	case hasWrap(w, "[[", "]]"):
		s, _ := inner("[[", "]]")
		return shapeRectangle, cleanLabel(s)
	case hasWrap(w, "{{", "}}"):
		s, _ := inner("{{", "}}")
		return shapeDiamond, cleanLabel(s)
	case hasWrap(w, "{", "}"):
		s, _ := inner("{", "}")
		return shapeDiamond, cleanLabel(s)
	case hasWrap(w, "[", "]"):
		s, _ := inner("[", "]")
		return shapeRectangle, cleanLabel(s)
	case hasWrap(w, "(", ")"):
		s, _ := inner("(", ")")
		return shapeRectangle, cleanLabel(s)
	default:
		return "", ""
	}
}

func hasWrap(w, open, close string) bool {
	return strings.HasPrefix(w, open) && strings.HasSuffix(w, close) && len(w) >= len(open)+len(close)
}

func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = brRe.ReplaceAllString(s, "\n")
	// Trim surrounding whitespace on each line so "<br/> coverage" doesn't keep a
	// leading space, and drop empty leading/trailing lines from stray breaks.
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	s = strings.Join(lines, "\n")
	return strings.Trim(s, "\n")
}

func stripComment(line string) string {
	if i := strings.Index(line, "%%"); i >= 0 {
		return line[:i]
	}
	return line
}

func shouldSkip(stmt string) bool {
	low := strings.ToLower(stmt)
	for _, p := range skipPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}
