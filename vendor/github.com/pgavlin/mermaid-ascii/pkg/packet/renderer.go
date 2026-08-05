package packet

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const bitsPerRow = 32

// Render renders a packet diagram as ASCII/Unicode text.
func Render(pd *PacketDiagram, config *diagram.Config) (string, error) {
	if pd == nil || len(pd.Fields) == 0 {
		return "", fmt.Errorf("no fields to render")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	hChar := "─"
	vChar := "│"
	cornerTL := "┌"
	cornerTR := "┐"
	cornerBL := "└"
	cornerBR := "┘"
	teeDown := "┬"
	teeUp := "┴"
	teeRight := "├"
	teeLeft := "┤"
	cross := "┼"
	if useAscii {
		hChar = "-"
		vChar = "|"
		cornerTL = "+"
		cornerTR = "+"
		cornerBL = "+"
		cornerBR = "+"
		teeDown = "+"
		teeUp = "+"
		teeRight = "+"
		teeLeft = "+"
		cross = "+"
	}

	// Calculate cell width (each bit gets this many chars)
	cellWidth := 3

	// Group fields into rows of bitsPerRow
	maxBit := 0
	for _, f := range pd.Fields {
		if f.EndBit > maxBit {
			maxBit = f.EndBit
		}
	}

	numRows := (maxBit / bitsPerRow) + 1
	var lines []string

	// Bit number header
	headerLine := " "
	for bit := 0; bit < bitsPerRow && bit <= maxBit; bit++ {
		label := fmt.Sprintf("%d", bit)
		pad := cellWidth - len(label)
		headerLine += strings.Repeat(" ", pad/2) + label + strings.Repeat(" ", pad-pad/2)
	}
	lines = append(lines, strings.TrimRight(headerLine, " "))

	for row := 0; row < numRows; row++ {
		rowStart := row * bitsPerRow
		rowEnd := rowStart + bitsPerRow - 1
		if rowEnd > maxBit {
			rowEnd = maxBit
		}

		// Find fields in this row
		rowFields := []*Field{}
		for _, f := range pd.Fields {
			if f.StartBit <= rowEnd && f.EndBit >= rowStart {
				rowFields = append(rowFields, f)
			}
		}

		if len(rowFields) == 0 {
			continue
		}

		// Top border
		topLine := ""
		if row == 0 {
			topLine += cornerTL
		} else {
			topLine += teeRight
		}
		for bit := rowStart; bit <= rowEnd; bit++ {
			topLine += strings.Repeat(hChar, cellWidth)
			isFieldBoundary := false
			for _, f := range rowFields {
				if f.StartBit == bit+1 && bit+1 <= rowEnd {
					isFieldBoundary = true
					break
				}
			}
			if bit == rowEnd {
				if row == 0 {
					topLine += cornerTR
				} else {
					topLine += teeLeft
				}
			} else if isFieldBoundary {
				if row == 0 {
					topLine += teeDown
				} else {
					topLine += cross
				}
			} else {
				topLine += hChar
			}
		}
		lines = append(lines, topLine)

		// Content line
		contentLine := vChar
		for _, f := range rowFields {
			fStart := f.StartBit
			if fStart < rowStart {
				fStart = rowStart
			}
			fEnd := f.EndBit
			if fEnd > rowEnd {
				fEnd = rowEnd
			}
			width := (fEnd - fStart + 1) * (cellWidth + 1) - 1
			label := f.Label
			if len(label) > width {
				label = label[:width]
			}
			pad := width - len(label)
			leftPad := pad / 2
			rightPad := pad - leftPad
			contentLine += strings.Repeat(" ", leftPad) + label + strings.Repeat(" ", rightPad) + vChar
		}
		lines = append(lines, contentLine)

		// Bottom border (only for last row)
		if row == numRows-1 {
			bottomLine := cornerBL
			for bit := rowStart; bit <= rowEnd; bit++ {
				bottomLine += strings.Repeat(hChar, cellWidth)
				isFieldBoundary := false
				for _, f := range rowFields {
					if f.StartBit == bit+1 && bit+1 <= rowEnd {
						isFieldBoundary = true
						break
					}
				}
				if bit == rowEnd {
					bottomLine += cornerBR
				} else if isFieldBoundary {
					bottomLine += teeUp
				} else {
					bottomLine += hChar
				}
			}
			lines = append(lines, bottomLine)
		}
	}

	return strings.Join(lines, "\n") + "\n", nil
}
