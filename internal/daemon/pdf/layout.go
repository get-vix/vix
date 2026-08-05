package pdf

import (
	"math"
	"sort"
	"strings"
)

// blockKind classifies a reconstructed content block.
type blockKind int

const (
	blockPara blockKind = iota
	blockHeading
	blockTable
)

// block is a reconstructed unit of page content.
type block struct {
	kind  blockKind
	level int        // heading level (1..6) for blockHeading
	text  string     // text for blockPara/blockHeading
	rows  [][]string // cells for blockTable
}

// lineSeg is a horizontal segment (potential table cell) within a line.
type lineSeg struct {
	x    float64
	endX float64
	text string
}

type line struct {
	y    float64
	size float64
	segs []lineSeg
}

// reconstruct turns a page's positioned runs into ordered content blocks.
func reconstruct(pg page) []block {
	lines := buildLines(pg.runs)
	if len(lines) == 0 {
		return nil
	}
	body := bodySize(lines)

	var blocks []block
	i := 0
	for i < len(lines) {
		ln := lines[i]
		if isHeading(ln, body) {
			blocks = append(blocks, block{kind: blockHeading, level: headingLevel(ln.size, body), text: lineText(ln)})
			i++
			continue
		}
		// Attempt to detect a table region starting at i.
		if end, tbl, ok := detectTable(lines, i, body); ok {
			blocks = append(blocks, tbl)
			i = end
			continue
		}
		// Otherwise accumulate a paragraph of body lines.
		var parts []string
		prevY := ln.y
		j := i
		for j < len(lines) {
			cur := lines[j]
			if isHeading(cur, body) || len(cur.segs) >= 2 {
				break
			}
			gap := prevY - cur.y
			if j > i && gap > 1.9*body {
				break // vertical gap => paragraph boundary
			}
			parts = append(parts, lineText(cur))
			prevY = cur.y
			j++
		}
		text := joinWrapped(parts)
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, block{kind: blockPara, text: text})
		}
		if j == i {
			j++ // ensure progress
		}
		i = j
	}
	return blocks
}

// buildLines groups runs into lines (by baseline y) and segments (by x-gaps).
func buildLines(runs []textRun) []line {
	if len(runs) == 0 {
		return nil
	}
	// Sort top-to-bottom (PDF y increases upward), then left-to-right.
	sort.SliceStable(runs, func(a, b int) bool {
		if math.Abs(runs[a].y-runs[b].y) > 0.5*maxf(runs[a].size, runs[b].size) {
			return runs[a].y > runs[b].y
		}
		return runs[a].x < runs[b].x
	})

	var lines []line
	var cur []textRun
	curY := runs[0].y
	curSize := runs[0].size
	flush := func() {
		if len(cur) == 0 {
			return
		}
		lines = append(lines, makeLine(cur))
		cur = nil
	}
	for _, r := range runs {
		tol := 0.5 * maxf(curSize, r.size)
		if len(cur) > 0 && math.Abs(r.y-curY) > tol {
			flush()
		}
		if len(cur) == 0 {
			curY = r.y
			curSize = r.size
		}
		cur = append(cur, r)
	}
	flush()
	return lines
}

// makeLine builds a line from same-baseline runs, merging into segments and
// inserting spaces at word gaps and column breaks.
func makeLine(runs []textRun) line {
	sort.SliceStable(runs, func(a, b int) bool { return runs[a].x < runs[b].x })
	size := runs[0].size
	for _, r := range runs {
		if r.size > size {
			size = r.size
		}
	}
	var segs []lineSeg
	var sb strings.Builder
	var segX, endX float64
	started := false
	for _, r := range runs {
		w := runWidth(r)
		if !started {
			segX, endX = r.x, r.x+w
			sb.WriteString(r.text)
			started = true
			continue
		}
		gap := r.x - endX
		switch {
		case gap > 2.2*size: // column break => new segment
			segs = append(segs, lineSeg{x: segX, endX: endX, text: sb.String()})
			sb.Reset()
			sb.WriteString(r.text)
			segX, endX = r.x, r.x+w
		case gap > 0.18*size: // word gap
			if !strings.HasSuffix(sb.String(), " ") && !strings.HasPrefix(r.text, " ") {
				sb.WriteByte(' ')
			}
			sb.WriteString(r.text)
			endX = r.x + w
		default:
			sb.WriteString(r.text)
			endX = r.x + w
		}
	}
	if sb.Len() > 0 {
		segs = append(segs, lineSeg{x: segX, endX: endX, text: sb.String()})
	}
	return line{y: runs[0].y, size: size, segs: segs}
}

func runWidth(r textRun) float64 {
	return float64(len([]rune(r.text))) * 0.5 * r.size
}

func lineText(l line) string {
	parts := make([]string, 0, len(l.segs))
	for _, s := range l.segs {
		parts = append(parts, strings.TrimSpace(s.text))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// joinWrapped joins soft-wrapped lines into a paragraph, de-hyphenating line
// breaks where a word was split.
func joinWrapped(parts []string) string {
	var sb strings.Builder
	for i, p := range parts {
		p = strings.TrimRight(p, " ")
		if i > 0 {
			prev := sb.String()
			if strings.HasSuffix(prev, "-") {
				// Join hyphenated word split across lines.
				s := sb.String()
				sb.Reset()
				sb.WriteString(strings.TrimSuffix(s, "-"))
			} else {
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(p)
	}
	return sb.String()
}

// bodySize returns the most common line size, treated as the body text size.
func bodySize(lines []line) float64 {
	counts := map[int]int{}
	for _, l := range lines {
		counts[int(math.Round(l.size))]++
	}
	best, bestN := 0, -1
	for sz, n := range counts {
		if n > bestN || (n == bestN && sz < best) {
			best, bestN = sz, n
		}
	}
	if best == 0 {
		return 12
	}
	return float64(best)
}

func isHeading(l line, body float64) bool {
	if len(l.segs) != 1 {
		return false
	}
	if l.size < body*1.2 {
		return false
	}
	// Headings are typically short.
	return len([]rune(lineText(l))) <= 120
}

func headingLevel(size, body float64) int {
	ratio := size / body
	switch {
	case ratio >= 2.0:
		return 1
	case ratio >= 1.6:
		return 2
	case ratio >= 1.3:
		return 3
	default:
		return 4
	}
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
