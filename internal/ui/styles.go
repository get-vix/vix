package ui

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/get-vix/vix/internal/config"
)

// lighten blends a hex color toward white by the given factor (0.0 = unchanged, 1.0 = white).
func lighten(hex string, factor float64) color.Color {
	c, _ := colorful.Hex(hex)
	white := colorful.Color{R: 1, G: 1, B: 1}
	blended := c.BlendLab(white, factor)
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", int(blended.R*255), int(blended.G*255), int(blended.B*255)))
}

var (
	primaryHex   = "#BC63FC"
	secondaryHex = "#A3FC63"
)

var (
	// Brand colors (true color hex for consistent identity)
	colorPrimary    = lipgloss.Color(primaryHex)      // Coral
	colorSecondary  = lipgloss.Color(secondaryHex)     // Sky blue
	colorAccentWarm = lighten(primaryHex, 0.3)         // Light coral (derived)
	colorAccentCool = lighten(secondaryHex, 0.3)       // Light sky blue (derived)

	// Semantic colors (ANSI for terminal compatibility)
	colorError   = lipgloss.Color("1") // Red
	colorSuccess = lipgloss.Color("2") // Green
	colorWarning = lipgloss.Color("3") // Yellow

	// Structural (ANSI blue for badges/headers with dark backgrounds)
	colorStructural = lipgloss.Color("4") // Blue

	// Neutral
	colorDim = lipgloss.Color("245") // Muted text, descriptions

	// User prompt
	userPromptStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15"))

	userPromptIcon = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary)

	userTimestampStyle = lipgloss.NewStyle().
				Foreground(colorDim)

	// Tool call
	toolCallStyle = lipgloss.NewStyle().
			Foreground(colorSecondary)

	toolCallDot = lipgloss.NewStyle().
			Foreground(colorPrimary)

	// Diff lines in tool results
	diffRemoveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203"))
	diffAddStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)
	diffEmptyStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")) // mid-tone grey bg for empty side of a pure-add/delete

	// Error
	errorStyle = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	// System success (e.g., reconnection messages)
	systemSuccessStyle = lipgloss.NewStyle().
				Foreground(colorSuccess).
				Italic(true)

	// Retry status
	retryStyle = lipgloss.NewStyle().
			Foreground(colorWarning).
			Italic(true)

	statusConnectedStyle = lipgloss.NewStyle().
				Foreground(colorSuccess)

	statusDisconnectedStyle = lipgloss.NewStyle().
				Foreground(colorError)

	statusReconnectingStyle = lipgloss.NewStyle().
				Foreground(colorWarning)

	// Input area
	inputPromptStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	// Chat mode bar - amber
	chatBarStyle = lipgloss.NewStyle().
			Foreground(colorPrimary)

	// Plan/workflow mode bar - teal
	planBarStyle = lipgloss.NewStyle().
			Foreground(colorSecondary)

	// Confirm prompt
	confirmStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	// Plan styles
	planHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(colorStructural)

	planDoneHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("0")).
				Background(colorSecondary)

	planTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSecondary)

	planRunningStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	planDoneStyle = lipgloss.NewStyle().
			Foreground(colorSuccess).
			Bold(true)

	planFailStyle = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	// History
	historyArrowStyle = lipgloss.NewStyle().
		Foreground(colorPrimary)

	historyBorderStyle = lipgloss.NewStyle().
		Foreground(colorSecondary)

	// Plan prompt
	planPromptActionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorSecondary)

	planPromptKeyStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorPrimary)

	// Question panel
	questionPanelCategoryStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("0")).
					Background(colorStructural)

	questionPanelCursorStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	questionPanelTabActiveStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("0")).
					Background(colorStructural)

	questionPanelTabAnsweredStyle = lipgloss.NewStyle().
					Foreground(colorSuccess)
)

// Styles holds all styles that adapt to the terminal background color.
// On dark backgrounds, "white" text is ANSI 15 and "dim" text is ANSI 8.
// On light backgrounds, "white" text becomes ANSI 0 (black) and "dim" becomes ANSI 7 (silver).
type Styles struct {
	// Adaptive colors for inline use
	ColorWhite       color.Color
	ColorDimGray     color.Color
	ColorBlurBorder  color.Color

	// Styles using colorWhite
	ToolResultStyle            lipgloss.Style
	QuestionTextStyle          lipgloss.Style
	PlanDescStyle              lipgloss.Style
	HistorySelectedStyle       lipgloss.Style
	QuestionPanelSelectedStyle lipgloss.Style

	// Styles using colorDimGray
	ToolCallReasonStyle          lipgloss.Style
	SystemStyle                  lipgloss.Style
	ThinkingStyle                lipgloss.Style
	StatusBarStyle               lipgloss.Style
	PlanBulletStyle              lipgloss.Style
	PlanPromptDimStyle           lipgloss.Style
	HistoryPanelStyle            lipgloss.Style
	QuestionPanelUnselectedStyle lipgloss.Style
	QuestionPanelDescStyle       lipgloss.Style
	QuestionPanelDividerStyle    lipgloss.Style
	QuestionPanelHelpStyle       lipgloss.Style
	QuestionPanelTabStyle        lipgloss.Style
	CodeBoxBorderStyle           lipgloss.Style

	// Viewport border (focus-aware)
	ViewportFocusedStyle lipgloss.Style
	ViewportBlurredStyle lipgloss.Style

	// Command palette
	CommandPaletteStyle         lipgloss.Style
	CommandPaletteSelectedStyle lipgloss.Style
	CommandPaletteSepStyle      lipgloss.Style

	// File completer popup
	FileCompleterStyle lipgloss.Style

	// Right panel
	RightPanelStyle lipgloss.Style

	// Tab bar
	TabActiveStyle   lipgloss.Style
	TabInactiveStyle lipgloss.Style
	TabAlertStyle    lipgloss.Style
}

// NewStyles creates a Styles set appropriate for the terminal background.
func NewStyles(hasDarkBG bool) Styles {
	white := lipgloss.Color("15")
	dimGray := lipgloss.Color("245")
	lightBlue := lipgloss.Color("117") // dark BG: bright sky blue
	blurredBorder := lipgloss.Color("240") // dark: subtle grey
	if !hasDarkBG {
		white = lipgloss.Color("0")
		dimGray = lipgloss.Color("7")
		lightBlue = lipgloss.Color("33") // light BG: saturated blue that reads on white
		blurredBorder = lipgloss.Color("250") // light: lighter grey for clear contrast vs black
	}

	return Styles{
		ColorWhite:      white,
		ColorDimGray:    dimGray,
		ColorBlurBorder: blurredBorder,

		ToolResultStyle:            lipgloss.NewStyle().Foreground(white),
		QuestionTextStyle:          lipgloss.NewStyle().Foreground(white),
		PlanDescStyle:              lipgloss.NewStyle().Foreground(white).Italic(true),
		HistorySelectedStyle:       lipgloss.NewStyle().Bold(true).Foreground(white),
		QuestionPanelSelectedStyle: lipgloss.NewStyle().Foreground(white).Bold(true),

		ToolCallReasonStyle:          lipgloss.NewStyle().Foreground(dimGray).Italic(true),
		SystemStyle:                  lipgloss.NewStyle().Foreground(dimGray).Italic(true),
		ThinkingStyle:                lipgloss.NewStyle().Foreground(lightBlue).Italic(true),
		StatusBarStyle:               lipgloss.NewStyle().Foreground(dimGray).Padding(0, 1),
		PlanBulletStyle:              lipgloss.NewStyle().Foreground(dimGray),
		PlanPromptDimStyle:           lipgloss.NewStyle().Foreground(dimGray),
		HistoryPanelStyle:            lipgloss.NewStyle().Foreground(dimGray),
		QuestionPanelUnselectedStyle: lipgloss.NewStyle().Foreground(dimGray),
		QuestionPanelDescStyle:       lipgloss.NewStyle().Foreground(dimGray),
		QuestionPanelDividerStyle:    lipgloss.NewStyle().Foreground(dimGray),
		QuestionPanelHelpStyle:       lipgloss.NewStyle().Foreground(dimGray).Italic(true),
		QuestionPanelTabStyle:        lipgloss.NewStyle().Foreground(dimGray),
		CodeBoxBorderStyle:           lipgloss.NewStyle().Foreground(dimGray),

		ViewportFocusedStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderTop(false).
			BorderForeground(white).
			Padding(0, 1),
		ViewportBlurredStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderTop(false).
			BorderForeground(blurredBorder).
			Padding(0, 1),

		CommandPaletteStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSecondary).
			Padding(1, 1),
		CommandPaletteSelectedStyle: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSecondary),
		CommandPaletteSepStyle: lipgloss.NewStyle().
			Foreground(dimGray),

		FileCompleterStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 1),

		RightPanelStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(blurredBorder).
			Padding(0, 1),

		TabActiveStyle:   lipgloss.NewStyle().Bold(true).Foreground(colorPrimary),
		TabInactiveStyle: lipgloss.NewStyle().Foreground(colorDim),
		TabAlertStyle:    lipgloss.NewStyle().Bold(true).Foreground(colorSecondary),
	}
}

// ApplyTheme updates brand colors from user/project config.
// Must be called before NewModel(). Empty fields keep defaults.
func ApplyTheme(tc config.ThemeConfig) {
	if tc.Primary != "" {
		primaryHex = tc.Primary
	}
	if tc.Secondary != "" {
		secondaryHex = tc.Secondary
	}

	// Rebuild all brand-derived colors
	colorPrimary = lipgloss.Color(primaryHex)
	colorSecondary = lipgloss.Color(secondaryHex)
	colorAccentWarm = lighten(primaryHex, 0.3)
	colorAccentCool = lighten(secondaryHex, 0.3)

	// Rebuild animation colors
	animColorA = lipgloss.Color(primaryHex)
	animColorB = lipgloss.Color(secondaryHex)

	// Rebuild all styles that reference brand colors
	userPromptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	userPromptIcon = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	toolCallStyle = lipgloss.NewStyle().Foreground(colorSecondary)
	toolCallDot = lipgloss.NewStyle().Foreground(colorPrimary)
	inputPromptStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	chatBarStyle = lipgloss.NewStyle().Foreground(colorPrimary)
	planBarStyle = lipgloss.NewStyle().Foreground(colorSecondary)
	confirmStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	planTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorSecondary)
	planDoneHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(colorSecondary)
	planRunningStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	historyArrowStyle = lipgloss.NewStyle().Foreground(colorPrimary)
	historyBorderStyle = lipgloss.NewStyle().Foreground(colorSecondary)
	planPromptActionStyle = lipgloss.NewStyle().Bold(true).Foreground(colorSecondary)
	planPromptKeyStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	questionPanelCursorStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
}
