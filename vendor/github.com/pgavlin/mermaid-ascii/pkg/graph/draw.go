package graph

import (
	"fmt"
	"strings"

	"github.com/gookit/color"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
	log "github.com/sirupsen/logrus"
)

var junctionChars = []string{
	"─", // Horizontal line
	"│", // Vertical line
	"┌", // Top-left corner
	"┐", // Top-right corner
	"└", // Bottom-left corner
	"┘", // Bottom-right corner
	"├", // T-junction pointing right
	"┤", // T-junction pointing left
	"┬", // T-junction pointing down
	"┴", // T-junction pointing up
	"┼", // Cross junction
	"╴", // Left end of horizontal line
	"╵", // Top end of vertical line
	"╶", // Right end of horizontal line
	"╷", // Bottom end of vertical line
	"═", // Double horizontal (decision box border)
	"║", // Double vertical (decision box border)
}

type drawing [][]string

type styleClass struct {
	name   string
	styles map[string]string
}

func (g *graph) drawNode(n *node) {
	log.Debug("Drawing node ", n.name, " at ", *n.drawingCoord)
	m := g.mergeDrawings(g.drawing, *n.drawingCoord, n.drawing)
	g.drawing = m
}

func (g *graph) drawEdge(e *edge) (*drawing, *drawing, *drawing, *drawing, *drawing) {
	from := e.from.gridCoord.Direction(e.startDir)
	to := e.to.gridCoord.Direction(e.endDir)
	log.Debugf("Drawing edge between %v (direction %v) and %v (direction %v)", *e.from, e.startDir, *e.to, e.endDir)
	return g.drawArrow(from, to, e)
}

func (d *drawing) drawText(start drawingCoord, text string) {
	// Increase dimensions if necessary.
	d.increaseSize(start.x+len(text), start.y)
	log.Debug("Drawing '", text, "' from ", start, " to ", drawingCoord{x: start.x + len(text), y: start.y})
	for x := 0; x < len(text); x++ {
		(*d)[x+start.x][start.y] = string(text[x])
	}
}

// lineChars returns the horizontal and vertical line characters for the given edge type.
func lineChars(edgeType EdgeType, useAscii bool) (hChar, vChar, diagFwd, diagBack string) {
	if useAscii {
		switch edgeType {
		case DottedArrow, DottedLine:
			return ".", ".", "/", "\\"
		case ThickArrow, ThickLine:
			return "=", "|", "/", "\\"
		default:
			return "-", "|", "/", "\\"
		}
	}
	switch edgeType {
	case DottedArrow, DottedLine:
		return "┈", "┊", "╱", "╲"
	case ThickArrow, ThickLine:
		return "═", "║", "╱", "╲"
	default:
		return "─", "│", "╱", "╲"
	}
}

func (g *graph) drawLine(d *drawing, from drawingCoord, to drawingCoord, offsetFrom int, offsetTo int) []drawingCoord {
	return g.drawLineWithType(d, from, to, offsetFrom, offsetTo, SolidArrow)
}

func (g *graph) drawLineWithType(d *drawing, from drawingCoord, to drawingCoord, offsetFrom int, offsetTo int, edgeType EdgeType) []drawingCoord {
	// Offset determines how far from the actual coord the line should start/stop.
	direction := determineDirection(genericCoord(from), genericCoord(to))
	drawnCoords := make([]drawingCoord, 0)
	hChar, vChar, diagFwd, diagBack := lineChars(edgeType, g.useAscii)
	log.Debug("Drawing line from ", from, " to ", to, " direction: ", direction, " offsetFrom: ", offsetFrom, " offsetTo: ", offsetTo)
	switch direction {
	case Up:
		for y := from.y - offsetFrom; y >= to.y-offsetTo; y-- {
			drawnCoords = append(drawnCoords, drawingCoord{from.x, y})
			(*d)[from.x][y] = vChar
		}
	case Down:
		for y := from.y + offsetFrom; y <= to.y+offsetTo; y++ {
			drawnCoords = append(drawnCoords, drawingCoord{from.x, y})
			(*d)[from.x][y] = vChar
		}
	case Left:
		for x := from.x - offsetFrom; x >= to.x-offsetTo; x-- {
			drawnCoords = append(drawnCoords, drawingCoord{x, from.y})
			(*d)[x][from.y] = hChar
		}
	case Right:
		for x := from.x + offsetFrom; x <= to.x+offsetTo; x++ {
			drawnCoords = append(drawnCoords, drawingCoord{x, from.y})
			(*d)[x][from.y] = hChar
		}
	case UpperLeft:
		for x, y := from.x, from.y-offsetFrom; x >= to.x-offsetTo && y >= to.y-offsetTo; x, y = x-1, y-1 {
			drawnCoords = append(drawnCoords, drawingCoord{x, y})
			(*d)[x][y] = diagBack
		}
	case UpperRight:
		for x, y := from.x, from.y-offsetFrom; x <= to.x+offsetTo && y >= to.y-offsetTo; x, y = x+1, y-1 {
			drawnCoords = append(drawnCoords, drawingCoord{x, y})
			(*d)[x][y] = diagFwd
		}
	case LowerLeft:
		for x, y := from.x, from.y+offsetFrom; x >= to.x-offsetTo && y <= to.y+offsetTo; x, y = x-1, y+1 {
			drawnCoords = append(drawnCoords, drawingCoord{x, y})
			(*d)[x][y] = diagFwd
		}
	case LowerRight:
		for x, y := from.x, from.y+offsetFrom; x <= to.x+offsetTo && y <= to.y+offsetTo; x, y = x+1, y+1 {
			drawnCoords = append(drawnCoords, drawingCoord{x, y})
			(*d)[x][y] = diagBack
		}
	}
	return drawnCoords
}

// Render renders a parsed graph Properties into a string using the provided config.
func Render(properties *Properties, config *diagram.Config) (string, error) {
	if properties == nil {
		return "", fmt.Errorf("nil properties")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	styleType := config.StyleType
	if styleType == "" {
		styleType = "cli"
	}
	properties.styleType = styleType
	properties.useAscii = config.UseAscii
	properties.showCoords = config.ShowCoords
	if config.BoxBorderPadding > 0 {
		properties.boxBorderPadding = config.BoxBorderPadding
	}
	if config.PaddingBetweenX > 0 {
		properties.paddingX = config.PaddingBetweenX
	}
	if config.PaddingBetweenY > 0 {
		properties.paddingY = config.PaddingBetweenY
	}
	properties.targetWidth = config.TargetWidth

	return drawMap(properties), nil
}

func drawMap(properties *Properties) string {
	g := mkGraph(properties.data, properties.nodeInfo)
	g.setStyleClasses(properties)
	g.paddingX = properties.paddingX
	g.paddingY = properties.paddingY
	g.useAscii = properties.useAscii
	g.graphDirection = properties.graphDirection
	g.boxBorderPadding = properties.boxBorderPadding
	g.showCoords = properties.showCoords
	g.targetWidth = properties.targetWidth
	g.setSubgraphs(properties.subgraphs)
	g.createMapping()
	d := g.draw()
	if g.showCoords {
		d = d.debugDrawingWrapper()
		d = d.debugCoordWrapper(g)
	}
	s := drawingToString(d)
	return s
}

// getNodeDimensions returns the width and height for a node's drawing area.
func getNodeDimensions(n *node, g graph) (int, int) {
	w := 0
	for i := 0; i < 2; i++ {
		w += g.columnWidth[n.gridCoord.x+i]
	}
	h := 0
	for i := 0; i < 2; i++ {
		h += g.rowHeight[n.gridCoord.y+i]
	}
	return w, h
}

// drawNodeText draws the node's display name centered in the drawing.
// Multi-line labels (from <br/> tags) are rendered with each line centered.
func drawNodeText(d *drawing, n *node, g graph, w, h int) {
	lines := n.nameLines()
	numLines := len(lines)
	// Vertically center the block of lines
	startY := h/2 - (numLines-1)/2
	for i, line := range lines {
		textY := startY + i
		textX := w/2 - CeilDiv(len(line), 2) + 1
		for x := 0; x < len(line); x++ {
			(*d)[textX+x][textY] = wrapTextInColor(string(line[x]), n.styleClass.styles["color"], g.styleType)
		}
	}
}

// drawShape dispatches to the appropriate shape drawing function based on node shape.
func drawShape(n *node, g graph) *drawing {
	switch n.shape {
	case shapeRounded, shapeStadium:
		return drawRoundedBox(n, g)
	case shapeSubroutine:
		return drawSubroutineBox(n, g)
	case shapeCylinder:
		return drawCylinderBox(n, g)
	case shapeCircle:
		return drawCircleBox(n, g)
	case shapeDiamond:
		return drawDiamondBox(n, g)
	case shapeHexagon:
		return drawHexagonBox(n, g)
	case shapeFlag:
		return drawFlagBox(n, g)
	default:
		return drawRectBox(n, g)
	}
}

// drawRectBox draws a standard rectangular box (default shape).
func drawRectBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing rect box from ", from, " to ", to)
	if !g.useAscii {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "─"
			boxDrawing[x][to.y] = "─"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "│"
			boxDrawing[to.x][y] = "│"
		}
		boxDrawing[from.x][from.y] = "┌"
		boxDrawing[to.x][from.y] = "┐"
		boxDrawing[from.x][to.y] = "└"
		boxDrawing[to.x][to.y] = "┘"
	} else {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "-"
			boxDrawing[x][to.y] = "-"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "|"
			boxDrawing[to.x][y] = "|"
		}
		boxDrawing[from.x][from.y] = "+"
		boxDrawing[to.x][from.y] = "+"
		boxDrawing[from.x][to.y] = "+"
		boxDrawing[to.x][to.y] = "+"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

// drawRoundedBox draws a box with rounded corners (for rounded/stadium shapes).
func drawRoundedBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing rounded box from ", from, " to ", to)
	if !g.useAscii {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "─"
			boxDrawing[x][to.y] = "─"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "│"
			boxDrawing[to.x][y] = "│"
		}
		boxDrawing[from.x][from.y] = "╭"
		boxDrawing[to.x][from.y] = "╮"
		boxDrawing[from.x][to.y] = "╰"
		boxDrawing[to.x][to.y] = "╯"
	} else {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "-"
			boxDrawing[x][to.y] = "-"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "|"
			boxDrawing[to.x][y] = "|"
		}
		boxDrawing[from.x][from.y] = "("
		boxDrawing[to.x][from.y] = ")"
		boxDrawing[from.x][to.y] = "("
		boxDrawing[to.x][to.y] = ")"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

// drawSubroutineBox draws a box with double vertical borders.
func drawSubroutineBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing subroutine box from ", from, " to ", to)
	if !g.useAscii {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "─"
			boxDrawing[x][to.y] = "─"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "│"
			boxDrawing[from.x+1][y] = "│"
			boxDrawing[to.x][y] = "│"
			boxDrawing[to.x-1][y] = "│"
		}
		boxDrawing[from.x][from.y] = "┌"
		boxDrawing[to.x][from.y] = "┐"
		boxDrawing[from.x][to.y] = "└"
		boxDrawing[to.x][to.y] = "┘"
	} else {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "-"
			boxDrawing[x][to.y] = "-"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "|"
			boxDrawing[from.x+1][y] = "|"
			boxDrawing[to.x][y] = "|"
			boxDrawing[to.x-1][y] = "|"
		}
		boxDrawing[from.x][from.y] = "+"
		boxDrawing[to.x][from.y] = "+"
		boxDrawing[from.x][to.y] = "+"
		boxDrawing[to.x][to.y] = "+"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

// drawCylinderBox draws a cylinder shape with curved top/bottom.
func drawCylinderBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing cylinder box from ", from, " to ", to)
	if !g.useAscii {
		// Top curved border
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "─"
		}
		// Bottom curved border (double line)
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][to.y] = "─"
			boxDrawing[x][to.y-1] = "─"
		}
		// Left border
		for y := from.y + 1; y < to.y-1; y++ {
			boxDrawing[from.x][y] = "│"
		}
		// Right border
		for y := from.y + 1; y < to.y-1; y++ {
			boxDrawing[to.x][y] = "│"
		}
		// Top corners
		boxDrawing[from.x][from.y] = "┌"
		boxDrawing[to.x][from.y] = "┐"
		// Bottom double line corners
		boxDrawing[from.x][to.y-1] = "└"
		boxDrawing[to.x][to.y-1] = "┘"
		boxDrawing[from.x][to.y] = "╰"
		boxDrawing[to.x][to.y] = "╯"
	} else {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "-"
			boxDrawing[x][to.y] = "-"
			boxDrawing[x][to.y-1] = "-"
		}
		for y := from.y + 1; y < to.y-1; y++ {
			boxDrawing[from.x][y] = "|"
			boxDrawing[to.x][y] = "|"
		}
		boxDrawing[from.x][from.y] = "+"
		boxDrawing[to.x][from.y] = "+"
		boxDrawing[from.x][to.y-1] = "+"
		boxDrawing[to.x][to.y-1] = "+"
		boxDrawing[from.x][to.y] = "("
		boxDrawing[to.x][to.y] = ")"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

// drawCircleBox draws a circle/ellipse shape with rounded borders.
func drawCircleBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing circle box from ", from, " to ", to)
	if !g.useAscii {
		for x := from.x + 2; x <= to.x-2; x++ {
			boxDrawing[x][from.y] = "─"
			boxDrawing[x][to.y] = "─"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "│"
			boxDrawing[to.x][y] = "│"
		}
		// Diagonal corners for circle effect
		boxDrawing[from.x+1][from.y] = "╱"
		boxDrawing[to.x-1][from.y] = "╲"
		boxDrawing[from.x+1][to.y] = "╲"
		boxDrawing[to.x-1][to.y] = "╱"
	} else {
		for x := from.x + 2; x <= to.x-2; x++ {
			boxDrawing[x][from.y] = "-"
			boxDrawing[x][to.y] = "-"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "|"
			boxDrawing[to.x][y] = "|"
		}
		boxDrawing[from.x+1][from.y] = "/"
		boxDrawing[to.x-1][from.y] = "\\"
		boxDrawing[from.x+1][to.y] = "\\"
		boxDrawing[to.x-1][to.y] = "/"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

// drawDiamondBox draws a decision node.
//
// Terminal box-drawing cannot render a clean rhombus for realistic (wide,
// short) labels: a single glyph per row leaves the diagonal edges several
// columns apart, so they scatter instead of connecting. Instead the decision
// shape is drawn as a double-line box — connected at any size, trivial to draw,
// and visually distinct from the single-line rectangles used for plain nodes.
func drawDiamondBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing decision (double-line) box from ", from, " to ", to)
	if !g.useAscii {
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "═"
			boxDrawing[x][to.y] = "═"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "║"
			boxDrawing[to.x][y] = "║"
		}
		boxDrawing[from.x][from.y] = "╔"
		boxDrawing[to.x][from.y] = "╗"
		boxDrawing[from.x][to.y] = "╚"
		boxDrawing[to.x][to.y] = "╝"
	} else {
		// ASCII fallback: '=' borders read as a heavier "decision" box.
		for x := from.x + 1; x < to.x; x++ {
			boxDrawing[x][from.y] = "="
			boxDrawing[x][to.y] = "="
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "|"
			boxDrawing[to.x][y] = "|"
		}
		boxDrawing[from.x][from.y] = "+"
		boxDrawing[to.x][from.y] = "+"
		boxDrawing[from.x][to.y] = "+"
		boxDrawing[to.x][to.y] = "+"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

// divRound divides a by b (b > 0), rounding to the nearest integer with halves
// rounded away from zero.
func divRound(a, b int) int {
	if a >= 0 {
		return (a + b/2) / b
	}
	return -((-a + b/2) / b)
}

// drawHexagonBox draws a hexagon shape with angled left/right edges.
func drawHexagonBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing hexagon box from ", from, " to ", to)

	midY := h / 2

	if !g.useAscii {
		// Top/bottom border (between angled edges)
		for x := from.x + 2; x <= to.x-2; x++ {
			boxDrawing[x][from.y] = "─"
			boxDrawing[x][to.y] = "─"
		}
		// Left angled edges
		for dy := 0; dy <= midY; dy++ {
			boxDrawing[from.x+1-Min(1, dy)][from.y+dy] = "╱"
			boxDrawing[from.x+1-Min(1, dy)][to.y-dy] = "╲"
		}
		// Right angled edges
		for dy := 0; dy <= midY; dy++ {
			boxDrawing[to.x-1+Min(1, dy)][from.y+dy] = "╲"
			boxDrawing[to.x-1+Min(1, dy)][to.y-dy] = "╱"
		}
		// Middle vertical sides
		boxDrawing[from.x][midY] = "│"
		boxDrawing[to.x][midY] = "│"
	} else {
		for x := from.x + 2; x <= to.x-2; x++ {
			boxDrawing[x][from.y] = "-"
			boxDrawing[x][to.y] = "-"
		}
		for dy := 0; dy <= midY; dy++ {
			boxDrawing[from.x+1-Min(1, dy)][from.y+dy] = "/"
			boxDrawing[from.x+1-Min(1, dy)][to.y-dy] = "\\"
		}
		for dy := 0; dy <= midY; dy++ {
			boxDrawing[to.x-1+Min(1, dy)][from.y+dy] = "\\"
			boxDrawing[to.x-1+Min(1, dy)][to.y-dy] = "/"
		}
		boxDrawing[from.x][midY] = "|"
		boxDrawing[to.x][midY] = "|"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

// drawFlagBox draws an asymmetric/flag shape with a pointed right side.
func drawFlagBox(n *node, g graph) *drawing {
	w, h := getNodeDimensions(n, g)
	from := drawingCoord{0, 0}
	to := drawingCoord{w, h}
	boxDrawing := *(mkDrawing(Max(from.x, to.x), Max(from.y, to.y)))
	log.Debug("Drawing flag box from ", from, " to ", to)

	midY := h / 2

	if !g.useAscii {
		// Top border
		for x := from.x + 1; x < to.x-1; x++ {
			boxDrawing[x][from.y] = "─"
		}
		// Bottom border
		for x := from.x + 1; x < to.x-1; x++ {
			boxDrawing[x][to.y] = "─"
		}
		// Left border (normal)
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "│"
		}
		// Left corners
		boxDrawing[from.x][from.y] = "┌"
		boxDrawing[from.x][to.y] = "└"
		// Right pointed side
		for dy := 1; dy <= midY; dy++ {
			boxDrawing[to.x-1+Min(1, dy)][from.y+dy] = "╲"
			boxDrawing[to.x-1+Min(1, dy)][to.y-dy] = "╱"
		}
		boxDrawing[to.x-1][from.y] = "╲"
		boxDrawing[to.x-1][to.y] = "╱"
		boxDrawing[to.x][midY] = ">"
	} else {
		for x := from.x + 1; x < to.x-1; x++ {
			boxDrawing[x][from.y] = "-"
			boxDrawing[x][to.y] = "-"
		}
		for y := from.y + 1; y < to.y; y++ {
			boxDrawing[from.x][y] = "|"
		}
		boxDrawing[from.x][from.y] = "+"
		boxDrawing[from.x][to.y] = "+"
		boxDrawing[to.x-1][from.y] = "\\"
		boxDrawing[to.x-1][to.y] = "/"
		boxDrawing[to.x][midY] = ">"
	}
	drawNodeText(&boxDrawing, n, g, w, h)
	return &boxDrawing
}

func drawSubgraph(sg *subgraph, g graph) *drawing {
	// Calculate dimensions
	width := sg.maxX - sg.minX
	height := sg.maxY - sg.minY

	if width <= 0 || height <= 0 {
		return mkDrawing(0, 0)
	}

	from := drawingCoord{0, 0}
	to := drawingCoord{width, height}
	subgraphDrawing := *(mkDrawing(width, height))

	log.Debugf("Drawing subgraph %s from (%d,%d) to (%d,%d)", sg.name, from.x, from.y, to.x, to.y)

	if !g.useAscii {
		// Draw top border
		for x := from.x + 1; x < to.x; x++ {
			subgraphDrawing[x][from.y] = "─"
		}
		// Draw bottom border
		for x := from.x + 1; x < to.x; x++ {
			subgraphDrawing[x][to.y] = "─"
		}
		// Draw left border
		for y := from.y + 1; y < to.y; y++ {
			subgraphDrawing[from.x][y] = "│"
		}
		// Draw right border
		for y := from.y + 1; y < to.y; y++ {
			subgraphDrawing[to.x][y] = "│"
		}
		// Draw corners
		subgraphDrawing[from.x][from.y] = "┌"
		subgraphDrawing[to.x][from.y] = "┐"
		subgraphDrawing[from.x][to.y] = "└"
		subgraphDrawing[to.x][to.y] = "┘"
	} else {
		// Draw top border
		for x := from.x + 1; x < to.x; x++ {
			subgraphDrawing[x][from.y] = "-"
		}
		// Draw bottom border
		for x := from.x + 1; x < to.x; x++ {
			subgraphDrawing[x][to.y] = "-"
		}
		// Draw left border
		for y := from.y + 1; y < to.y; y++ {
			subgraphDrawing[from.x][y] = "|"
		}
		// Draw right border
		for y := from.y + 1; y < to.y; y++ {
			subgraphDrawing[to.x][y] = "|"
		}
		// Draw corners
		subgraphDrawing[from.x][from.y] = "+"
		subgraphDrawing[to.x][from.y] = "+"
		subgraphDrawing[from.x][to.y] = "+"
		subgraphDrawing[to.x][to.y] = "+"
	}

	// NOTE: Label is now drawn separately in drawSubgraphLabel to prevent arrows from overwriting it

	return &subgraphDrawing
}

func drawSubgraphLabel(sg *subgraph, g graph) (*drawing, drawingCoord) {
	// Calculate dimensions
	width := sg.maxX - sg.minX
	height := sg.maxY - sg.minY

	if width <= 0 || height <= 0 {
		return mkDrawing(0, 0), drawingCoord{0, 0}
	}

	from := drawingCoord{0, 0}
	to := drawingCoord{width, height}
	labelDrawing := *(mkDrawing(width, height))

	// Draw label centered at top
	labelY := from.y + 1
	labelX := from.x + width/2 - len(sg.name)/2
	if labelX < from.x+1 {
		labelX = from.x + 1
	}
	for i, char := range sg.name {
		if labelX+i < to.x {
			labelDrawing[labelX+i][labelY] = string(char)
		}
	}

	// Return label drawing and its offset position
	offset := drawingCoord{sg.minX, sg.minY}
	return &labelDrawing, offset
}

func wrapTextInColor(text, c, styleType string) string {
	if c == "" {
		return text
	}
	if styleType == "html" {
		return fmt.Sprintf("<span style='color: %s'>%s</span>", c, text)
	} else if styleType == "cli" {
		cliColor := color.HEX(c)
		return cliColor.Sprint(text)
	} else {
		log.Warnf("Unknown style type %s", styleType)
		return text
	}
}

func (d *drawing) increaseSize(x int, y int) {
	currSizeX, currSizeY := getDrawingSize(d)
	drawingWithNewSize := mkDrawing(Max(x, currSizeX), Max(y, currSizeY))
	// For increaseSize, we don't need junction merging, so just copy the drawing
	for x := 0; x < len(*drawingWithNewSize); x++ {
		for y := 0; y < len((*drawingWithNewSize)[0]); y++ {
			if x < len(*d) && y < len((*d)[0]) {
				(*drawingWithNewSize)[x][y] = (*d)[x][y]
			}
		}
	}
	*d = *drawingWithNewSize
}

func (g *graph) setDrawingSizeToGridConstraints() {
	// Get largest column and row size
	maxX := 0
	maxY := 0
	for _, w := range g.columnWidth {
		maxX += w
	}
	for _, h := range g.rowHeight {
		maxY += h
	}
	// Increase size of drawing to fit all nodes
	g.drawing.increaseSize(maxX-1, maxY-1)
}

func mergeJunctions(c1, c2 string) string {
	// Define all possible junction combinations
	junctionMap := map[string]map[string]string{
		"─": {"│": "┼", "┌": "┬", "┐": "┬", "└": "┴", "┘": "┴", "├": "┼", "┤": "┼", "┬": "┬", "┴": "┴"},
		"│": {"─": "┼", "┌": "├", "┐": "┤", "└": "├", "┘": "┤", "├": "├", "┤": "┤", "┬": "┼", "┴": "┼"},
		"┌": {"─": "┬", "│": "├", "┐": "┬", "└": "├", "┘": "┼", "├": "├", "┤": "┼", "┬": "┬", "┴": "┼"},
		"┐": {"─": "┬", "│": "┤", "┌": "┬", "└": "┼", "┘": "┤", "├": "┼", "┤": "┤", "┬": "┬", "┴": "┼"},
		"└": {"─": "┴", "│": "├", "┌": "├", "┐": "┼", "┘": "┴", "├": "├", "┤": "┼", "┬": "┼", "┴": "┴"},
		"┘": {"─": "┴", "│": "┤", "┌": "┼", "┐": "┤", "└": "┴", "├": "┼", "┤": "┤", "┬": "┼", "┴": "┴"},
		"├": {"─": "┼", "│": "├", "┌": "├", "┐": "┼", "└": "├", "┘": "┼", "┤": "┼", "┬": "┼", "┴": "┼"},
		"┤": {"─": "┼", "│": "┤", "┌": "┼", "┐": "┤", "└": "┼", "┘": "┤", "├": "┼", "┬": "┼", "┴": "┼"},
		"┬": {"─": "┬", "│": "┼", "┌": "┬", "┐": "┬", "└": "┼", "┘": "┼", "├": "┼", "┤": "┼", "┴": "┼"},
		"┴": {"─": "┴", "│": "┼", "┌": "┼", "┐": "┼", "└": "┴", "┘": "┴", "├": "┼", "┤": "┼", "┬": "┼"},
		// Double-line decision-box borders meeting single-line edges. The edge
		// attaches with a single stub so the box stays visually "double".
		"═": {"│": "╪", "┬": "╤", "┴": "╧", "├": "╪", "┤": "╪", "┼": "╪", "╵": "╧", "╷": "╤"},
		"║": {"─": "╫", "├": "╟", "┤": "╢", "┬": "╫", "┴": "╫", "┼": "╫", "╴": "╢", "╶": "╟"},
	}

	// Check if there's a defined merge for the two characters
	if merged, ok := junctionMap[c1][c2]; ok {
		log.Debugf("Merging %s and %s to %s", c1, c2, merged)
		return merged
	}

	// If no merge is defined, return c1 as a fallback
	return c1
}

func (g *graph) mergeDrawings(baseDrawing *drawing, mergeCoord drawingCoord, drawings ...*drawing) *drawing {
	// Find the maximum dimensions
	maxX, maxY := getDrawingSize(baseDrawing)
	for _, d := range drawings {
		dX, dY := getDrawingSize(d)
		maxX = Max(maxX, dX+mergeCoord.x)
		maxY = Max(maxY, dY+mergeCoord.y)
	}

	// Create a new merged drawing with the maximum dimensions
	mergedDrawing := mkDrawing(maxX, maxY)

	// Copy the base drawing
	for x := 0; x <= maxX; x++ {
		for y := 0; y <= maxY; y++ {
			if x < len(*baseDrawing) && y < len((*baseDrawing)[0]) {
				(*mergedDrawing)[x][y] = (*baseDrawing)[x][y]
			}
		}
	}

	// Merge all other drawings
	for _, d := range drawings {
		for x := 0; x < len(*d); x++ {
			for y := 0; y < len((*d)[0]); y++ {
				c := (*d)[x][y]
				if c != " " {
					currentChar := (*mergedDrawing)[x+mergeCoord.x][y+mergeCoord.y]
					if !g.useAscii && isJunctionChar(c) && isJunctionChar(currentChar) {
						(*mergedDrawing)[x+mergeCoord.x][y+mergeCoord.y] = mergeJunctions(currentChar, c)
					} else {
						(*mergedDrawing)[x+mergeCoord.x][y+mergeCoord.y] = c
					}
				}
			}
		}
	}

	return mergedDrawing
}

func isJunctionChar(c string) bool {
	for _, junctionChar := range junctionChars {
		if c == junctionChar {
			return true
		}
	}
	return false
}

func drawingToString(d *drawing) string {
	maxX, maxY := getDrawingSize(d)
	dBuilder := strings.Builder{}
	for y := 0; y <= maxY; y++ {
		for x := 0; x <= maxX; x++ {
			dBuilder.WriteString((*d)[x][y])
		}
		if y != maxY {
			dBuilder.WriteString("\n")
		}
	}
	return dBuilder.String()
}

func mkDrawing(x int, y int) *drawing {
	d := make(drawing, x+1)
	for i := 0; i <= x; i++ {
		d[i] = make([]string, y+1)
		for j := 0; j <= y; j++ {
			d[i][j] = " "
		}
	}
	return &d
}

func copyCanvas(toBeCopied *drawing) *drawing {
	x, y := getDrawingSize(toBeCopied)
	return mkDrawing(x, y)
}

func getDrawingSize(d *drawing) (int, int) {
	return len(*d) - 1, len((*d)[0]) - 1
}

func determineDirection(from genericCoord, to genericCoord) direction {
	if from.x == to.x {
		if from.y < to.y {
			return Down
		} else {
			return Up
		}
	} else if from.y == to.y {
		if from.x < to.x {
			return Right
		} else {
			return Left
		}
	} else if from.x < to.x {
		if from.y < to.y {
			return LowerRight
		} else {
			return UpperRight
		}
	} else {
		if from.y < to.y {
			return LowerLeft
		} else {
			return UpperLeft
		}
	}
}

func (d drawing) debugDrawingWrapper() *drawing {
	maxX, maxY := getDrawingSize(&d)
	debugDrawing := *mkDrawing(maxX+2, maxY+1)
	for x := 0; x <= maxX; x++ {
		debugDrawing[x+2][0] = fmt.Sprintf("%d", x%10)
	}
	for y := 0; y <= maxY; y++ {
		debugDrawing[0][y+1] = fmt.Sprintf("%2d", y)
	}

	// For debug wrapper, we don't need junction merging, so just copy
	for x := 0; x < len(debugDrawing); x++ {
		for y := 0; y < len(debugDrawing[0]); y++ {
			if x >= 2 && y >= 1 && x-2 < len(d) && y-1 < len(d[0]) {
				debugDrawing[x][y] = d[x-2][y-1]
			}
		}
	}
	return &debugDrawing
}

func (d drawing) debugCoordWrapper(g graph) *drawing {
	maxX, maxY := getDrawingSize(&d)
	debugDrawing := *mkDrawing(maxX+2, maxY+1)
	currX := 3
	currY := 2
	for x := 0; currX <= maxX+g.columnWidth[x]; x++ {
		w := g.columnWidth[x]
		// debugPos := currX + w/2
		debugPos := currX
		// log.Debugf("Grid coord %d has width %d: %d", x, w, currX)
		debugDrawing[debugPos][0] = fmt.Sprintf("%d", x%10)
		currX += w
	}
	for y := 0; currY <= maxY+g.rowHeight[y]; y++ {
		h := g.rowHeight[y]
		debugPos := currY + h/2
		debugDrawing[0][debugPos] = fmt.Sprintf("%d", y%10)
		currY += h
	}

	return g.mergeDrawings(&debugDrawing, drawingCoord{1, 1}, &d)
}
