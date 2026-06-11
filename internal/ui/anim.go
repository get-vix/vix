package ui

import (
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"
)

const (
	// animFPS sets the thinking spinner cadence. Every tick re-runs the full
	// Model.View, so this directly multiplies render cost; 12fps reads as
	// smooth (same rationale as sessionsSpinnerPeriod below) and is 2.5x
	// cheaper than the previous 30fps.
	animFPS      = 12
	animNumChars = 12
)

// animFrames are the braille spinner frames each character cycles through.
var animFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Animation gradient endpoints — derived from brand colors in styles.go.
var (
	animColorA = lipgloss.Color(primaryHex)
	animColorB = lipgloss.Color(secondaryHex)
)

// animStepMsg triggers the next animation frame.
// anim must point to the ThinkingAnim that scheduled the tick, and gen must
// match its current generation. Together they ensure ticks from a different
// session (same gen by coincidence) and stale ticks from a previous Start/Stop
// cycle are both silently dropped.
type animStepMsg struct {
	gen  int
	anim *ThinkingAnim
}

// tabBlinkMsg toggles the tab alert blink phase.
// gen must match Model.tabAlertBlinkGen; stale ticks are silently dropped.
type tabBlinkMsg struct{ gen int }

const tabBlinkHalfPeriod = 500 * time.Millisecond

// sessionsSpinnerMsg advances the sessions-list loading spinner.
// gen must match Model.sessionsSpinnerGen; stale ticks are silently dropped.
type sessionsSpinnerMsg struct{ gen int }

// sessionsSpinnerPeriod sets the sessions-list spinner cadence. A list spinner
// doesn't need the chat spinner's 30fps; 12fps reads as smooth and is cheap.
const sessionsSpinnerPeriod = time.Second / 12

// ThinkingAnim renders a spinner row: each character cycles through braille
// spinner frames with a phase offset so a wave ripples across the bar.
// Colors follow a gradient across the bar.
type ThinkingAnim struct {
	ramp   []color.Color
	step   int
	active bool
	gen    int // incremented on every Stop; invalidates in-flight ticks
}

// NewThinkingAnim creates a new animation.
func NewThinkingAnim() ThinkingAnim {
	return ThinkingAnim{
		ramp: makeGradient(animNumChars, animColorA, animColorB),
	}
}

// Start activates the animation and resets timing.
// If the animation is already running, it is a no-op — this prevents
// multiple concurrent tick loops from accumulating when Start is called
// repeatedly (e.g. on each workflow step).
func (a *ThinkingAnim) Start() tea.Cmd {
	if a.active {
		return nil
	}
	a.active = true
	a.step = 0
	return a.tick()
}

// Stop deactivates the animation and bumps the generation counter so that
// any animStepMsg already queued in Bubble Tea's message channel is ignored
// when it eventually arrives.
func (a *ThinkingAnim) Stop() {
	a.active = false
	a.gen++
}

// Resume restarts the animation tick loop after a tab switch without resetting
// the step counter. It bumps the generation so any stale in-flight ticks from
// before the switch are silently dropped. If the animation is not active it is
// a no-op.
func (a *ThinkingAnim) Resume() tea.Cmd {
	if !a.active {
		return nil
	}
	a.gen++
	return a.tick()
}

// Advance moves to the next frame if msg belongs to this instance and the
// current generation, and returns a tick command. Ticks from a different
// ThinkingAnim (cross-session collision) or a stale generation are dropped.
func (a *ThinkingAnim) Advance(msg animStepMsg) tea.Cmd {
	if msg.anim != a || !a.active || msg.gen != a.gen {
		return nil
	}
	a.step++
	return a.tick()
}

// View renders the current animation frame with left padding.
func (a *ThinkingAnim) View() string {
	if !a.active {
		return ""
	}
	var b strings.Builder
	b.WriteString("  ") // indent to align with chat content

	nFrames := len(animFrames)
	for i := range animNumChars {
		// Each character is one step behind its left neighbour, creating a wave.
		frame := (a.step - i + nFrames*animNumChars) % nFrames
		g := animFrames[frame]
		b.WriteString(lipgloss.NewStyle().Foreground(a.ramp[i]).Render(string(g)))
	}
	return b.String()
}

func (a *ThinkingAnim) tick() tea.Cmd {
	gen := a.gen
	anim := a
	return tea.Tick(time.Second/animFPS, func(time.Time) tea.Msg {
		return animStepMsg{gen: gen, anim: anim}
	})
}

// makeGradient blends two colors into a ramp of the given size.
func makeGradient(size int, a, b color.Color) []color.Color {
	ca, _ := colorful.MakeColor(a)
	cb, _ := colorful.MakeColor(b)
	ramp := make([]color.Color, size)
	for i := range ramp {
		t := float64(i) / float64(size-1)
		ramp[i] = ca.BlendHcl(cb, t)
	}
	return ramp
}
