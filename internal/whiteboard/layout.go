package whiteboard

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Gaps (in px) between boxes on the canvas. Placement is size-aware: strides are
// derived from actual node dimensions (see layout) plus these gaps, so growing a
// box to fit its label can never make it overlap a neighbor. rankGap is larger
// to leave room for edge labels drawn between ranks.
const (
	crossGap = 70
	rankGap  = 90

	// Subgraph container insets: clusterTitleH reserves space for the title at the
	// top; clusterPad pads the other three sides. A cluster's children are placed
	// at (clusterPad, clusterTitleH) inside the group box, and the box is sized to
	// content plus these margins.
	clusterPad    = 24
	clusterTitleH = 34
)

// Label metrics approximating the Virgil font the web canvas renders in. Used to
// size boxes to their content; deliberately rough — a slight over-estimate keeps
// text off the border.
const (
	labelCharW = 9  // px per character
	labelLineH = 22 // px per line
)

// xIterations is how many barycenter sweeps assignX runs. A handful is enough
// for the small flowcharts we render to settle into a centered layout.
const xIterations = 8

var shapeColors = map[string][2]string{
	shapeRectangle: {"#3b82f6", "#1d4ed8"},
	shapeDiamond:   {"#8b5cf6", "#6d28d9"},
	shapeDatabase:  {"#10b981", "#059669"},
}

// nodeSize estimates the box size (px) needed to fit label for the given shape.
// The label may contain newlines (from <br/> conversion). Short labels keep the
// per-shape minimums, so existing diagrams look unchanged; longer or multi-line
// labels grow the box. Sizes are the source of truth the web canvas renders to.
func nodeSize(label, shape string) (int, int) {
	lines := strings.Split(label, "\n")
	longest := 0
	for _, ln := range lines {
		if n := utf8.RuneCountInString(ln); n > longest {
			longest = n
		}
	}
	contentW := longest * labelCharW
	contentH := len(lines) * labelLineH

	switch shape {
	case shapeDiamond:
		// Text sits in the inscribed area (~60% of the box), so scale up.
		w := int(float64(contentW)*1.7) + 48
		h := int(float64(contentH)*1.7) + 48
		return maxInt(w, 128), maxInt(h, 128)
	case shapeDatabase:
		// Cylinder caps eat vertical space (py-8 in the node), so pad height.
		return maxInt(contentW+48, 128), maxInt(contentH+72, 140)
	default: // rectangle
		return maxInt(contentW+32, 150), maxInt(contentH+32, 80)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// placement is a node/cluster's laid-out box. For a node inside a cluster, x/y
// are relative to that cluster's group box (React Flow parent/child model); for
// a top-level node they are absolute.
type placement struct{ x, y, w, h int }

// layout assigns coordinates to a parsed flowchart. It lays out each subgraph
// container recursively: a container's members and immediate sub-clusters are
// ranked by longest path (cycles broken so a back-edge can't inflate the ranks),
// spread across each rank with a barycenter pass, and placed by direction. A
// sub-cluster is collapsed to a single sized meta-node in its parent's layout,
// then its own children are positioned relative to it — so members of a subgraph
// always stay grouped. Group container nodes are emitted first (parents before
// children) so React Flow can attach children to a defined parent.
func layout(g flowGraph) ([]Node, []Edge) {
	place := make(map[string]placement, len(g.order)+len(g.clusterOrder))

	var layoutContainer func(container string) (int, int)
	layoutContainer = func(container string) (int, int) {
		var leaves []string
		for _, id := range g.order {
			if g.nodes[id].cluster == container {
				leaves = append(leaves, id)
			}
		}
		var subs []string
		for _, cid := range g.clusterOrder {
			if g.clusters[cid].parent == container {
				subs = append(subs, cid)
			}
		}

		sizes := make(map[string][2]int, len(leaves)+len(subs))
		// Size sub-clusters first (recursively) so they can be placed as meta-nodes.
		for _, s := range subs {
			cw, ch := layoutContainer(s)
			sizes[s] = [2]int{cw + 2*clusterPad, ch + clusterTitleH + clusterPad}
		}
		for _, id := range leaves {
			w, h := nodeSize(g.nodes[id].label, g.nodes[id].shape)
			sizes[id] = [2]int{w, h}
		}

		items := make([]string, 0, len(leaves)+len(subs))
		items = append(items, leaves...)
		items = append(items, subs...)

		pos, cw, ch := layerAndPlace(items, g.inducedEdges(container), sizes, g.dirOf(container))
		ox, oy := contentOffset(container)
		for _, it := range items {
			place[it] = placement{x: pos[it][0] + ox, y: pos[it][1] + oy, w: sizes[it][0], h: sizes[it][1]}
		}
		return cw, ch
	}
	layoutContainer("")

	nodes := make([]Node, 0, len(g.order)+len(g.clusterOrder))
	// Container nodes first, parents before children (clusterOrder is declaration
	// order, which nests outer before inner), so a child's parentId is already
	// defined when React Flow ingests it.
	for _, cid := range g.clusterOrder {
		c := g.clusters[cid]
		p := place[cid]
		nodes = append(nodes, Node{
			ID:            cid,
			Shape:         shapeGroup,
			ParentID:      c.parent,
			X:             p.x,
			Y:             p.y,
			Width:         p.w,
			Height:        p.h,
			Label:         c.label,
			Color:         c.color,
			BorderColor:   c.borderColor,
			TextAlignment: "left",
		})
	}
	for _, id := range g.order {
		pn := g.nodes[id]
		p := place[id]
		colors := shapeColors[pn.shape]
		if colors == [2]string{} {
			colors = shapeColors[shapeRectangle]
		}
		color, border := colors[0], colors[1]
		if pn.color != "" {
			color = pn.color
		}
		if pn.borderColor != "" {
			border = pn.borderColor
		}
		nodes = append(nodes, Node{
			ID:            id,
			Shape:         pn.shape,
			ParentID:      pn.cluster,
			X:             p.x,
			Y:             p.y,
			Width:         p.w,
			Height:        p.h,
			Label:         pn.label,
			Color:         color,
			BorderColor:   border,
			TextAlignment: "center",
		})
	}

	fromH, toH := handles(g.direction)
	edges := make([]Edge, 0, len(g.edges))
	for i, pe := range g.edges {
		edges = append(edges, Edge{
			ID:         edgeID(i),
			From:       pe.from,
			FromHandle: fromH,
			To:         pe.to,
			ToHandle:   toH,
			Label:      pe.label,
		})
	}
	return nodes, edges
}

// contentOffset is the top-left inset of a container's content area within its
// group box. Top-level content ("") has no box, so no inset.
func contentOffset(container string) (int, int) {
	if container == "" {
		return 0, 0
	}
	return clusterPad, clusterTitleH
}

// dirOf returns the layout direction for a container: a subgraph's own direction
// if set, else the diagram direction.
func (g *flowGraph) dirOf(container string) string {
	if container != "" {
		if d := g.clusters[container].direction; d != "" {
			return d
		}
	}
	return g.direction
}

// parentContainer returns the enclosing container of a node or cluster id, and
// whether the id is known.
func (g *flowGraph) parentContainer(id string) (string, bool) {
	if n, ok := g.nodes[id]; ok {
		return n.cluster, true
	}
	if c, ok := g.clusters[id]; ok {
		return c.parent, true
	}
	return "", false
}

// repIn returns the direct child of container that is id or an ancestor of id
// (the "representative" of id at this level), or "" when id is not inside
// container. An edge endpoint that is a subgraph id resolves to that group node.
func (g *flowGraph) repIn(id, container string) string {
	cur := id
	for {
		p, ok := g.parentContainer(cur)
		if !ok {
			return ""
		}
		if p == container {
			return cur
		}
		if p == "" {
			return ""
		}
		cur = p
	}
}

// inducedEdges projects the graph's edges onto one container: an edge between two
// distinct direct children of the container (each endpoint mapped to its
// representative). Edges wholly inside a sub-cluster, or leaving the container,
// are handled at their own level.
func (g *flowGraph) inducedEdges(container string) []pEdge {
	var out []pEdge
	for _, e := range g.edges {
		a := g.repIn(e.from, container)
		b := g.repIn(e.to, container)
		if a == "" || b == "" || a == b {
			continue
		}
		out = append(out, pEdge{from: a, to: b})
	}
	return out
}

// layerAndPlace ranks and positions one container's items (nodes and sub-cluster
// meta-nodes) with the size-aware layered algorithm, returning each item's
// top-left position normalized to a (0,0) origin plus the content bounds.
func layerAndPlace(order []string, edges []pEdge, sizes map[string][2]int, direction string) (map[string][2]int, int, int) {
	back := backEdges(order, edges)
	ranks := rankNodes(order, edges, back)

	maxRank := 0
	for _, r := range ranks {
		if r > maxRank {
			maxRank = r
		}
	}

	horizontal := direction == "LR" || direction == "RL"
	reverse := direction == "BT" || direction == "RL"

	cross := assignCross(order, edges, ranks, back)

	erank := make(map[string]int, len(order))
	maxER := 0
	for _, id := range order {
		er := ranks[id]
		if reverse {
			er = maxRank - er
		}
		erank[id] = er
		if er > maxER {
			maxER = er
		}
	}

	rankDim := make([]int, maxER+1)
	crossMax := 0
	for _, id := range order {
		w, h := sizes[id][0], sizes[id][1]
		if horizontal {
			if h > crossMax {
				crossMax = h
			}
			if w > rankDim[erank[id]] {
				rankDim[erank[id]] = w
			}
		} else {
			if w > crossMax {
				crossMax = w
			}
			if h > rankDim[erank[id]] {
				rankDim[erank[id]] = h
			}
		}
	}
	crossStride := crossMax + crossGap
	rankOffset := make([]int, maxER+1)
	acc := 0
	for r := 0; r <= maxER; r++ {
		rankOffset[r] = acc
		acc += rankDim[r] + rankGap
	}

	pos := make(map[string][2]int, len(order))
	for _, id := range order {
		var x, y int
		if horizontal {
			x = rankOffset[erank[id]]
			y = int(math.Round(cross[id] * float64(crossStride)))
		} else {
			x = int(math.Round(cross[id] * float64(crossStride)))
			y = rankOffset[erank[id]]
		}
		pos[id] = [2]int{x, y}
	}

	if len(order) == 0 {
		return pos, 0, 0
	}
	// Normalize to a (0,0) origin and measure content bounds so a parent can size
	// its box and place this container relative to its own top-left.
	minX, minY := math.MaxInt, math.MaxInt
	maxX, maxY := math.MinInt, math.MinInt
	for _, id := range order {
		p := pos[id]
		w, h := sizes[id][0], sizes[id][1]
		if p[0] < minX {
			minX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[0]+w > maxX {
			maxX = p[0] + w
		}
		if p[1]+h > maxY {
			maxY = p[1] + h
		}
	}
	for id, p := range pos {
		pos[id] = [2]int{p[0] - minX, p[1] - minY}
	}
	return pos, maxX - minX, maxY - minY
}

// backEdges classifies feedback (back) edges via DFS: an edge into a node still
// on the recursion stack closes a cycle. Removing these makes the remaining
// graph a DAG so longest-path ranking can't inflate. Self-loops count as back
// edges too. Roots are visited in ascending in-degree order (ties by declaration
// order) so a genuine entry point is rooted before a feedback edge can reach it
// as a tree edge and sink it below its own successors; each node's out-edges are
// visited in edge order for determinism.
func backEdges(order []string, edges []pEdge) map[int]bool {
	type outEdge struct {
		idx int
		to  string
	}
	adj := make(map[string][]outEdge, len(order))
	indeg := make(map[string]int, len(order))
	for _, id := range order {
		indeg[id] = 0
	}
	for i, e := range edges {
		adj[e.from] = append(adj[e.from], outEdge{i, e.to})
		indeg[e.to]++
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(order))
	back := map[int]bool{}
	var visit func(string)
	visit = func(u string) {
		color[u] = gray
		for _, oe := range adj[u] {
			switch color[oe.to] {
			case gray:
				back[oe.idx] = true
			case white:
				visit(oe.to)
			}
		}
		color[u] = black
	}
	idx := make(map[string]int, len(order))
	for i, id := range order {
		idx[id] = i
	}
	roots := make([]string, len(order))
	copy(roots, order)
	sort.SliceStable(roots, func(a, b int) bool {
		ra, rb := roots[a], roots[b]
		if indeg[ra] != indeg[rb] {
			return indeg[ra] < indeg[rb]
		}
		return idx[ra] < idx[rb]
	})
	for _, id := range roots {
		if color[id] == white {
			visit(id)
		}
	}
	return back
}

// assignCross places each node on the cross-axis (in slot units) so parents sit
// centered over their children. It seeds each rank left-to-right, then runs
// alternating down/up barycenter sweeps: a node moves to the mean of its
// neighbors on the adjacent rank, and each rank is de-overlapped with an
// isotonic pass that keeps nodes at least one slot apart while staying as close
// as possible to their desired centers.
func assignCross(order []string, edges []pEdge, ranks map[string]int, back map[int]bool) map[string]float64 {
	maxRank := 0
	for _, r := range ranks {
		if r > maxRank {
			maxRank = r
		}
	}
	layers := make([][]string, maxRank+1)
	for _, id := range order {
		layers[ranks[id]] = append(layers[ranks[id]], id)
	}

	preds := map[string][]string{}
	succs := map[string][]string{}
	for i, e := range edges {
		if back[i] || e.from == e.to {
			continue
		}
		succs[e.from] = append(succs[e.from], e.to)
		preds[e.to] = append(preds[e.to], e.from)
	}

	pos := make(map[string]float64, len(order))
	for _, layer := range layers {
		for i, id := range layer {
			pos[id] = float64(i)
		}
	}

	for it := 0; it < xIterations; it++ {
		if it%2 == 0 {
			for r := 1; r < len(layers); r++ {
				placeLayer(layers[r], pos, preds)
			}
		} else {
			for r := len(layers) - 2; r >= 0; r-- {
				placeLayer(layers[r], pos, succs)
			}
		}
	}
	return pos
}

// placeLayer positions one rank: each node's desired center is the mean of its
// neighbors' positions (nodes without neighbors keep their current spot). Nodes
// are ordered by desired center (crossing reduction, reordering layer in place),
// then separated by at least one slot via an isotonic (pool-adjacent-violators)
// pass that minimizes total displacement — yielding a symmetric, centered fan.
func placeLayer(layer []string, pos map[string]float64, neigh map[string][]string) {
	if len(layer) == 0 {
		return
	}
	type item struct {
		id      string
		desired float64
	}
	items := make([]item, len(layer))
	for i, id := range layer {
		ns := neigh[id]
		if len(ns) == 0 {
			items[i] = item{id, pos[id]}
			continue
		}
		sum := 0.0
		for _, n := range ns {
			sum += pos[n]
		}
		items[i] = item{id, sum / float64(len(ns))}
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].desired < items[b].desired })

	// Enforce a minimum gap of one slot while staying as close as possible to
	// each desired center. Substituting e[i] = desired[i] - i turns the gap
	// constraint into "non-decreasing", solved optimally by isotonic regression.
	e := make([]float64, len(items))
	for i := range items {
		e[i] = items[i].desired - float64(i)
	}
	y := isotonic(e)
	for i := range items {
		pos[items[i].id] = y[i] + float64(i)
		layer[i] = items[i].id
	}
}

// isotonic returns the least-squares non-decreasing fit of v using the
// pool-adjacent-violators algorithm.
func isotonic(v []float64) []float64 {
	type block struct {
		sum float64
		cnt int
	}
	var blocks []block
	for _, x := range v {
		b := block{x, 1}
		for len(blocks) > 0 {
			last := blocks[len(blocks)-1]
			if last.sum/float64(last.cnt) <= b.sum/float64(b.cnt) {
				break
			}
			blocks = blocks[:len(blocks)-1]
			b = block{last.sum + b.sum, last.cnt + b.cnt}
		}
		blocks = append(blocks, b)
	}
	out := make([]float64, 0, len(v))
	for _, b := range blocks {
		val := b.sum / float64(b.cnt)
		for i := 0; i < b.cnt; i++ {
			out = append(out, val)
		}
	}
	return out
}

// rankNodes computes a longest-path rank per node via bounded relaxation over
// forward (non-back) edges only, so cycles can't inflate the ranks.
func rankNodes(order []string, edges []pEdge, back map[int]bool) map[string]int {
	rank := make(map[string]int, len(order))
	for _, id := range order {
		rank[id] = 0
	}
	for iter := 0; iter < len(order); iter++ {
		changed := false
		for i, e := range edges {
			if back[i] {
				continue
			}
			if rank[e.to] < rank[e.from]+1 {
				rank[e.to] = rank[e.from] + 1
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return rank
}

// handles returns the (from, to) node sides for the given layout direction.
func handles(direction string) (string, string) {
	switch direction {
	case "LR":
		return "right", "left"
	case "RL":
		return "left", "right"
	case "BT":
		return "top", "bottom"
	default: // TD / TB
		return "bottom", "top"
	}
}

func edgeID(i int) string {
	return "edge_" + itoa(i+1)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}
