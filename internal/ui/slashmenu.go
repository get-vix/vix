package ui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

const slashMenuMaxVisible = 8

// slashGroupOrder is the display order of slash-menu sections. Commands whose
// Group is empty or unrecognized fall into a trailing default section.
var slashGroupOrder = []string{"Conversation", "Skills"}

// slashCommands is the fixed list of built-in slash commands shown in the menu.
var slashCommands = []Command{
	{Name: "fork", Description: "Fork a new thread from a turn (/fork N)", Action: "slash_fork", Group: "Conversation"},
	{Name: "trim", Description: "Delete all messages AFTER a turn (/trim N)", Action: "slash_trim", Group: "Conversation"},
	{Name: "copy", Description: "Copy a turn, or the whole conversation (/copy [N])", Action: "slash_copy", Group: "Conversation"},
	{Name: "goto", Description: "Scroll to a turn's start (/goto N)", Action: "slash_goto", Group: "Conversation"},
	{Name: "clear", Description: "Clear conversation history", Action: "slash_clear", Group: "Conversation"},
	{Name: "compact", Description: "Summarize older turns to free context (/compact [N])", Action: "slash_compact", Group: "Conversation"},
	{Name: "skills", Description: "List available skills", Action: "slash_skills", Group: "Skills"},
}

// slashCommandInsertText returns the input text to insert when a parameterized
// slash command is selected from the menu (so the user can type its argument).
// ok is false for commands that should execute immediately on select.
func slashCommandInsertText(action string) (string, bool) {
	if name, ok := strings.CutPrefix(action, "slash_skill:"); ok {
		return "/" + name + " ", true
	}
	switch action {
	case "slash_fork":
		return "/fork ", true
	case "slash_trim":
		return "/trim ", true
	case "slash_copy":
		return "/copy ", true
	case "slash_goto":
		return "/goto ", true
	case "slash_compact":
		return "/compact ", true
	}
	return "", false
}

// threadSlashCommands returns the built-in slash commands followed by one
// entry per loaded skill, so skills autocomplete alongside built-ins. Selecting
// a skill inserts "/<name> " for the user to add arguments and submit.
func threadSlashCommands(sess *ThreadState) []Command {
	if sess == nil || len(sess.skills) == 0 {
		return slashCommands
	}
	cmds := make([]Command, 0, len(slashCommands)+len(sess.skills))
	cmds = append(cmds, slashCommands...)
	for _, sk := range sess.skills {
		desc := sk.Description
		if desc == "" {
			desc = "Run the " + sk.Name + " skill"
		}
		cmds = append(cmds, Command{
			Name:        sk.Name,
			Description: desc,
			Action:      "slash_skill:" + sk.Name,
			Group:       "Skills",
		})
	}
	return cmds
}

// SlashMenu is a popup that lists available slash commands matching the typed /query.
type SlashMenu struct {
	visible     bool
	allCommands []Command
	filtered    []Command
	selected    int
}

// Open shows the menu with the given commands filtered by query.
func (s *SlashMenu) Open(commands []Command, query string) {
	s.visible = true
	s.allCommands = commands
	s.applyFilter(query)
	s.selected = 0
}

// Refresh updates the filter query without changing the command list.
func (s *SlashMenu) Refresh(query string) {
	s.applyFilter(query)
	if s.selected >= len(s.filtered) {
		s.selected = max(0, len(s.filtered)-1)
	}
}

// applyFilter updates filtered based on query.
func (s *SlashMenu) applyFilter(query string) {
	if query == "" {
		s.filtered = s.allCommands
		return
	}
	lower := strings.ToLower(query)
	s.filtered = nil
	for _, cmd := range s.allCommands {
		if strings.Contains(strings.ToLower(cmd.Name), lower) {
			s.filtered = append(s.filtered, cmd)
		}
	}
}

// Close hides the menu.
func (s *SlashMenu) Close() {
	s.visible = false
}

// IsVisible returns whether the menu is showing.
func (s *SlashMenu) IsVisible() bool {
	return s.visible
}

// MoveUp moves the selection toward earlier entries.
func (s *SlashMenu) MoveUp() {
	if s.selected > 0 {
		s.selected--
	}
}

// MoveDown moves the selection toward later entries.
func (s *SlashMenu) MoveDown() {
	if s.selected < len(s.filtered)-1 {
		s.selected++
	}
}

// SelectedAction returns the Action of the currently highlighted command, or "" if empty.
func (s *SlashMenu) SelectedAction() string {
	if len(s.filtered) == 0 || s.selected < 0 || s.selected >= len(s.filtered) {
		return ""
	}
	return s.filtered[s.selected].Action
}

// extractSlashQuery returns the query string (text after /) and true when the
// textarea value starts with / and the suffix contains no whitespace.
func extractSlashQuery(value string) (query string, found bool) {
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	rest := value[1:]
	for _, r := range rest {
		if unicode.IsSpace(r) {
			return "", false
		}
	}
	return rest, true
}

// menuRow is a single rendered line in the slash menu: either a section header
// (header != "") or a command row. cmdIndex points into the filtered slice for
// command rows and is -1 for headers.
type menuRow struct {
	header   string
	cmd      Command
	cmdIndex int
}

// buildRows groups filtered commands into ordered sections, emitting a header
// row before each non-empty group. Groups listed in slashGroupOrder come first
// in that order; any commands with an unrecognized or empty Group fall into a
// trailing default section (header omitted so a single ungrouped list looks
// flat).
func buildRows(filtered []Command) []menuRow {
	if len(filtered) == 0 {
		return nil
	}
	// Collect command indices per group, preserving first-seen order for any
	// groups not named in slashGroupOrder.
	byGroup := map[string][]int{}
	var extraOrder []string
	known := map[string]bool{}
	for _, g := range slashGroupOrder {
		known[g] = true
	}
	for i, cmd := range filtered {
		g := cmd.Group
		if _, seen := byGroup[g]; !seen && g != "" && !known[g] {
			extraOrder = append(extraOrder, g)
		}
		byGroup[g] = append(byGroup[g], i)
	}

	var rows []menuRow
	emit := func(group string, withHeader bool) {
		idxs := byGroup[group]
		if len(idxs) == 0 {
			return
		}
		if withHeader && group != "" {
			rows = append(rows, menuRow{header: group, cmdIndex: -1})
		}
		for _, i := range idxs {
			rows = append(rows, menuRow{cmd: filtered[i], cmdIndex: i})
		}
	}
	for _, g := range slashGroupOrder {
		emit(g, true)
	}
	for _, g := range extraOrder {
		emit(g, false)
	}
	emit("", false) // ungrouped commands, no header
	return rows
}

func (s *SlashMenu) View(width, maxHeight int, styles Styles) string {
	if !s.visible {
		return ""
	}

	maxRows := maxHeight
	if maxRows > slashMenuMaxVisible {
		maxRows = slashMenuMaxVisible
	}

	// Build top border
	borderColor := colorPrimary
	borderCharStyle := lipgloss.NewStyle().Foreground(borderColor)
	title := " Commands "
	titleStyle := lipgloss.NewStyle().Foreground(borderColor)
	titleRendered := titleStyle.Render(title)
	titleLen := lipgloss.Width(titleRendered)
	remainingDashes := width - 3 - titleLen
	if remainingDashes < 0 {
		remainingDashes = 0
	}
	topBorder := borderCharStyle.Render("╭─") + titleRendered +
		borderCharStyle.Render(strings.Repeat("─", remainingDashes)) +
		borderCharStyle.Render("╮")

	innerWidth := width - 4 // border (2) + padding (2)
	if innerWidth < 1 {
		innerWidth = 1
	}

	if len(s.filtered) == 0 {
		emptyLine := lipgloss.NewStyle().Foreground(colorDim).Render("  (no matching commands)")
		body := styles.FileCompleterStyle.Width(width).Render(emptyLine)
		return topBorder + "\n" + body
	}

	// Compute max name length from allCommands for stable column alignment
	maxNameLen := 0
	for _, cmd := range s.allCommands {
		if n := len(cmd.Name); n > maxNameLen {
			maxNameLen = n
		}
	}

	// Group filtered commands into ordered sections with header rows.
	allRows := buildRows(s.filtered)
	total := len(allRows)
	if maxRows > total {
		maxRows = total
	}

	// Row index of the selected command (selection indexes s.filtered, never headers).
	selRow := 0
	for i, r := range allRows {
		if r.cmdIndex == s.selected {
			selRow = i
			break
		}
	}

	// Sliding window around the selected row, including header rows in the budget.
	startIdx := 0
	if selRow >= maxRows {
		startIdx = selRow - maxRows + 1
	}
	endIdx := startIdx + maxRows
	if endIdx > total {
		endIdx = total
		startIdx = max(0, endIdx-maxRows)
	}
	// Keep the selected command's section header in view when the command would
	// otherwise sit flush against the top edge of the window.
	if startIdx > 0 && allRows[startIdx].cmdIndex == s.selected && allRows[startIdx-1].header != "" {
		startIdx--
		endIdx = startIdx + maxRows
		if endIdx > total {
			endIdx = total
		}
	}

	// Width available for the description column: inner width minus the 2-char
	// row prefix, the name column, and the 3-space separator. Truncate to keep
	// each entry on a single line so long descriptions don't wrap.
	descWidth := innerWidth - maxNameLen - 5

	var rows []string
	for i := startIdx; i < endIdx; i++ {
		r := allRows[i]
		if r.header != "" {
			line := lipgloss.NewStyle().Bold(true).Foreground(colorDim).Width(innerWidth).Render(r.header)
			rows = append(rows, line)
			continue
		}
		cmd := r.cmd
		desc := truncateLabel(cmd.Description, descWidth)
		if r.cmdIndex == s.selected {
			nameStr := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(maxNameLen).Render(cmd.Name)
			descStr := lipgloss.NewStyle().Foreground(colorPrimary).Render(desc)
			line := lipgloss.NewStyle().Width(innerWidth).Render("▸ " + nameStr + "   " + descStr)
			rows = append(rows, line)
		} else {
			nameStr := lipgloss.NewStyle().Foreground(colorAccentCool).Width(maxNameLen).Render(cmd.Name)
			descStr := lipgloss.NewStyle().Foreground(colorDim).Render(desc)
			line := lipgloss.NewStyle().Width(innerWidth).Render("  " + nameStr + "   " + descStr)
			rows = append(rows, line)
		}
	}

	content := strings.Join(rows, "\n")
	body := styles.FileCompleterStyle.Width(width).Render(content)
	return topBorder + "\n" + body
}
