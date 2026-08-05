package gitgraph

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	laneWidth        = 4
	commitLineBuffer = 50
)

// Render renders a GitGraph as a formatted string using Unicode or ASCII characters.
func Render(gg *GitGraph, config *diagram.Config) (string, error) {
	if gg == nil || len(gg.Commits) == 0 {
		return "", fmt.Errorf("no commits to render")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	// Characters
	commitChar := "●"
	mergeChar := "●"
	highlightChar := "◆"
	reverseChar := "▲"
	vChar := "│"
	hChar := "─"
	branchRight := "╲"
	branchLeft := "╱"
	if useAscii {
		commitChar = "*"
		mergeChar = "*"
		highlightChar = "@"
		reverseChar = "^"
		vChar = "|"
		hChar = "-"
		branchRight = "\\"
		branchLeft = "/"
	}

	// Determine max lanes
	maxLane := 0
	for _, b := range gg.Branches {
		if b.Lane > maxLane {
			maxLane = b.Lane
		}
	}
	numLanes := maxLane + 1

	// Track which lanes are active at each commit
	activeLanes := make(map[int]bool)
	activeLanes[0] = true // main is always active
	branchStartCommit := make(map[string]int) // branch name -> commit index where it starts

	// Find when branches start
	for _, b := range gg.Branches {
		for i, c := range gg.Commits {
			if c.Branch == b.Name {
				if _, exists := branchStartCommit[b.Name]; !exists {
					branchStartCommit[b.Name] = i
				}
			}
		}
	}

	var lines []string

	// Branch legend at top
	legendLine := ""
	for _, b := range gg.Branches {
		pos := b.Lane * laneWidth
		for len(legendLine) < pos {
			legendLine += " "
		}
		legendLine += b.Name
	}
	lines = append(lines, legendLine)

	// Render each commit
	for i, commit := range gg.Commits {
		// Update active lanes
		activeLanes[commit.Lane] = true

		// Draw connection line from previous commit
		if i > 0 {
			connLine := make([]rune, numLanes*laneWidth)
			for j := range connLine {
				connLine[j] = ' '
			}
			for lane := range activeLanes {
				pos := lane * laneWidth
				if pos < len(connLine) {
					connLine[pos] = []rune(vChar)[0]
				}
			}

			// Draw merge/branch lines if this commit has parents from other branches
			if len(commit.Parents) > 0 {
				for _, parentBranch := range commit.Parents {
					for _, b := range gg.Branches {
						if b.Name == parentBranch {
							fromLane := b.Lane
							toLane := commit.Lane
							if fromLane != toLane {
								// Draw diagonal
								minL := fromLane
								maxL := toLane
								if fromLane > toLane {
									minL = toLane
									maxL = fromLane
								}
								for l := minL + 1; l < maxL; l++ {
									pos := l * laneWidth
									if pos < len(connLine) {
										connLine[pos] = []rune(hChar)[0]
									}
								}
								fromPos := fromLane * laneWidth
								toPos := toLane * laneWidth
								if fromPos < len(connLine) && toPos < len(connLine) {
									if fromLane < toLane {
										connLine[fromPos+1] = []rune(branchRight)[0]
									} else {
										connLine[toPos+1] = []rune(branchLeft)[0]
									}
								}
							}
						}
					}
				}
			}
			lines = append(lines, strings.TrimRight(string(connLine), " "))
		}

		// Draw commit line
		commitLine := make([]rune, numLanes*laneWidth+commitLineBuffer)
		for j := range commitLine {
			commitLine[j] = ' '
		}

		// Draw lane lines
		for lane := range activeLanes {
			pos := lane * laneWidth
			if pos < len(commitLine) {
				commitLine[pos] = []rune(vChar)[0]
			}
		}

		// Draw commit dot
		pos := commit.Lane * laneWidth
		char := commitChar
		switch commit.Type {
		case Highlight:
			char = highlightChar
		case Reverse:
			char = reverseChar
		}
		if len(commit.Parents) > 0 {
			char = mergeChar
		}
		if pos < len(commitLine) {
			commitLine[pos] = []rune(char)[0]
		}

		// Add commit info
		infoPos := numLanes*laneWidth + 2
		info := commit.ID
		if commit.Message != "" {
			info += " " + commit.Message
		}
		if commit.Tag != "" {
			tagStr := " (tag: " + commit.Tag + ")"
			info += tagStr
		}

		for j, r := range info {
			p := infoPos + j
			if p < len(commitLine) {
				commitLine[p] = r
			}
		}

		lines = append(lines, strings.TrimRight(string(commitLine), " "))
	}

	return strings.Join(lines, "\n") + "\n", nil
}
