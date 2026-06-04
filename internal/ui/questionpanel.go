package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/protocol"
)

// QuestionPanelResult describes the outcome of a key event in the question panel.
type QuestionPanelResult int

const (
	QPNoop      QuestionPanelResult = iota // key consumed, no action
	QPSubmitted                            // user submitted answer(s)
	QPCancelled                            // user cancelled
)

// questionTab holds the state for a single question tab.
type questionTab struct {
	id            string
	category      string
	question      string
	questionLines []string                       // pre-rendered question text lines for this tab
	options       []string                       // display labels; last is always "Type something." (simple mode)
	richOptions   []protocol.EventQuestionOption // structured options (workflow tool steps)
	selected      int                            // cursor position within this tab's options
	answer        string                         // recorded answer
	answerText    string                         // text input value when has_user_input
	answered      bool
}

// maxQuestionLines is the maximum number of question-text lines shown at once in the panel.
const maxQuestionLines = 5

// QuestionPanel is a dedicated input panel for answering questions with selectable options.
type QuestionPanel struct {
	visible           bool
	tabs              []questionTab
	currentTab        int
	textInput         textarea.Model // shared inline textarea for "Type something." option
	width             int
	maxVisible        int // max visible options before scrolling
	offset            int // scroll offset for current tab's options
	confirmMode          bool     // true when used for tool permission prompts
	confirmDirRequest    bool     // true when confirming directory access (3 options)
	confirmRequestedDirs []string // directories being requested in a directory-access confirm
	preview              string   // tool preview content shown in confirm mode
	questionLines     []string // pre-split rendered question text lines
	questionOffset    int      // scroll offset within the question text block
}

// NewQuestionPanel returns a QuestionPanel with its inline textarea fully
// initialized. Call this once at Model construction so SetWidth and other
// panel operations never see a zero-valued textarea (whose embedded viewport
// nil-derefs on SetWidth). Open / OpenConfirm only mutate panel state; the
// textarea is configured exactly once, here.
func NewQuestionPanel() QuestionPanel {
	ti := textarea.New()
	ti.Placeholder = "Type your answer..."
	ti.Prompt = ""
	ti.ShowLineNumbers = false
	ti.SetHeight(1)
	ti.MaxHeight = 1
	ti.CharLimit = 0

	noStyle := lipgloss.NewStyle()
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	s := ti.Styles()
	s.Focused.Base = noStyle
	s.Focused.CursorLine = noStyle
	s.Focused.Placeholder = dimStyle
	s.Focused.Text = noStyle
	s.Focused.EndOfBuffer = noStyle
	s.Focused.Prompt = noStyle
	s.Blurred = s.Focused
	ti.SetStyles(s)

	return QuestionPanel{textInput: ti}
}

// renderQuestionLines renders the question text into display lines using markdown.
func renderQuestionLines(question string, innerWidth int, md *MarkdownRenderer) []string {
	rendered := strings.TrimLeft(md.Render(question), "\n")
	lines := strings.Split(rendered, "\n")
	// Trim trailing empty lines
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Open initializes the panel from a question event.
func (qp *QuestionPanel) Open(event protocol.EventUserQuestion, width int, md *MarkdownRenderer) {
	qp.visible = true
	qp.width = width
	qp.currentTab = 0
	qp.offset = 0
	qp.maxVisible = 8
	qp.confirmMode = false
	qp.textInput.Reset()
	qp.textInput.SetWidth(width - 4 - 5)

	innerWidth := width - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	// Build tabs from event
	if len(event.Questions) > 0 {
		// Multi-question batch mode
		qp.tabs = make([]questionTab, len(event.Questions))
		for i, q := range event.Questions {
			opts := make([]string, len(q.Options))
			copy(opts, q.Options)
			opts = append(opts, "Type something.")
			qp.tabs[i] = questionTab{
				id:            q.ID,
				category:      q.Category,
				question:      q.Question,
				questionLines: renderQuestionLines(q.Question, innerWidth, md),
				options:       opts,
				selected:      0,
			}
		}
	} else if len(event.RichOptions) > 0 {
		// Rich options mode (workflow tool steps)
		cat := event.Category
		if cat == "" {
			cat = "Question"
		}
		qp.tabs = []questionTab{{
			id:          "single",
			category:    cat,
			question:    event.Question,
			richOptions: event.RichOptions,
			selected:    0,
		}}
	} else {
		// Single question mode (simple string options)
		opts := make([]string, len(event.Options))
		copy(opts, event.Options)
		opts = append(opts, "Type something.")
		cat := event.Category
		if cat == "" {
			cat = "Question"
		}
		qp.tabs = []questionTab{{
			id:       "single",
			category: cat,
			question: event.Question,
			options:  opts,
			selected: 0,
		}}
	}

	// Render question text into lines for display in the panel (single/rich-option modes)
	qp.questionLines = renderQuestionLines(event.Question, innerWidth, md)
	qp.questionOffset = 0

	// Focus text input if first option is the text option
	qp.syncTextInputFocus()
}

// buildDirAccessQuestion builds the permission question shown when a tool
// wants to access directories outside the working directory. It always includes
// the full paths so the user can see exactly what is being requested.
func buildDirAccessQuestion(dirs []string) string {
	if len(dirs) == 1 {
		return "Allow access to `" + dirs[0] + "`?"
	}
	return "Allow access to these directories?\n\n- `" + strings.Join(dirs, "`\n- `") + "`"
}

// OpenConfirm initializes the panel for a tool permission prompt.
// It shows a preview of what the tool will do and offers only Accept/Deny options.
func (qp *QuestionPanel) OpenConfirm(toolName string, params map[string]any, requestedDirs []string, width int, md *MarkdownRenderer) {
	qp.visible = true
	qp.width = width
	qp.currentTab = 0
	qp.offset = 0
	qp.maxVisible = 8
	qp.confirmMode = true
	qp.confirmDirRequest = len(requestedDirs) > 0
	qp.confirmRequestedDirs = requestedDirs
	qp.preview = buildConfirmPreview(toolName, params)

	options := []string{"Yes, allow", "No, deny"}
	question := buildConfirmQuestion(toolName, params)

	if len(requestedDirs) > 0 {
		question = buildDirAccessQuestion(requestedDirs)
		dirList := strings.Join(requestedDirs, "\n  ")
		qp.preview = "Directories:\n  " + dirList + "\n\nTool: " + toolName
		if cmd, ok := params["command"].(string); ok {
			qp.preview += "\nCommand: " + cmd
		}
		options = []string{"Allow once", "Allow and remember", "Deny"}
	}

	qp.tabs = []questionTab{{
		id:       "confirm",
		category: "Permission",
		question: question,
		options:  options,
		selected: 0,
	}}

	// Render question text into lines for display in the panel
	innerWidth := width - 4
	if innerWidth < 20 {
		innerWidth = 20
	}
	qp.questionLines = renderQuestionLines(question, innerWidth, md)
	qp.questionOffset = 0
}

// buildConfirmQuestion builds the permission question shown in the panel and chat.
// For file-touching tools it includes the target path so the user knows exactly
// which file is being requested.
func buildConfirmQuestion(toolName string, params map[string]any) string {
	switch toolName {
	case "write_file", "edit_file", "edit_minified_file", "delete_file":
		if path, ok := params["path"].(string); ok && path != "" {
			return "Allow `" + toolName + "` on `" + path + "`?"
		}
	}
	return "Allow " + toolName + "?"
}

// buildConfirmPreview builds a plain-text preview of the tool operation for display.
func buildConfirmPreview(toolName string, params map[string]any) string {
	const maxLines = 12
	switch toolName {
	case "write_file":
		path, _ := params["path"].(string)
		content, _ := params["content"].(string)
		lines := strings.Split(content, "\n")
		truncated := false
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			truncated = true
		}
		preview := strings.Join(lines, "\n")
		if truncated {
			preview += "\n…"
		}
		return "📄 " + path + "\n" + preview

	case "edit_file", "edit_minified_file":
		path, _ := params["path"].(string)
		old, _ := params["old_string"].(string)
		newStr, _ := params["new_string"].(string)
		oldLines := strings.Split(old, "\n")
		newLines := strings.Split(newStr, "\n")
		var sb strings.Builder
		sb.WriteString("✏️  " + path + "\n")
		for i, l := range oldLines {
			if i >= maxLines/2 {
				sb.WriteString("- …\n")
				break
			}
			sb.WriteString("- " + l + "\n")
		}
		for i, l := range newLines {
			if i >= maxLines/2 {
				sb.WriteString("+ …\n")
				break
			}
			sb.WriteString("+ " + l + "\n")
		}
		return strings.TrimRight(sb.String(), "\n")

	case "delete_file":
		path, _ := params["path"].(string)
		return "🗑️  " + path

	default:
		return ""
	}
}

// QAPair holds a question and its answer for rendering in chat history.
type QAPair struct {
	Category string
	Question string
	Answer   string
}

// GetAnsweredPairs returns the Q&A pairs from the current tabs (call before Close).
func (qp *QuestionPanel) GetAnsweredPairs() []QAPair {
	pairs := make([]QAPair, 0, len(qp.tabs))
	for _, tab := range qp.tabs {
		if tab.answered {
			pairs = append(pairs, QAPair{
				Category: tab.category,
				Question: tab.question,
				Answer:   tab.answer,
			})
		}
	}
	return pairs
}

// CurrentTab returns the current tab's category and question as a QAPair (without answer).
func (qp *QuestionPanel) CurrentTab() QAPair {
	if qp.currentTab < len(qp.tabs) {
		t := qp.tabs[qp.currentTab]
		return QAPair{Category: t.category, Question: t.question}
	}
	return QAPair{Category: "Question"}
}

// CurrentAnswerText returns the answerText of the current tab (for has_user_input options).
func (qp *QuestionPanel) CurrentAnswerText() string {
	if qp.currentTab < len(qp.tabs) {
		return qp.tabs[qp.currentTab].answerText
	}
	return ""
}

// optionCount returns the number of selectable options in the given tab.
func (tab *questionTab) optionCount() int {
	if len(tab.richOptions) > 0 {
		return len(tab.richOptions)
	}
	return len(tab.options)
}

// Close hides the panel and resets state.
func (qp *QuestionPanel) Close() {
	qp.visible = false
	qp.tabs = nil
	qp.currentTab = 0
	qp.confirmMode = false
	qp.preview = ""
}

// IsVisible returns whether the panel is showing.
func (qp *QuestionPanel) IsVisible() bool {
	return qp.visible
}

// SetWidth updates the panel width on terminal resize.
func (qp *QuestionPanel) SetWidth(width int) {
	qp.width = width
	if qp.visible {
		qp.textInput.SetWidth(width - 9)
	}
}

// isMultiTab returns true if there are multiple question tabs.
func (qp *QuestionPanel) isMultiTab() bool {
	return len(qp.tabs) > 1
}

// currentTabRef returns a pointer to the current tab.
func (qp *QuestionPanel) currentTabRef() *questionTab {
	if qp.currentTab >= 0 && qp.currentTab < len(qp.tabs) {
		return &qp.tabs[qp.currentTab]
	}
	return nil
}

// activeQuestionLines returns the question text lines to display: the current
// tab's lines (populated for single- and multi-question batches), falling back
// to the panel-level lines used by rich-option and confirm modes.
func (qp *QuestionPanel) activeQuestionLines() []string {
	if tab := qp.currentTabRef(); tab != nil && len(tab.questionLines) > 0 {
		return tab.questionLines
	}
	return qp.questionLines
}

// isOnTextOption returns true if the cursor is on a text-input option.
// For rich options: checks has_user_input on the selected option.
// For simple options: checks if cursor is on the last option ("Type something.").
func (qp *QuestionPanel) isOnTextOption() bool {
	if qp.confirmMode {
		return false
	}
	tab := qp.currentTabRef()
	if tab == nil {
		return false
	}
	if len(tab.richOptions) > 0 {
		return tab.selected < len(tab.richOptions) && tab.richOptions[tab.selected].HasUserInput
	}
	return tab.selected == len(tab.options)-1
}

// syncTextInputFocus focuses or blurs the text input based on cursor position.
func (qp *QuestionPanel) syncTextInputFocus() {
	if qp.confirmMode {
		return
	}
	if qp.isOnTextOption() {
		qp.textInput.Focus()
	} else {
		qp.textInput.Blur()
	}
}

// allAnswered returns true if every tab has been answered.
func (qp *QuestionPanel) allAnswered() bool {
	for _, tab := range qp.tabs {
		if !tab.answered {
			return false
		}
	}
	return true
}

// HandleKey processes a key event and returns (result, singleAnswer, batchAnswers).
func (qp *QuestionPanel) HandleKey(msg tea.KeyPressMsg) (QuestionPanelResult, string, map[string]string) {
	tab := qp.currentTabRef()
	if tab == nil {
		return QPNoop, "", nil
	}

	switch msg.String() {
	case "esc":
		return QPCancelled, "", nil

	case "pgup":
		qp.questionOffset -= maxQuestionLines
		if qp.questionOffset < 0 {
			qp.questionOffset = 0
		}
		return QPNoop, "", nil

	case "pgdown":
		qp.questionOffset += maxQuestionLines
		maxOff := len(qp.activeQuestionLines()) - maxQuestionLines
		if maxOff < 0 {
			maxOff = 0
		}
		if qp.questionOffset > maxOff {
			qp.questionOffset = maxOff
		}
		return QPNoop, "", nil

	case "up":
		if tab.selected > 0 {
			tab.selected--
			// Adjust scroll offset
			if tab.selected < qp.offset {
				qp.offset = tab.selected
			}
			qp.syncTextInputFocus()
		}
		return QPNoop, "", nil

	case "down":
		if tab.selected < tab.optionCount()-1 {
			tab.selected++
			// Adjust scroll offset
			if tab.selected >= qp.offset+qp.maxVisible {
				qp.offset = tab.selected - qp.maxVisible + 1
			}
			qp.syncTextInputFocus()
		}
		return QPNoop, "", nil

	case "left", "ctrl+h":
		if qp.isMultiTab() && qp.currentTab > 0 {
			qp.currentTab--
			qp.offset = 0
			qp.questionOffset = 0
			qp.textInput.Reset()
			qp.syncTextInputFocus()
		}
		return QPNoop, "", nil

	case "right", "ctrl+l":
		if qp.isMultiTab() && qp.currentTab < len(qp.tabs)-1 {
			qp.currentTab++
			qp.offset = 0
			qp.questionOffset = 0
			qp.textInput.Reset()
			qp.syncTextInputFocus()
		}
		return QPNoop, "", nil

	case "enter":
		return qp.handleEnter()

	}

	// Forward to text input when on text option
	if qp.isOnTextOption() {
		var cmd tea.Cmd
		qp.textInput, cmd = qp.textInput.Update(msg)
		_ = cmd
		return QPNoop, "", nil
	}

	return QPNoop, "", nil
}

// handleEnter processes the Enter key.
func (qp *QuestionPanel) handleEnter() (QuestionPanelResult, string, map[string]string) {
	tab := qp.currentTabRef()
	if tab == nil {
		return QPNoop, "", nil
	}

	// Determine the answer for the current selection
	var answer string
	if len(tab.richOptions) > 0 {
		// Rich options mode
		if tab.selected >= len(tab.richOptions) {
			return QPNoop, "", nil
		}
		opt := tab.richOptions[tab.selected]
		answer = opt.Title
		if opt.HasUserInput {
			text := strings.TrimSpace(qp.textInput.Value())
			if text == "" {
				return QPNoop, "", nil // don't submit empty text on has_user_input
			}
			tab.answerText = text
		}
	} else if qp.isOnTextOption() {
		text := strings.TrimSpace(qp.textInput.Value())
		if text == "" {
			return QPNoop, "", nil // don't submit empty text
		}
		answer = text
	} else {
		answer = tab.options[tab.selected]
	}

	if qp.isMultiTab() {
		// Record answer for current tab
		tab.answer = answer
		tab.answered = true
		qp.textInput.Reset()

		// If all answered, submit
		if qp.allAnswered() {
			answers := make(map[string]string, len(qp.tabs))
			for _, t := range qp.tabs {
				answers[t.id] = t.answer
			}
			return QPSubmitted, "", answers
		}

		// Auto-advance to next unanswered tab
		for i := 0; i < len(qp.tabs); i++ {
			next := (qp.currentTab + 1 + i) % len(qp.tabs)
			if !qp.tabs[next].answered {
				qp.currentTab = next
				qp.offset = 0
				qp.questionOffset = 0
				qp.syncTextInputFocus()
				break
			}
		}
		return QPNoop, "", nil
	}

	// Single tab mode — submit immediately
	return QPSubmitted, answer, nil
}

// Height returns the total rendered height of the panel.
func (qp *QuestionPanel) Height() int {
	if !qp.visible || len(qp.tabs) == 0 {
		return 4 // default input height
	}

	tab := qp.currentTabRef()
	if tab == nil {
		return 4
	}

	h := 0
	// Tab bar (multi-tab)
	if qp.isMultiTab() {
		h++ // tab bar line
	}

	// Question text lines: prefer the current tab's lines (populated for
	// single- and multi-question batches), falling back to the panel-level
	// lines used by rich-option and confirm modes.
	activeLines := qp.activeQuestionLines()
	qLines := len(activeLines)
	if qLines > 0 {
		shown := qLines
		if shown > maxQuestionLines {
			shown = maxQuestionLines
		}
		h += shown
		if shown == 1 {
			h++ // leading blank line added for single-line vertical centering
		}
		h++ // blank separator
	}

	// Options (capped by maxVisible)
	visible := tab.optionCount()
	if visible > qp.maxVisible {
		visible = qp.maxVisible
	}
	h += visible

	// Text input line if on text option
	if qp.isOnTextOption() {
		h++ // the text input itself
	}

	h++ // divider
	h++ // help text

	return h
}

// Render produces the styled panel content.
func (qp *QuestionPanel) Render(s Styles, focused bool, md *MarkdownRenderer) string {
	if !qp.visible || len(qp.tabs) == 0 {
		return ""
	}

	tab := qp.currentTabRef()
	if tab == nil {
		return ""
	}

	var sb strings.Builder
	innerWidth := qp.width - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	borderColor := s.ColorBlurBorder
	if focused {
		borderColor = colorSecondary
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)

	// Determine which question lines to display for the current tab: prefer the
	// current tab's lines (populated for single- and multi-question batches),
	// falling back to the panel-level lines used by rich-option and confirm modes.
	activeQuestionLines := qp.activeQuestionLines()

	// Top border with category or tab bar
	scrollable := len(activeQuestionLines) > maxQuestionLines
	if qp.isMultiTab() {
		tabBar := qp.renderTabBar(s)
		tabLen := lipgloss.Width(tabBar)
		remaining := innerWidth + 2 - tabLen
		if remaining < 0 {
			remaining = 0
		}
		topBorder := borderStyle.Render("╭─") + tabBar + borderStyle.Render(strings.Repeat("─", remaining)+"╮")
		sb.WriteString(topBorder + "\n")
	} else {
		dimStyle := lipgloss.NewStyle().Foreground(colorDim)
		catRendered := questionPanelCategoryStyle.Render(" " + tab.category + " ")
		var labelRendered string
		if scrollable {
			labelRendered = catRendered + borderStyle.Render("─") + dimStyle.Render(" ↑/↓ PgUp/PgDn to scroll ") + borderStyle.Render("─")
		} else {
			labelRendered = catRendered
		}
		labelLen := lipgloss.Width(labelRendered)
		remaining := innerWidth + 2 - labelLen - 1
		if remaining < 0 {
			remaining = 0
		}
		topBorder := borderStyle.Render("╭─") + labelRendered + borderStyle.Render(strings.Repeat("─", remaining)+"╮")
		sb.WriteString(topBorder + "\n")
	}

	// Content lines helper
	writeLine := func(line string) {
		padded := lipgloss.NewStyle().Width(innerWidth).Render(line)
		sb.WriteString(borderStyle.Render("│") + " " + padded + " " + borderStyle.Render("│") + "\n")
	}

	// Question text lines
	if len(activeQuestionLines) > 0 {
		end := qp.questionOffset + maxQuestionLines
		if end > len(activeQuestionLines) {
			end = len(activeQuestionLines)
		}
		// Add a leading blank line when the question fits on a single line,
		// so it appears vertically centred rather than flush against the top border.
		visibleLines := end - qp.questionOffset
		if visibleLines == 1 {
			writeLine("")
		}
		for _, line := range activeQuestionLines[qp.questionOffset:end] {
			writeLine(line)
		}
		writeLine("")
	}

	// Options with scrolling
	optCount := tab.optionCount()
	visStart := qp.offset
	visEnd := visStart + qp.maxVisible
	if visEnd > optCount {
		visEnd = optCount
	}

	answeredStyle := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)

	if len(tab.richOptions) > 0 {
		// Rich options: title in bold, description in dim
		for i := visStart; i < visEnd; i++ {
			opt := tab.richOptions[i]
			num := fmt.Sprintf("%d. ", i+1)

			if tab.answered && i == tab.selected {
				writeLine("  " + answeredStyle.Render("✓ "+num+opt.Title) + "  " + s.QuestionPanelDescStyle.Render(opt.Description))
			} else if i == tab.selected {
				cursor := questionPanelCursorStyle.Render("› ")
				writeLine(cursor + s.QuestionPanelSelectedStyle.Render(num+opt.Title) + "  " + s.QuestionPanelDescStyle.Render(opt.Description))
			} else {
				writeLine("  " + s.QuestionPanelUnselectedStyle.Render(num+opt.Title) + "  " + s.QuestionPanelDescStyle.Render(opt.Description))
			}
		}
	} else {
		// Simple string options
		for i := visStart; i < visEnd; i++ {
			opt := tab.options[i]
			num := fmt.Sprintf("%d. ", i+1)

			if tab.answered && i == tab.selected {
				writeLine("  " + answeredStyle.Render("✓ "+num+opt))
			} else if i == tab.selected {
				cursor := questionPanelCursorStyle.Render("› ")
				writeLine(cursor + s.QuestionPanelSelectedStyle.Render(num+opt))
			} else {
				writeLine("  " + s.QuestionPanelUnselectedStyle.Render(num+opt))
			}
		}
	}

	// Text input area (shown when cursor is on text option)
	if qp.isOnTextOption() {
		inputView := qp.textInput.View()
		writeLine("     " + inputView)
	}

	// Divider
	divider := s.QuestionPanelDividerStyle.Render(strings.Repeat("─", innerWidth))
	sb.WriteString(borderStyle.Render("│") + " " + divider + " " + borderStyle.Render("│") + "\n")

	// Help text
	var help string
	if qp.isMultiTab() {
		help = "Enter to select · ↑/↓ to navigate · ←/→ for tabs · Esc to cancel"
	} else {
		help = "Enter to select · ↑/↓ to navigate · Esc to cancel"
	}
	helpRendered := s.QuestionPanelHelpStyle.Render(help)
	helpPadded := lipgloss.NewStyle().Width(innerWidth).Render(helpRendered)
	sb.WriteString(borderStyle.Render("│") + " " + helpPadded + " " + borderStyle.Render("│") + "\n")

	// Bottom border
	bottomDashes := strings.Repeat("─", innerWidth+2)
	sb.WriteString(borderStyle.Render("╰" + bottomDashes + "╯"))

	return sb.String()
}


// renderTabBar builds the tab bar for multi-question mode.
func (qp *QuestionPanel) renderTabBar(s Styles) string {
	var parts []string
	parts = append(parts, " ")

	for i, tab := range qp.tabs {
		var indicator string
		if tab.answered {
			indicator = questionPanelTabAnsweredStyle.Render("✓")
		} else if i == qp.currentTab {
			indicator = "■"
		} else {
			indicator = "□"
		}

		label := fmt.Sprintf(" %s %s ", indicator, tab.category)
		if i == qp.currentTab {
			parts = append(parts, questionPanelTabActiveStyle.Render(label))
		} else if tab.answered {
			parts = append(parts, questionPanelTabAnsweredStyle.Render(label))
		} else {
			parts = append(parts, s.QuestionPanelTabStyle.Render(label))
		}
	}

	parts = append(parts, " ")
	return strings.Join(parts, "")
}
