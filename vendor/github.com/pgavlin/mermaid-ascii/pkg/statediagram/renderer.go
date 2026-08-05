package statediagram

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/canvas"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

type stateChars struct {
	canvas.BoxChars
	arrowDown string
	arrowBody string
}

var unicodeChars = stateChars{
	BoxChars:  canvas.UnicodeBox,
	arrowDown: "▼",
	arrowBody: "│",
}

var asciiChars = stateChars{
	BoxChars:  canvas.ASCIIBox,
	arrowDown: "v",
	arrowBody: "|",
}

const (
	minStateBoxWidth = 12
	stateBoxPadding  = 4 // 2 padding + 2 border
	borderWidth      = 2
)

// Render renders a StateDiagram to a string.
func Render(sd *StateDiagram, config *diagram.Config) (string, error) {
	if sd == nil || len(sd.States) == 0 {
		return "", fmt.Errorf("no states to render")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	chars := unicodeChars
	if config.UseAscii {
		chars = asciiChars
	}

	var lines []string

	// Build an ordered list of states from transitions for layout
	// Use transition order to determine vertical layout
	ordered := orderStates(sd)

	// Track note annotations per state
	noteMap := make(map[string]*Note)
	for _, n := range sd.Notes {
		noteMap[n.State.ID] = n
	}

	// Calculate the max width needed for state boxes
	maxWidth := 0
	for _, s := range ordered {
		label := stateLabel(s)
		w := len(label) + stateBoxPadding // 2 padding + 2 border
		if w > maxWidth {
			maxWidth = w
		}
	}
	if maxWidth < minStateBoxWidth {
		maxWidth = minStateBoxWidth
	}
	innerWidth := maxWidth - borderWidth // without border chars

	// Find transitions by "from" for labeling
	transFromMap := make(map[string][]*Transition)
	for _, tr := range sd.Transitions {
		transFromMap[tr.From.ID] = append(transFromMap[tr.From.ID], tr)
	}

	for i, s := range ordered {
		label := stateLabel(s)

		if s.ID == "[*]" && s.IsStart {
			// Render start/end pseudo-state as (*)
			padLeft := (innerWidth - 3) / 2
			lines = append(lines, strings.Repeat(" ", padLeft+1)+"(*)")
		} else {
			// Render state box
			box := renderStateBox(label, innerWidth, chars)
			// If there's a note, append it to the right
			if note, ok := noteMap[s.ID]; ok {
				noteText := fmt.Sprintf("  [%s]", note.Text)
				for j := range box {
					box[j] = box[j] + noteText
					noteText = "" // only on first line
				}
			}
			lines = append(lines, box...)
		}

		// Draw transition arrow to next state if exists
		if i < len(ordered)-1 {
			// Find the trigger label for this transition
			trigger := ""
			nextState := ordered[i+1]
			for _, tr := range transFromMap[s.ID] {
				if tr.To.ID == nextState.ID {
					trigger = tr.Trigger
					break
				}
			}

			center := (maxWidth) / 2
			arrowLine := strings.Repeat(" ", center) + chars.arrowBody
			if trigger != "" {
				arrowLine += " " + trigger
			}
			lines = append(lines, arrowLine)
			lines = append(lines, strings.Repeat(" ", center)+chars.arrowDown)
		}
	}

	return strings.Join(lines, "\n") + "\n", nil
}

func stateLabel(s *State) string {
	if s.ID == "[*]" {
		return "(*)"
	}
	if s.Label != "" && s.Label != s.ID {
		return s.Label
	}
	return s.ID
}

func renderStateBox(label string, innerWidth int, chars stateChars) []string {
	if len(label) > innerWidth {
		innerWidth = len(label) + 2
	}

	return []string{
		chars.TopBorder(innerWidth),
		chars.CenterText(label, innerWidth),
		chars.BottomBorder(innerWidth),
	}
}

// orderStates returns states in a reasonable vertical order based on transitions.
func orderStates(sd *StateDiagram) []*State {
	if len(sd.Transitions) == 0 {
		return sd.States
	}

	// Use a simple approach: follow the transition chain starting from [*] or first state
	visited := make(map[string]bool)
	var ordered []*State

	// Build adjacency
	adj := make(map[string][]string)
	for _, tr := range sd.Transitions {
		adj[tr.From.ID] = append(adj[tr.From.ID], tr.To.ID)
	}

	// Start with [*] if present, otherwise first transition source
	startID := ""
	for _, s := range sd.States {
		if s.ID == "[*]" {
			startID = "[*]"
			break
		}
	}
	if startID == "" && len(sd.Transitions) > 0 {
		startID = sd.Transitions[0].From.ID
	}
	if startID == "" && len(sd.States) > 0 {
		startID = sd.States[0].ID
	}

	// BFS-like traversal
	queue := []string{startID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		if s, ok := sd.stateMap[id]; ok {
			ordered = append(ordered, s)
		}
		for _, next := range adj[id] {
			if !visited[next] {
				queue = append(queue, next)
			}
		}
	}

	// Add any remaining states not reachable from start
	for _, s := range sd.States {
		if !visited[s.ID] {
			ordered = append(ordered, s)
		}
	}

	return ordered
}
