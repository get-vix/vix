package pdf

import (
	"math"
	"sort"
	"strings"
)

// detectTable attempts to read a table starting at lines[start]. A table is a
// run of >=2 consecutive multi-segment lines whose segments align into >=2
// columns. It returns the index past the table, the table block, and whether a
// table was found.
func detectTable(lines []line, start int, body float64) (int, block, bool) {
	end := start
	for end < len(lines) {
		l := lines[end]
		if isHeading(l, body) || len(l.segs) < 2 {
			break
		}
		// Stop if there is a large vertical gap (table ended, new region).
		if end > start && lines[end-1].y-l.y > 2.5*body {
			break
		}
		end++
	}
	if end-start < 2 {
		return start, block{}, false
	}

	group := lines[start:end]
	cols := clusterColumns(group, body)
	if len(cols) < 2 {
		return start, block{}, false
	}

	rows := make([][]string, 0, len(group))
	for _, l := range group {
		row := make([]string, len(cols))
		for _, s := range l.segs {
			ci := nearestColumn(cols, s.x)
			cell := strings.TrimSpace(s.text)
			if row[ci] == "" {
				row[ci] = cell
			} else {
				row[ci] += " " + cell
			}
		}
		rows = append(rows, row)
	}
	return end, block{kind: blockTable, rows: rows}, true
}

// clusterColumns groups segment start-x positions across lines into column
// anchors.
func clusterColumns(lines []line, body float64) []float64 {
	var xs []float64
	for _, l := range lines {
		for _, s := range l.segs {
			xs = append(xs, s.x)
		}
	}
	sort.Float64s(xs)
	tol := math.Max(body*1.5, 6)
	var cols []float64
	var cluster []float64
	flush := func() {
		if len(cluster) == 0 {
			return
		}
		sum := 0.0
		for _, v := range cluster {
			sum += v
		}
		cols = append(cols, sum/float64(len(cluster)))
		cluster = nil
	}
	for _, x := range xs {
		if len(cluster) > 0 && x-cluster[len(cluster)-1] > tol {
			flush()
		}
		cluster = append(cluster, x)
	}
	flush()
	return cols
}

func nearestColumn(cols []float64, x float64) int {
	best, bestD := 0, math.Inf(1)
	for i, c := range cols {
		if d := math.Abs(c - x); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}
