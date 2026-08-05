package pdf

import (
	"strings"
)

// Result is the outcome of converting a PDF to Markdown.
type Result struct {
	Markdown string // rendered Markdown ("" when Scanned)
	Pages    int    // number of pages walked
	Scanned  bool   // true when no extractable text layer was found
}

// ToMarkdown parses a PDF and renders its text content as Markdown. It returns
// ErrEncrypted for encrypted documents. For image-only/scanned PDFs it returns
// a Result with Scanned=true and empty Markdown (OCR is not attempted).
func ToMarkdown(data []byte) (Result, error) {
	doc, err := Parse(data)
	if err != nil {
		return Result{}, err
	}
	pages := doc.pages()
	res := Result{Pages: len(pages)}

	var out []string
	for _, pg := range pages {
		blocks := reconstruct(pg)
		if md := renderBlocks(blocks); strings.TrimSpace(md) != "" {
			out = append(out, md)
		}
	}
	md := strings.Join(out, "\n\n")
	if strings.TrimSpace(md) == "" {
		res.Scanned = true
		return res, nil
	}
	res.Markdown = md
	return res, nil
}

func renderBlocks(blocks []block) string {
	var parts []string
	for _, b := range blocks {
		switch b.kind {
		case blockHeading:
			lvl := b.level
			if lvl < 1 {
				lvl = 1
			}
			if lvl > 6 {
				lvl = 6
			}
			parts = append(parts, strings.Repeat("#", lvl)+" "+b.text)
		case blockPara:
			parts = append(parts, b.text)
		case blockTable:
			parts = append(parts, renderTable(b.rows))
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	ncol := 0
	for _, r := range rows {
		if len(r) > ncol {
			ncol = len(r)
		}
	}
	if ncol == 0 {
		return ""
	}
	cell := func(r []string, i int) string {
		if i < len(r) {
			return escapeCell(r[i])
		}
		return ""
	}
	var sb strings.Builder
	writeRow := func(r []string) {
		sb.WriteByte('|')
		for i := 0; i < ncol; i++ {
			sb.WriteByte(' ')
			sb.WriteString(cell(r, i))
			sb.WriteString(" |")
		}
		sb.WriteByte('\n')
	}
	writeRow(rows[0])
	sb.WriteByte('|')
	for i := 0; i < ncol; i++ {
		sb.WriteString(" --- |")
	}
	sb.WriteByte('\n')
	for _, r := range rows[1:] {
		writeRow(r)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}
