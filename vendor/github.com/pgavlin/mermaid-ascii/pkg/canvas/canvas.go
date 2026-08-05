package canvas

import "strings"

// Canvas is a 2D grid of characters for rendering diagrams.
// Internally it is stored as [x][y] (column-major), matching the
// convention used by the existing drawing type in cmd/draw.go.
type Canvas [][]string

// New creates a new Canvas with the given dimensions, filled with spaces.
// Width and height must be positive; a zero or negative dimension produces
// a nil Canvas.
func New(width, height int) *Canvas {
	if width <= 0 || height <= 0 {
		return nil
	}
	c := make(Canvas, width)
	for x := 0; x < width; x++ {
		c[x] = make([]string, height)
		for y := 0; y < height; y++ {
			c[x][y] = " "
		}
	}
	return &c
}

// Width returns the width (number of columns) of the canvas.
func (c *Canvas) Width() int {
	if c == nil || len(*c) == 0 {
		return 0
	}
	return len(*c)
}

// Height returns the height (number of rows) of the canvas.
func (c *Canvas) Height() int {
	if c == nil || len(*c) == 0 {
		return 0
	}
	return len((*c)[0])
}

// InBounds reports whether (x, y) is inside the canvas.
func (c *Canvas) InBounds(x, y int) bool {
	return x >= 0 && y >= 0 && x < c.Width() && y < c.Height()
}

// Set sets the character at position (x, y).
// Out-of-bounds writes are silently ignored.
func (c *Canvas) Set(x, y int, s string) {
	if !c.InBounds(x, y) {
		return
	}
	(*c)[x][y] = s
}

// Get returns the character at position (x, y).
// Out-of-bounds reads return a space.
func (c *Canvas) Get(x, y int) string {
	if !c.InBounds(x, y) {
		return " "
	}
	return (*c)[x][y]
}

// DrawText writes text horizontally starting at position (x, y).
// Characters that fall outside the canvas bounds are silently skipped.
func (c *Canvas) DrawText(x, y int, text string) {
	for i, ch := range text {
		c.Set(x+i, y, string(ch))
	}
}

// ToString converts the canvas to a string with rows separated by newlines.
// Trailing newline is not included.
func (c *Canvas) ToString() string {
	if c == nil {
		return ""
	}
	w := c.Width()
	h := c.Height()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b.WriteString((*c)[x][y])
		}
		if y < h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Copy creates a deep copy of the canvas.
func (c *Canvas) Copy() *Canvas {
	if c == nil {
		return nil
	}
	w := c.Width()
	h := c.Height()
	cp := New(w, h)
	for x := 0; x < w; x++ {
		copy((*cp)[x], (*c)[x])
	}
	return cp
}

// IncreaseSize expands the canvas to at least the given dimensions.
// Existing content is preserved. If both dimensions are already large
// enough the canvas is unchanged.
func (c *Canvas) IncreaseSize(width, height int) {
	if c == nil {
		return
	}
	curW := c.Width()
	curH := c.Height()
	newW := curW
	if width > curW {
		newW = width
	}
	newH := curH
	if height > curH {
		newH = height
	}
	if newW == curW && newH == curH {
		return
	}
	bigger := New(newW, newH)
	for x := 0; x < curW; x++ {
		for y := 0; y < curH; y++ {
			(*bigger)[x][y] = (*c)[x][y]
		}
	}
	*c = *bigger
}
