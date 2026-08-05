package sequence

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
	"github.com/mattn/go-runewidth"
)

const (
	defaultSelfMessageWidth   = 4
	defaultMessageSpacing     = 1
	defaultParticipantSpacing = 5
	boxPaddingLeftRight       = 2
	minBoxWidth               = 3
	boxBorderWidth            = 2
	labelLeftMargin           = 2
	labelBufferSpace          = 10
	activationBoxWidth        = 2 // width of activation box (left bar + space + right bar = 3 chars total, but we use 2 offset)
	noteBoxPadding            = 1
)

type diagramLayout struct {
	participantWidths  []int
	participantCenters []int
	totalWidth         int
	messageSpacing     int
	selfMessageWidth   int
}

// activationState tracks active activations per participant
type activationState struct {
	// stacks[participantIndex] = count of active activations
	stacks []int
}

func newActivationState(numParticipants int) *activationState {
	return &activationState{
		stacks: make([]int, numParticipants),
	}
}

func (a *activationState) activate(pIdx int) {
	a.stacks[pIdx]++
}

func (a *activationState) deactivate(pIdx int) {
	if a.stacks[pIdx] > 0 {
		a.stacks[pIdx]--
	}
}

func (a *activationState) isActive(pIdx int) bool {
	return a.stacks[pIdx] > 0
}

func (a *activationState) depth(pIdx int) int {
	return a.stacks[pIdx]
}

func calculateLayout(sd *SequenceDiagram, config *diagram.Config) *diagramLayout {
	participantSpacing := config.SequenceParticipantSpacing
	if participantSpacing <= 0 {
		participantSpacing = defaultParticipantSpacing
	}

	widths := make([]int, len(sd.Participants))
	for i, p := range sd.Participants {
		w := runewidth.StringWidth(p.Label) + boxPaddingLeftRight
		if w < minBoxWidth {
			w = minBoxWidth
		}
		// For actors, ensure minimum width for stick figure
		if p.Type == ParticipantActor {
			// Stick figure is 3 chars wide (/|\), label may be wider
			labelW := runewidth.StringWidth(p.Label)
			if labelW < 3 {
				labelW = 3
			}
			if w < labelW+boxPaddingLeftRight {
				w = labelW + boxPaddingLeftRight
			}
		}
		widths[i] = w
	}

	centers := make([]int, len(sd.Participants))
	currentX := 0
	for i := range sd.Participants {
		boxWidth := widths[i] + boxBorderWidth
		if i == 0 {
			centers[i] = boxWidth / 2
			currentX = boxWidth
		} else {
			currentX += participantSpacing
			centers[i] = currentX + boxWidth/2
			currentX += boxWidth
		}
	}

	last := len(sd.Participants) - 1
	totalWidth := centers[last] + (widths[last]+boxBorderWidth)/2

	msgSpacing := config.SequenceMessageSpacing
	if msgSpacing <= 0 {
		msgSpacing = defaultMessageSpacing
	}
	selfWidth := config.SequenceSelfMessageWidth
	if selfWidth <= 0 {
		selfWidth = defaultSelfMessageWidth
	}

	return &diagramLayout{
		participantWidths:  widths,
		participantCenters: centers,
		totalWidth:         totalWidth,
		messageSpacing:     msgSpacing,
		selfMessageWidth:   selfWidth,
	}
}

// lifelineState tracks which participants have been created/destroyed for lifeline rendering.
type lifelineState struct {
	created   []bool // participant has been created (or was declared normally)
	destroyed []bool // participant has been destroyed
}

func newLifelineState(sd *SequenceDiagram) *lifelineState {
	n := len(sd.Participants)
	ls := &lifelineState{
		created:   make([]bool, n),
		destroyed: make([]bool, n),
	}
	// Check which participants are created via CreateEvent
	createdViaEvent := make(map[int]bool)
	for _, elem := range sd.Elements {
		if ce, ok := elem.(*CreateEvent); ok {
			createdViaEvent[ce.Participant.Index] = true
		}
	}
	// Mark normally-declared participants as created from the start
	for i := range sd.Participants {
		if !createdViaEvent[i] {
			ls.created[i] = true
		}
	}
	return ls
}

func (ls *lifelineState) isActive(pIdx int) bool {
	return ls.created[pIdx] && !ls.destroyed[pIdx]
}

// Render renders a parsed SequenceDiagram as ASCII or Unicode text.
func Render(sd *SequenceDiagram, config *diagram.Config) (string, error) {
	if sd == nil || len(sd.Participants) == 0 {
		return "", fmt.Errorf("no participants")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	chars := Unicode
	if config.UseAscii {
		chars = ASCII
	}

	layout := calculateLayout(sd, config)
	actState := newActivationState(len(sd.Participants))
	llState := newLifelineState(sd)
	var lines []string

	// Draw group headers if any
	if len(sd.Groups) > 0 {
		lines = append(lines, renderGroupHeaders(sd, layout, chars)...)
	}

	// Determine which participants are created mid-diagram
	createdMidDiagram := make(map[int]bool)
	for _, elem := range sd.Elements {
		if ce, ok := elem.(*CreateEvent); ok {
			createdMidDiagram[ce.Participant.Index] = true
		}
	}

	// Draw participant headers (only for non-create participants)
	lines = append(lines, renderParticipantHeadersFiltered(sd, layout, chars, createdMidDiagram)...)

	// If we have elements, render them; otherwise fall back to Messages
	if len(sd.Elements) > 0 {
		lines = append(lines, renderElementsWithLifeline(sd.Elements, layout, chars, actState, llState)...)
	} else {
		for _, msg := range sd.Messages {
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivationsAndState(layout, chars, actState, llState))
			}

			if msg.From == msg.To {
				lines = append(lines, renderSelfMessage(msg, layout, chars, actState)...)
			} else {
				lines = append(lines, renderMessage(msg, layout, chars, actState)...)
			}
		}
	}

	lines = append(lines, buildLifelineWithActivationsAndState(layout, chars, actState, llState))
	return strings.Join(lines, "\n") + "\n", nil
}

// renderParticipantHeaders draws the participant boxes or actor figures at the top.
func renderParticipantHeaders(sd *SequenceDiagram, layout *diagramLayout, chars BoxChars) []string {
	// Check if any participant is an actor
	hasActors := false
	for _, p := range sd.Participants {
		if p.Type == ParticipantActor {
			hasActors = true
			break
		}
	}

	if !hasActors {
		// All boxes: original 3-line rendering
		var lines []string
		lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
			return string(chars.TopLeft) + strings.Repeat(string(chars.Horizontal), layout.participantWidths[i]) + string(chars.TopRight)
		}))

		lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
			w := layout.participantWidths[i]
			labelLen := runewidth.StringWidth(sd.Participants[i].Label)
			pad := (w - labelLen) / 2
			return string(chars.Vertical) + strings.Repeat(" ", pad) + sd.Participants[i].Label +
				strings.Repeat(" ", w-pad-labelLen) + string(chars.Vertical)
		}))

		lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
			w := layout.participantWidths[i]
			return string(chars.BottomLeft) + strings.Repeat(string(chars.Horizontal), w/2) +
				string(chars.TeeDown) + strings.Repeat(string(chars.Horizontal), w-w/2-1) +
				string(chars.BottomRight)
		}))
		return lines
	}

	// Mixed actors and boxes: need to align them vertically
	// Actor is 3 lines tall (head, body, legs) plus label line = 4 lines
	// Box is 3 lines tall (top border, label, bottom border)
	// We pad box rendering to be 4 lines (empty line at top) when actors are present

	// Line 1: actor heads / empty for boxes
	var lines []string
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			// head: O centered
			center := w / 2
			result := strings.Repeat(" ", center) + "O" + strings.Repeat(" ", w-center-1)
			return result
		}
		// box: top border
		return string(chars.TopLeft) + strings.Repeat(string(chars.Horizontal), layout.participantWidths[i]) + string(chars.TopRight)
	}))

	// Line 2: actor body / box label
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			center := w / 2
			result := strings.Repeat(" ", center-1) + "/|\\" + strings.Repeat(" ", w-center-2)
			return result
		}
		// box label
		bw := layout.participantWidths[i]
		labelLen := runewidth.StringWidth(p.Label)
		pad := (bw - labelLen) / 2
		return string(chars.Vertical) + strings.Repeat(" ", pad) + p.Label +
			strings.Repeat(" ", bw-pad-labelLen) + string(chars.Vertical)
	}))

	// Line 3: actor legs / box bottom
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			center := w / 2
			result := strings.Repeat(" ", center-1) + "/ \\" + strings.Repeat(" ", w-center-2)
			return result
		}
		bw := layout.participantWidths[i]
		return string(chars.BottomLeft) + strings.Repeat(string(chars.Horizontal), bw/2) +
			string(chars.TeeDown) + strings.Repeat(string(chars.Horizontal), bw-bw/2-1) +
			string(chars.BottomRight)
	}))

	// Line 4: actor label / empty for boxes
	// Only if there are actors, they need their label below the figure
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			labelLen := runewidth.StringWidth(p.Label)
			pad := (w - labelLen) / 2
			result := strings.Repeat(" ", pad) + p.Label + strings.Repeat(" ", w-pad-labelLen)
			return result
		}
		// For boxes, just show the lifeline
		return strings.Repeat(" ", w/2) + string(chars.Vertical) + strings.Repeat(" ", w-w/2-1)
	}))

	return lines
}

// renderParticipantHeadersFiltered draws participant headers, skipping those created mid-diagram.
func renderParticipantHeadersFiltered(sd *SequenceDiagram, layout *diagramLayout, chars BoxChars, skip map[int]bool) []string {
	if len(skip) == 0 {
		return renderParticipantHeaders(sd, layout, chars)
	}

	// Check if any non-skipped participant is an actor
	hasActors := false
	for _, p := range sd.Participants {
		if !skip[p.Index] && p.Type == ParticipantActor {
			hasActors = true
			break
		}
	}

	emptyBox := func(i int) string {
		w := layout.participantWidths[i] + boxBorderWidth
		return strings.Repeat(" ", w)
	}

	if !hasActors {
		var lines []string
		lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
			if skip[i] {
				return emptyBox(i)
			}
			return string(chars.TopLeft) + strings.Repeat(string(chars.Horizontal), layout.participantWidths[i]) + string(chars.TopRight)
		}))
		lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
			if skip[i] {
				return emptyBox(i)
			}
			w := layout.participantWidths[i]
			labelLen := runewidth.StringWidth(sd.Participants[i].Label)
			pad := (w - labelLen) / 2
			return string(chars.Vertical) + strings.Repeat(" ", pad) + sd.Participants[i].Label +
				strings.Repeat(" ", w-pad-labelLen) + string(chars.Vertical)
		}))
		lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
			if skip[i] {
				return emptyBox(i)
			}
			w := layout.participantWidths[i]
			return string(chars.BottomLeft) + strings.Repeat(string(chars.Horizontal), w/2) +
				string(chars.TeeDown) + strings.Repeat(string(chars.Horizontal), w-w/2-1) +
				string(chars.BottomRight)
		}))
		return lines
	}

	// Mixed mode with actors - same as renderParticipantHeaders but skipping
	var lines []string
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		if skip[i] {
			return emptyBox(i)
		}
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			center := w / 2
			return strings.Repeat(" ", center) + "O" + strings.Repeat(" ", w-center-1)
		}
		return string(chars.TopLeft) + strings.Repeat(string(chars.Horizontal), layout.participantWidths[i]) + string(chars.TopRight)
	}))
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		if skip[i] {
			return emptyBox(i)
		}
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			center := w / 2
			return strings.Repeat(" ", center-1) + "/|\\" + strings.Repeat(" ", w-center-2)
		}
		bw := layout.participantWidths[i]
		labelLen := runewidth.StringWidth(p.Label)
		pad := (bw - labelLen) / 2
		return string(chars.Vertical) + strings.Repeat(" ", pad) + p.Label +
			strings.Repeat(" ", bw-pad-labelLen) + string(chars.Vertical)
	}))
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		if skip[i] {
			return emptyBox(i)
		}
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			center := w / 2
			return strings.Repeat(" ", center-1) + "/ \\" + strings.Repeat(" ", w-center-2)
		}
		bw := layout.participantWidths[i]
		return string(chars.BottomLeft) + strings.Repeat(string(chars.Horizontal), bw/2) +
			string(chars.TeeDown) + strings.Repeat(string(chars.Horizontal), bw-bw/2-1) +
			string(chars.BottomRight)
	}))
	lines = append(lines, buildLine(sd.Participants, layout, func(i int) string {
		if skip[i] {
			return emptyBox(i)
		}
		p := sd.Participants[i]
		w := layout.participantWidths[i] + boxBorderWidth
		if p.Type == ParticipantActor {
			labelLen := runewidth.StringWidth(p.Label)
			pad := (w - labelLen) / 2
			return strings.Repeat(" ", pad) + p.Label + strings.Repeat(" ", w-pad-labelLen)
		}
		return strings.Repeat(" ", w/2) + string(chars.Vertical) + strings.Repeat(" ", w-w/2-1)
	}))
	return lines
}

// renderGroupHeaders renders group (box) indicators above the participant headers.
func renderGroupHeaders(sd *SequenceDiagram, layout *diagramLayout, chars BoxChars) []string {
	if len(sd.Groups) == 0 {
		return nil
	}
	var lines []string

	// For each group, find the leftmost and rightmost participant positions
	type groupSpan struct {
		leftX  int
		rightX int
		label  string
	}
	var spans []groupSpan
	for _, g := range sd.Groups {
		if len(g.Participants) == 0 {
			continue
		}
		leftIdx := g.Participants[0].Index
		rightIdx := g.Participants[len(g.Participants)-1].Index
		leftCenter := layout.participantCenters[leftIdx]
		rightCenter := layout.participantCenters[rightIdx]
		leftBoxWidth := layout.participantWidths[leftIdx] + boxBorderWidth
		rightBoxWidth := layout.participantWidths[rightIdx] + boxBorderWidth
		leftX := leftCenter - leftBoxWidth/2 - 1
		if leftX < 0 {
			leftX = 0
		}
		rightX := rightCenter + rightBoxWidth/2 + 1
		spans = append(spans, groupSpan{leftX: leftX, rightX: rightX, label: g.Label})
	}

	// Render label line
	totalWidth := layout.totalWidth + labelBufferSpace
	labelLine := makeEmptyLine(totalWidth)
	for _, s := range spans {
		if s.label != "" {
			// Center the label above the group span
			labelWidth := runewidth.StringWidth(s.label)
			spanWidth := s.rightX - s.leftX + 1
			pad := (spanWidth - labelWidth) / 2
			col := s.leftX + pad
			if col < 0 {
				col = 0
			}
			for _, r := range s.label {
				if col < len(labelLine) {
					labelLine[col] = r
					col++
				}
			}
		}
	}
	lines = append(lines, strings.TrimRight(string(labelLine), " "))

	// Render top border line with [ and ]
	borderLine := makeEmptyLine(totalWidth)
	for _, s := range spans {
		if s.leftX < len(borderLine) {
			borderLine[s.leftX] = '['
		}
		for j := s.leftX + 1; j < s.rightX && j < len(borderLine); j++ {
			borderLine[j] = chars.Horizontal
		}
		if s.rightX < len(borderLine) {
			borderLine[s.rightX] = ']'
		}
	}
	lines = append(lines, strings.TrimRight(string(borderLine), " "))

	return lines
}

// buildLifelineWithActivationsAndState builds a lifeline respecting create/destroy state.
func buildLifelineWithActivationsAndState(layout *diagramLayout, chars BoxChars, actState *activationState, llState *lifelineState) string {
	line := make([]rune, layout.totalWidth+1)
	for i := range line {
		line[i] = ' '
	}
	for pIdx, c := range layout.participantCenters {
		if c < len(line) && llState.isActive(pIdx) {
			if actState.isActive(pIdx) {
				if c-1 >= 0 {
					line[c-1] = chars.ActivationLeft
				}
				line[c] = ' '
				if c+1 < len(line) {
					line[c+1] = chars.ActivationRight
				}
			} else {
				line[c] = chars.Vertical
			}
		}
	}
	return strings.TrimRight(string(line), " ")
}

// renderElementsWithLifeline renders elements with create/destroy awareness.
func renderElementsWithLifeline(elements []Element, layout *diagramLayout, chars BoxChars, actState *activationState, llState *lifelineState) []string {
	var lines []string
	for _, elem := range elements {
		switch e := elem.(type) {
		case *Message:
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivationsAndState(layout, chars, actState, llState))
			}
			if e.From == e.To {
				lines = append(lines, renderSelfMessage(e, layout, chars, actState)...)
			} else {
				lines = append(lines, renderMessage(e, layout, chars, actState)...)
			}

		case *Note:
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivationsAndState(layout, chars, actState, llState))
			}
			lines = append(lines, renderNote(e, layout, chars, actState)...)

		case *ActivationEvent:
			if e.Activate {
				actState.activate(e.Participant.Index)
			} else {
				actState.deactivate(e.Participant.Index)
			}

		case *Block:
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivationsAndState(layout, chars, actState, llState))
			}
			lines = append(lines, renderBlock(e, layout, chars, actState)...)

		case *CreateEvent:
			// Render a spacing line, then the participant box inline
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivationsAndState(layout, chars, actState, llState))
			}
			lines = append(lines, renderInlineParticipant(e.Participant, layout, chars, llState)...)
			llState.created[e.Participant.Index] = true

		case *DestroyEvent:
			// Render an X on the participant's lifeline
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivationsAndState(layout, chars, actState, llState))
			}
			lines = append(lines, renderDestroyMarker(e.Participant, layout, chars, actState, llState))
			llState.destroyed[e.Participant.Index] = true
		}
	}
	return lines
}

// renderInlineParticipant renders a participant box at the current vertical position (for create).
func renderInlineParticipant(p *Participant, layout *diagramLayout, chars BoxChars, llState *lifelineState) []string {
	var lines []string
	idx := p.Index
	boxWidth := layout.participantWidths[idx]
	center := layout.participantCenters[idx]
	leftX := center - (boxWidth+boxBorderWidth)/2

	totalWidth := layout.totalWidth + labelBufferSpace

	makeRuneLine := func() []rune {
		r := make([]rune, totalWidth)
		for i := range r {
			r[i] = ' '
		}
		// Draw other active lifelines
		for pIdx, c := range layout.participantCenters {
			if c < len(r) && llState.isActive(pIdx) {
				r[c] = chars.Vertical
			}
		}
		return r
	}

	// Top border
	topLine := makeRuneLine()
	for j := 0; j < boxWidth+boxBorderWidth && leftX+j < len(topLine); j++ {
		if j == 0 {
			topLine[leftX+j] = chars.TopLeft
		} else if j == boxWidth+boxBorderWidth-1 {
			topLine[leftX+j] = chars.TopRight
		} else {
			topLine[leftX+j] = chars.Horizontal
		}
	}
	lines = append(lines, strings.TrimRight(string(topLine), " "))

	// Label line
	labelLine := makeRuneLine()
	labelLine[leftX] = chars.Vertical
	labelLine[leftX+boxWidth+boxBorderWidth-1] = chars.Vertical
	labelLen := runewidth.StringWidth(p.Label)
	pad := (boxWidth - labelLen) / 2
	col := leftX + 1 + pad
	for _, r := range p.Label {
		if col < len(labelLine) {
			labelLine[col] = r
			col++
		}
	}
	lines = append(lines, strings.TrimRight(string(labelLine), " "))

	// Bottom border with lifeline tee
	botLine := makeRuneLine()
	for j := 0; j < boxWidth+boxBorderWidth && leftX+j < len(botLine); j++ {
		if j == 0 {
			botLine[leftX+j] = chars.BottomLeft
		} else if j == boxWidth+boxBorderWidth-1 {
			botLine[leftX+j] = chars.BottomRight
		} else if leftX+j == center {
			botLine[leftX+j] = chars.TeeDown
		} else {
			botLine[leftX+j] = chars.Horizontal
		}
	}
	lines = append(lines, strings.TrimRight(string(botLine), " "))

	return lines
}

// renderDestroyMarker renders an X on the participant's lifeline.
func renderDestroyMarker(p *Participant, layout *diagramLayout, chars BoxChars, actState *activationState, llState *lifelineState) string {
	line := make([]rune, layout.totalWidth+1)
	for i := range line {
		line[i] = ' '
	}
	for pIdx, c := range layout.participantCenters {
		if c < len(line) && llState.isActive(pIdx) {
			if pIdx == p.Index {
				// Render X
				line[c] = 'X'
			} else if actState.isActive(pIdx) {
				if c-1 >= 0 {
					line[c-1] = chars.ActivationLeft
				}
				line[c] = ' '
				if c+1 < len(line) {
					line[c+1] = chars.ActivationRight
				}
			} else {
				line[c] = chars.Vertical
			}
		}
	}
	return strings.TrimRight(string(line), " ")
}

// renderElements renders a list of elements (messages, notes, activations, blocks).
func renderElements(elements []Element, layout *diagramLayout, chars BoxChars, actState *activationState) []string {
	var lines []string
	for _, elem := range elements {
		switch e := elem.(type) {
		case *Message:
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivations(layout, chars, actState))
			}
			if e.From == e.To {
				lines = append(lines, renderSelfMessage(e, layout, chars, actState)...)
			} else {
				lines = append(lines, renderMessage(e, layout, chars, actState)...)
			}

		case *Note:
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivations(layout, chars, actState))
			}
			lines = append(lines, renderNote(e, layout, chars, actState)...)

		case *ActivationEvent:
			if e.Activate {
				actState.activate(e.Participant.Index)
			} else {
				actState.deactivate(e.Participant.Index)
			}

		case *Block:
			for i := 0; i < layout.messageSpacing; i++ {
				lines = append(lines, buildLifelineWithActivations(layout, chars, actState))
			}
			lines = append(lines, renderBlock(e, layout, chars, actState)...)
		}
	}
	return lines
}

func buildLine(participants []*Participant, layout *diagramLayout, draw func(int) string) string {
	var sb strings.Builder
	for i := range participants {
		boxWidth := layout.participantWidths[i] + boxBorderWidth
		left := layout.participantCenters[i] - boxWidth/2

		needed := left - len([]rune(sb.String()))
		if needed > 0 {
			sb.WriteString(strings.Repeat(" ", needed))
		}
		sb.WriteString(draw(i))
	}
	return sb.String()
}

func buildLifeline(layout *diagramLayout, chars BoxChars) string {
	line := make([]rune, layout.totalWidth+1)
	for i := range line {
		line[i] = ' '
	}
	for _, c := range layout.participantCenters {
		if c < len(line) {
			line[c] = chars.Vertical
		}
	}
	return strings.TrimRight(string(line), " ")
}

func buildLifelineWithActivations(layout *diagramLayout, chars BoxChars, actState *activationState) string {
	line := make([]rune, layout.totalWidth+1)
	for i := range line {
		line[i] = ' '
	}
	for pIdx, c := range layout.participantCenters {
		if c < len(line) {
			if actState.isActive(pIdx) {
				// Draw activation box: │ │ around the lifeline
				if c-1 >= 0 {
					line[c-1] = chars.ActivationLeft
				}
				line[c] = ' '
				if c+1 < len(line) {
					line[c+1] = chars.ActivationRight
				}
			} else {
				line[c] = chars.Vertical
			}
		}
	}
	return strings.TrimRight(string(line), " ")
}

// getArrowChars returns the appropriate end characters for a given arrow type.
func getArrowChars(aType ArrowType, chars BoxChars) (rightEnd rune, leftEnd rune) {
	switch aType {
	case SolidArrow, DottedArrow:
		return chars.ArrowRight, chars.ArrowLeft
	case SolidOpen, DottedOpen:
		return chars.OpenArrowRight, chars.OpenArrowLeft
	case SolidCross, DottedCross:
		return chars.CrossEnd, chars.CrossEnd
	case SolidAsync, DottedAsync:
		return ')', '('
	default:
		return chars.ArrowRight, chars.ArrowLeft
	}
}

func renderMessage(msg *Message, layout *diagramLayout, chars BoxChars, actState *activationState) []string {
	var lines []string
	from, to := layout.participantCenters[msg.From.Index], layout.participantCenters[msg.To.Index]

	label := msg.Label
	if msg.Number > 0 {
		label = fmt.Sprintf("%d. %s", msg.Number, msg.Label)
	}

	if label != "" {
		start := min(from, to) + labelLeftMargin
		labelWidth := runewidth.StringWidth(label)
		w := max(layout.totalWidth, start+labelWidth) + labelBufferSpace
		line := []rune(buildLifelineWithActivations(layout, chars, actState))
		if len(line) < w {
			padding := make([]rune, w-len(line))
			for k := range padding {
				padding[k] = ' '
			}
			line = append(line, padding...)
		}

		col := start
		for _, r := range label {
			if col < len(line) {
				line[col] = r
				col++
			}
		}
		lines = append(lines, strings.TrimRight(string(line), " "))
	}

	line := []rune(buildLifelineWithActivations(layout, chars, actState))
	style := chars.SolidLine
	if msg.ArrowType.IsDotted() {
		style = chars.DottedLine
	}

	rightEnd, leftEnd := getArrowChars(msg.ArrowType, chars)

	if from < to {
		line[from] = chars.TeeRight
		for i := from + 1; i < to; i++ {
			line[i] = style
		}
		line[to-1] = rightEnd
		line[to] = chars.Vertical
	} else {
		line[to] = chars.Vertical
		line[to+1] = leftEnd
		for i := to + 2; i < from; i++ {
			line[i] = style
		}
		line[from] = chars.TeeLeft
	}
	lines = append(lines, strings.TrimRight(string(line), " "))
	return lines
}

func renderSelfMessage(msg *Message, layout *diagramLayout, chars BoxChars, actState *activationState) []string {
	var lines []string
	center := layout.participantCenters[msg.From.Index]
	width := layout.selfMessageWidth

	ensureWidth := func(l string) []rune {
		target := layout.totalWidth + width + 1
		r := []rune(l)
		if len(r) < target {
			pad := make([]rune, target-len(r))
			for i := range pad {
				pad[i] = ' '
			}
			r = append(r, pad...)
		}
		return r
	}

	label := msg.Label
	if msg.Number > 0 {
		label = fmt.Sprintf("%d. %s", msg.Number, msg.Label)
	}

	if label != "" {
		line := ensureWidth(buildLifelineWithActivations(layout, chars, actState))
		start := center + labelLeftMargin
		labelWidth := runewidth.StringWidth(label)
		needed := start + labelWidth + labelBufferSpace
		if len(line) < needed {
			pad := make([]rune, needed-len(line))
			for i := range pad {
				pad[i] = ' '
			}
			line = append(line, pad...)
		}
		col := start
		for _, c := range label {
			if col < len(line) {
				line[col] = c
				col++
			}
		}
		lines = append(lines, strings.TrimRight(string(line), " "))
	}

	l1 := ensureWidth(buildLifelineWithActivations(layout, chars, actState))
	l1[center] = chars.TeeRight
	for i := 1; i < width; i++ {
		l1[center+i] = chars.Horizontal
	}
	l1[center+width-1] = chars.SelfTopRight
	lines = append(lines, strings.TrimRight(string(l1), " "))

	l2 := ensureWidth(buildLifelineWithActivations(layout, chars, actState))
	l2[center+width-1] = chars.Vertical
	lines = append(lines, strings.TrimRight(string(l2), " "))

	l3 := ensureWidth(buildLifelineWithActivations(layout, chars, actState))
	l3[center] = chars.Vertical
	l3[center+1] = chars.ArrowLeft
	for i := 2; i < width-1; i++ {
		l3[center+i] = chars.Horizontal
	}
	l3[center+width-1] = chars.SelfBottom
	lines = append(lines, strings.TrimRight(string(l3), " "))

	return lines
}

// renderNote renders a note as a small box.
func renderNote(note *Note, layout *diagramLayout, chars BoxChars, actState *activationState) []string {
	var lines []string
	textWidth := runewidth.StringWidth(note.Text)
	boxWidth := textWidth + 2 // 1 padding on each side

	var startX int
	switch note.Position {
	case NoteRightOf:
		startX = layout.participantCenters[note.Participant.Index] + 2
	case NoteLeftOf:
		startX = layout.participantCenters[note.Participant.Index] - 2 - boxWidth - 2
		if startX < 0 {
			startX = 0
		}
	case NoteOver:
		if note.EndParticipant != nil {
			// Span across participants
			leftCenter := layout.participantCenters[note.Participant.Index]
			rightCenter := layout.participantCenters[note.EndParticipant.Index]
			midpoint := (leftCenter + rightCenter) / 2
			startX = midpoint - boxWidth/2
		} else {
			center := layout.participantCenters[note.Participant.Index]
			startX = center - boxWidth/2
		}
	}

	if startX < 0 {
		startX = 0
	}

	totalWidth := startX + boxWidth + 2 + labelBufferSpace
	if totalWidth < layout.totalWidth+1 {
		totalWidth = layout.totalWidth + 1
	}

	makeRuneLine := func() []rune {
		base := buildLifelineWithActivations(layout, chars, actState)
		r := []rune(base)
		if len(r) < totalWidth {
			pad := make([]rune, totalWidth-len(r))
			for i := range pad {
				pad[i] = ' '
			}
			r = append(r, pad...)
		}
		return r
	}

	// Top border
	topLine := makeRuneLine()
	for j := startX; j < startX+boxWidth+2 && j < len(topLine); j++ {
		if j == startX {
			topLine[j] = chars.TopLeft
		} else if j == startX+boxWidth+1 {
			topLine[j] = chars.TopRight
		} else {
			topLine[j] = chars.Horizontal
		}
	}
	lines = append(lines, strings.TrimRight(string(topLine), " "))

	// Text line
	textLine := makeRuneLine()
	if startX < len(textLine) {
		textLine[startX] = chars.Vertical
	}
	col := startX + 1
	// padding
	if col < len(textLine) {
		textLine[col] = ' '
		col++
	}
	for _, r := range note.Text {
		if col < len(textLine) {
			textLine[col] = r
			col++
		}
	}
	if col < len(textLine) {
		textLine[col] = ' '
		col++
	}
	if startX+boxWidth+1 < len(textLine) {
		textLine[startX+boxWidth+1] = chars.Vertical
	}
	lines = append(lines, strings.TrimRight(string(textLine), " "))

	// Bottom border
	botLine := makeRuneLine()
	for j := startX; j < startX+boxWidth+2 && j < len(botLine); j++ {
		if j == startX {
			botLine[j] = chars.BottomLeft
		} else if j == startX+boxWidth+1 {
			botLine[j] = chars.BottomRight
		} else {
			botLine[j] = chars.Horizontal
		}
	}
	lines = append(lines, strings.TrimRight(string(botLine), " "))

	return lines
}

// renderBlock renders an interaction block (loop, alt, opt, etc.).
func renderBlock(block *Block, layout *diagramLayout, chars BoxChars, actState *activationState) []string {
	var lines []string

	// Determine the span of the block - from first to last participant involved
	leftX := 1 // Start from left edge with small margin
	rightX := layout.totalWidth - 1

	blockLabel := fmt.Sprintf("%s %s", block.Type, block.Label)

	// Top border with label
	topLine := makeEmptyLine(layout.totalWidth + labelBufferSpace)
	setLifelines(topLine, layout, chars, actState)
	for j := leftX; j <= rightX && j < len(topLine); j++ {
		if j == leftX {
			topLine[j] = chars.TopLeft
		} else if j == rightX {
			topLine[j] = chars.TopRight
		} else {
			topLine[j] = chars.Horizontal
		}
	}
	lines = append(lines, strings.TrimRight(string(topLine), " "))

	// Label line
	labelLine := makeEmptyLine(layout.totalWidth + labelBufferSpace)
	setLifelines(labelLine, layout, chars, actState)
	if leftX < len(labelLine) {
		labelLine[leftX] = chars.Vertical
	}
	if rightX < len(labelLine) {
		labelLine[rightX] = chars.Vertical
	}
	col := leftX + 2
	for _, r := range blockLabel {
		if col < len(labelLine) {
			labelLine[col] = r
			col++
		}
	}
	lines = append(lines, strings.TrimRight(string(labelLine), " "))

	// Separator after label
	sepLine := makeEmptyLine(layout.totalWidth + labelBufferSpace)
	setLifelines(sepLine, layout, chars, actState)
	if leftX < len(sepLine) {
		sepLine[leftX] = chars.Vertical
	}
	if rightX < len(sepLine) {
		sepLine[rightX] = chars.Vertical
	}
	for j := leftX + 1; j < rightX && j < len(sepLine); j++ {
		if sepLine[j] == chars.Vertical || sepLine[j] == chars.ActivationLeft || sepLine[j] == chars.ActivationRight {
			continue // don't overwrite lifelines
		}
		sepLine[j] = chars.DottedLine
	}
	lines = append(lines, strings.TrimRight(string(sepLine), " "))

	// Render block contents
	contentLines := renderElements(block.Elements, layout, chars, actState)
	for _, cl := range contentLines {
		// Add block borders to content lines
		r := []rune(cl)
		if len(r) < layout.totalWidth+labelBufferSpace {
			pad := make([]rune, layout.totalWidth+labelBufferSpace-len(r))
			for k := range pad {
				pad[k] = ' '
			}
			r = append(r, pad...)
		}
		if leftX < len(r) {
			r[leftX] = chars.Vertical
		}
		if rightX < len(r) {
			r[rightX] = chars.Vertical
		}
		lines = append(lines, strings.TrimRight(string(r), " "))
	}

	// Render sections (else/and/option)
	for _, section := range block.Sections {
		// Dashed divider
		divLine := makeEmptyLine(layout.totalWidth + labelBufferSpace)
		setLifelines(divLine, layout, chars, actState)
		if leftX < len(divLine) {
			divLine[leftX] = chars.Vertical
		}
		if rightX < len(divLine) {
			divLine[rightX] = chars.Vertical
		}
		for j := leftX + 1; j < rightX && j < len(divLine); j++ {
			if divLine[j] == chars.Vertical || divLine[j] == chars.ActivationLeft || divLine[j] == chars.ActivationRight {
				continue
			}
			divLine[j] = chars.DottedLine
		}
		lines = append(lines, strings.TrimRight(string(divLine), " "))

		// Section label (if any)
		if section.Label != "" {
			sLabelLine := makeEmptyLine(layout.totalWidth + labelBufferSpace)
			setLifelines(sLabelLine, layout, chars, actState)
			if leftX < len(sLabelLine) {
				sLabelLine[leftX] = chars.Vertical
			}
			if rightX < len(sLabelLine) {
				sLabelLine[rightX] = chars.Vertical
			}
			col := leftX + 2
			for _, r := range section.Label {
				if col < len(sLabelLine) {
					sLabelLine[col] = r
					col++
				}
			}
			lines = append(lines, strings.TrimRight(string(sLabelLine), " "))
		}

		// Section content
		sectionContent := renderElements(section.Elements, layout, chars, actState)
		for _, cl := range sectionContent {
			r := []rune(cl)
			if len(r) < layout.totalWidth+labelBufferSpace {
				pad := make([]rune, layout.totalWidth+labelBufferSpace-len(r))
				for k := range pad {
					pad[k] = ' '
				}
				r = append(r, pad...)
			}
			if leftX < len(r) {
				r[leftX] = chars.Vertical
			}
			if rightX < len(r) {
				r[rightX] = chars.Vertical
			}
			lines = append(lines, strings.TrimRight(string(r), " "))
		}
	}

	// Bottom border
	botLine := makeEmptyLine(layout.totalWidth + labelBufferSpace)
	setLifelines(botLine, layout, chars, actState)
	for j := leftX; j <= rightX && j < len(botLine); j++ {
		if j == leftX {
			botLine[j] = chars.BottomLeft
		} else if j == rightX {
			botLine[j] = chars.BottomRight
		} else {
			botLine[j] = chars.Horizontal
		}
	}
	lines = append(lines, strings.TrimRight(string(botLine), " "))

	return lines
}

func makeEmptyLine(width int) []rune {
	line := make([]rune, width)
	for i := range line {
		line[i] = ' '
	}
	return line
}

func setLifelines(line []rune, layout *diagramLayout, chars BoxChars, actState *activationState) {
	for pIdx, c := range layout.participantCenters {
		if c < len(line) {
			if actState.isActive(pIdx) {
				if c-1 >= 0 {
					line[c-1] = chars.ActivationLeft
				}
				line[c] = ' '
				if c+1 < len(line) {
					line[c+1] = chars.ActivationRight
				}
			} else {
				line[c] = chars.Vertical
			}
		}
	}
}
