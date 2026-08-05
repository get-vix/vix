package graph

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// nameLines splits a node's display name into lines (split on newline).
func (n *node) nameLines() []string {
	return strings.Split(n.name, "\n")
}

// nameWidth returns the width of the widest line in a node's display name.
func (n *node) nameWidth() int {
	w := 0
	for _, line := range n.nameLines() {
		if len(line) > w {
			w = len(line)
		}
	}
	return w
}

// nameHeight returns the number of lines in a node's display name.
func (n *node) nameHeight() int {
	return len(n.nameLines())
}

type node struct {
	id             string // unique identifier (map key, e.g. "A")
	name           string // display label (e.g. "text" from A[text])
	drawing        *drawing
	drawingCoord   *drawingCoord
	gridCoord      *gridCoord
	drawn          bool
	index          int // Index of the node in the graph.nodes slice
	styleClassName string
	styleClass     styleClass
	shape          nodeShape
}

func (n node) String() string {
	return n.name
}

func (n *node) setCoord(c *drawingCoord) {
	n.drawingCoord = c
}

func (n *node) setDrawing(g graph) *drawing {
	d := drawShape(n, g)
	n.drawing = d
	return d
}

// wordWrap wraps text to fit within maxWidth characters.
// It preserves existing newlines, wraps at word boundaries when possible,
// and falls back to character-level breaks for words longer than maxWidth.
func wordWrap(text string, maxWidth int) string {
	if maxWidth <= 0 {
		maxWidth = 1
	}
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		if len(line) <= maxWidth {
			result = append(result, line)
			continue
		}
		// Wrap this line
		for len(line) > maxWidth {
			// Find last space within maxWidth
			breakAt := -1
			for i := maxWidth; i >= 0; i-- {
				if i < len(line) && line[i] == ' ' {
					breakAt = i
					break
				}
			}
			if breakAt <= 0 {
				// No space found, break at maxWidth
				breakAt = maxWidth
				result = append(result, line[:breakAt])
				line = line[breakAt:]
			} else {
				result = append(result, line[:breakAt])
				line = line[breakAt+1:] // skip the space
			}
		}
		if len(line) > 0 {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// shapeExtraWidth returns the extra width a shape adds beyond text + border padding.
func shapeExtraWidth(shape nodeShape, boxBorderPadding int) int {
	switch shape {
	case shapeHexagon:
		return 4
	case shapeSubroutine:
		return 2
	case shapeFlag:
		return 2
	default:
		return 0
	}
}

// longestWord returns the length of the longest whitespace-delimited word in text.
func longestWord(text string) int {
	max := 0
	for _, line := range strings.Split(text, "\n") {
		for _, word := range strings.Fields(line) {
			if len(word) > max {
				max = len(word)
			}
		}
	}
	if max == 0 {
		max = 1
	}
	return max
}

// constrainToTargetWidth adjusts column widths and wraps node text
// to try to fit the diagram within the target width.
// Phase 1: reduce padding/spacing columns. Phase 2: wrap node text.
func (g *graph) constrainToTargetWidth() {
	if g.targetWidth <= 0 {
		return
	}

	sumWidth := func() int {
		total := 0
		for _, w := range g.columnWidth {
			total += w
		}
		return total
	}

	totalBefore := sumWidth()
	if totalBefore <= g.targetWidth {
		return
	}

	// Classify columns
	type colClass int
	const (
		colOther   colClass = iota // padding, edge paths
		colBorder                  // node border columns
		colContent                 // node content columns
	)

	classification := make(map[int]colClass)
	contentColNodes := make(map[int][]*node)

	for _, n := range g.nodes {
		if n.gridCoord == nil {
			continue
		}
		classification[n.gridCoord.x] = colBorder
		classification[n.gridCoord.x+2] = colBorder
		classification[n.gridCoord.x+1] = colContent
		contentColNodes[n.gridCoord.x+1] = append(contentColNodes[n.gridCoord.x+1], n)
	}

	// Build set of edge label column minimums
	edgeLabelMins := make(map[int]int)
	for _, e := range g.edges {
		if len(e.text) == 0 || len(e.labelLine) < 2 {
			continue
		}
		minX, maxX := e.labelLine[0].x, e.labelLine[1].x
		if minX > maxX {
			minX, maxX = maxX, minX
		}
		middleX := minX + (maxX-minX)/2
		labelMin := len(e.text) + 2
		if labelMin > edgeLabelMins[middleX] {
			edgeLabelMins[middleX] = labelMin
		}
	}

	// Phase 1: Reduce padding/spacing columns
	// Padding columns are "other" columns (not border, not content)
	const minPadding = 1
	for col, w := range g.columnWidth {
		if classification[col] != colOther {
			continue
		}
		// Don't shrink below edge label minimums
		minW := minPadding
		if elm, ok := edgeLabelMins[col]; ok && elm > minW {
			minW = elm
		}
		if w > minW {
			g.columnWidth[col] = minW
		}
	}

	if sumWidth() <= g.targetWidth {
		return
	}

	// Phase 2: Wrap node text in content columns
	// Compute minimum text width per content column (longest word across all nodes)
	const minTextChars = 5 // absolute minimum readable width
	excess := sumWidth() - g.targetWidth

	// Compute total shrinkable width from content columns
	type contentColInfo struct {
		col         int
		currentWidth int
		minWidth    int // minimum based on longest word + shape extras
	}
	var contentCols []contentColInfo
	totalShrinkable := 0

	for col, nodes := range contentColNodes {
		currentW := g.columnWidth[col]
		// Find the minimum text width (longest word across nodes in this column)
		maxLongestWord := minTextChars
		maxShapeExtra := 0
		for _, n := range nodes {
			lw := longestWord(n.name)
			if lw > maxLongestWord {
				maxLongestWord = lw
			}
			extra := shapeExtraWidth(n.shape, g.boxBorderPadding)
			if extra > maxShapeExtra {
				maxShapeExtra = extra
			}
		}
		minW := 2*g.boxBorderPadding + maxLongestWord + maxShapeExtra
		// Also respect edge label minimums on this column
		if elm, ok := edgeLabelMins[col]; ok && elm > minW {
			minW = elm
		}
		if minW >= currentW {
			continue // can't shrink this column
		}
		contentCols = append(contentCols, contentColInfo{
			col:         col,
			currentWidth: currentW,
			minWidth:    minW,
		})
		totalShrinkable += currentW - minW
	}

	if totalShrinkable <= 0 {
		return
	}

	shrinkRatio := float64(excess) / float64(totalShrinkable)
	if shrinkRatio > 1.0 {
		shrinkRatio = 1.0
	}

	// Shrink content columns and wrap text
	for _, ci := range contentCols {
		reduction := int(float64(ci.currentWidth-ci.minWidth) * shrinkRatio)
		newColWidth := ci.currentWidth - reduction

		// Wrap all nodes in this column
		for _, n := range contentColNodes[ci.col] {
			extra := shapeExtraWidth(n.shape, g.boxBorderPadding)
			maxTextWidth := newColWidth - 2*g.boxBorderPadding - extra
			if maxTextWidth < 1 {
				maxTextWidth = 1
			}
			if n.nameWidth() > maxTextWidth {
				n.name = wordWrap(n.name, maxTextWidth)
			}
		}

		// Recalculate column width from actual wrapped text
		maxNeeded := 0
		for _, n := range contentColNodes[ci.col] {
			extra := shapeExtraWidth(n.shape, g.boxBorderPadding)
			needed := 2*g.boxBorderPadding + n.nameWidth() + extra
			if n.shape == shapeCircle {
				heightNeeded := n.nameHeight() + 2*g.boxBorderPadding
				if needed < heightNeeded+2 {
					needed = n.nameWidth() + 2*g.boxBorderPadding + 2
				}
			}
			if needed > maxNeeded {
				maxNeeded = needed
			}
		}
		if maxNeeded > 0 {
			g.columnWidth[ci.col] = maxNeeded
		}
	}

	// Recalculate row heights (wrapping may have increased line counts)
	for _, n := range g.nodes {
		if n.gridCoord == nil {
			continue
		}
		rowHeight := n.nameHeight() + 2*g.boxBorderPadding
		yCoord := n.gridCoord.y + 1
		g.rowHeight[yCoord] = Max(g.rowHeight[yCoord], rowHeight)
	}
}

func (g *graph) setColumnWidth(n *node) {
	// For every node there are three columns:
	// - 2 lines of border
	// - 1 line of text
	// - 2x padding
	// - 2x margin
	col1 := 1
	col2 := 2*g.boxBorderPadding + n.nameWidth()
	col3 := 1

	// Shapes that need extra width
	switch n.shape {
	case shapeHexagon:
		// Hexagon has angled edges on left/right
		col2 += 2 * 2 // 2 extra chars per side for the angled edges
	case shapeSubroutine:
		// Subroutine has double vertical borders
		col2 += 2 // extra char on each side
	case shapeCircle:
		// Circle needs width >= height for symmetry
		minTextWidth := n.nameWidth() + 2*g.boxBorderPadding
		heightNeeded := n.nameHeight() + 2*g.boxBorderPadding
		if col2 < heightNeeded+2 {
			col2 = minTextWidth + 2
		}
	case shapeFlag:
		// Flag has a pointed left side
		col2 += 2 // extra space for the point
	}

	colsToBePlaced := []int{col1, col2, col3}

	rowHeight := n.nameHeight() + 2*g.boxBorderPadding
	rowsToBePlaced := []int{1, rowHeight, 1} // Border, padding + line, border

	for idx, col := range colsToBePlaced {
		// Set new width for column if the size increased
		xCoord := n.gridCoord.x + idx
		g.columnWidth[xCoord] = Max(g.columnWidth[xCoord], col)
	}

	for idx, row := range rowsToBePlaced {
		// Set new width for column if the size increased
		yCoord := n.gridCoord.y + idx
		g.rowHeight[yCoord] = Max(g.rowHeight[yCoord], row)
	}

	// Set padding before node
	if n.gridCoord.x > 0 {
		g.columnWidth[n.gridCoord.x-1] = g.paddingX // TODO: x2?
	}
	if n.gridCoord.y > 0 {
		basePadding := g.paddingY

		// Add extra padding if node is in a subgraph AND has incoming edges from outside
		// This accounts for subgraph visual overhead (border, label, padding)
		// and allows arrows to continue as | for longer before becoming arrow heads
		if g.hasIncomingEdgeFromOutsideSubgraph(n) {
			const subgraphOverhead = 4
			basePadding += subgraphOverhead
			log.Debugf("Adding subgraph overhead of %d to rowHeight before node %s", subgraphOverhead, n.name)
		}

		// Use Max to preserve the largest padding requirement for this row
		// (multiple nodes may share the same Y coordinate)
		g.rowHeight[n.gridCoord.y-1] = Max(g.rowHeight[n.gridCoord.y-1], basePadding)
	}
}

func (g *graph) increaseGridSizeForPath(path []gridCoord) {
	for _, c := range path {
		if _, exists := g.columnWidth[c.x]; !exists {
			g.columnWidth[c.x] = g.paddingX / 2
		}
		if _, exists := g.rowHeight[c.y]; !exists {
			g.rowHeight[c.y] = g.paddingY / 2
		}
	}
}

func (g *graph) reserveSpotInGrid(n *node, requestedCoord *gridCoord) *gridCoord {
	if g.grid[*requestedCoord] != nil {
		log.Debugf("Coord %d,%d is already taken", requestedCoord.x, requestedCoord.y)
		// Next column is 4 coords further. This is because every node is 3 coords wide + 1 coord inbetween.
		if g.graphDirection == "LR" {
			return g.reserveSpotInGrid(n, &gridCoord{x: requestedCoord.x, y: requestedCoord.y + 4})
		} else {
			return g.reserveSpotInGrid(n, &gridCoord{x: requestedCoord.x + 4, y: requestedCoord.y})
		}
	}
	// Reserve border + middle + border for node
	log.Debugf("Reserving coord %v for node %v", requestedCoord, n)
	for x := 0; x < 3; x++ {
		for y := 0; y < 3; y++ {
			reservedCoord := gridCoord{x: requestedCoord.x + x, y: requestedCoord.y + y}
			g.grid[reservedCoord] = n
		}
	}
	n.gridCoord = requestedCoord
	return requestedCoord
}
