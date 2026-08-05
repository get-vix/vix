package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Command represents an action in the command palette.
type Command struct {
	Name        string
	Description string
	Action      string // identifier returned when selected
	Group       string // display section; "" sorts into a default group
}

// CommandPalette is a filterable overlay triggered by Ctrl+P.
type CommandPalette struct {
	visible       bool
	filter        textinput.Model
	commands      []Command
	extraCommands []Command
	filtered      []Command
	selected      int
}

// NewCommandPalette creates a command palette with default commands.
func NewCommandPalette() CommandPalette {
	ti := textinput.New()
	ti.Placeholder = "Type a command..."
	ti.Prompt = "  "

	commands := []Command{
		{Name: "Search History", Description: "Search input history (Ctrl+R)", Action: "history"},
		{Name: "Scroll to Top", Description: "Jump to beginning of chat", Action: "scroll_top"},
		{Name: "Scroll to Bottom", Description: "Jump to end of chat", Action: "scroll_bottom"},
		{Name: "Quit", Description: "Exit the application", Action: "quit"},
	}

	return CommandPalette{
		filter:   ti,
		commands: commands,
		filtered: commands,
	}
}

// BaseCommands returns the static command list (excluding ephemeral extraCommands).
func (c *CommandPalette) BaseCommands() []Command {
	return c.commands
}

// SetExtraCommands injects dynamic commands (e.g. tab-switch entries) into the
// palette. Call this before Open() so they are included in filtering.
func (c *CommandPalette) SetExtraCommands(cmds []Command) {
	c.extraCommands = cmds
}

// Open shows the command palette and focuses the filter input.
func (c *CommandPalette) Open() {
	c.visible = true
	c.filter.SetValue("")
	c.filter.Focus()
	c.filtered = append(c.commands, c.extraCommands...)
	c.selected = 0
}

// Close hides the command palette.
func (c *CommandPalette) Close() {
	c.visible = false
	c.filter.Blur()
}

// IsVisible returns whether the palette is showing.
func (c *CommandPalette) IsVisible() bool {
	return c.visible
}

// Update handles a key press and returns the selected action (if any) and whether the key was consumed.
func (c *CommandPalette) Update(msg tea.KeyPressMsg) (action string, consumed bool) {
	switch msg.String() {
	case "esc":
		c.Close()
		return "", true
	case "enter":
		if len(c.filtered) > 0 && c.selected < len(c.filtered) {
			action := c.filtered[c.selected].Action
			c.Close()
			return action, true
		}
		return "", true
	case "up":
		if c.selected > 0 {
			c.selected--
		}
		return "", true
	case "down":
		if c.selected < len(c.filtered)-1 {
			c.selected++
		}
		return "", true
	default:
		// Forward to text input for filtering
		c.filter, _ = c.filter.Update(msg)
		c.applyFilter()
		return "", true
	}
}

// applyFilter re-filters the command list based on the current input.
func (c *CommandPalette) applyFilter() {
	all := append(c.commands, c.extraCommands...)
	query := strings.ToLower(c.filter.Value())
	if query == "" {
		c.filtered = all
	} else {
		c.filtered = nil
		for _, cmd := range all {
			if strings.Contains(strings.ToLower(cmd.Name), query) {
				c.filtered = append(c.filtered, cmd)
			}
		}
	}
	if c.selected >= len(c.filtered) {
		c.selected = max(0, len(c.filtered)-1)
	}
}

// View renders the command palette overlay.
func (c *CommandPalette) View(width, height int, s Styles) string {
	// Compute column widths from all commands so alignment is stable while filtering.
	all := append(c.commands, c.extraCommands...)
	maxNameLen, maxDescLen := 0, 0
	for _, cmd := range all {
		if n := len(cmd.Name); n > maxNameLen {
			maxNameLen = n
		}
		if d := len(cmd.Description); d > maxDescLen {
			maxDescLen = d
		}
	}

	// cursor(2) + name col + gap(3) + desc col + border padding(4)
	paletteWidth := 2 + maxNameLen + 3 + maxDescLen + 4
	if paletteWidth > width-4 {
		paletteWidth = width - 4
	}
	innerWidth := paletteWidth - 4

	// Filter input
	c.filter.SetWidth(innerWidth)
	filterView := c.filter.View()

	// Separator
	sep := s.CommandPaletteSepStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	// Command list
	maxItems := height/2 - 4
	if maxItems < 3 {
		maxItems = 3
	}
	if maxItems > len(c.filtered) {
		maxItems = len(c.filtered)
	}

	// Compute visible window around selected
	startIdx := 0
	if c.selected >= maxItems {
		startIdx = c.selected - maxItems + 1
	}
	endIdx := startIdx + maxItems
	if endIdx > len(c.filtered) {
		endIdx = len(c.filtered)
		startIdx = max(0, endIdx-maxItems)
	}

	var items []string
	for i := startIdx; i < endIdx; i++ {
		cmd := c.filtered[i]
		if i == c.selected {
			nameStr := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(maxNameLen).Render(cmd.Name)
			descStr := lipgloss.NewStyle().Foreground(colorPrimary).Render(cmd.Description)
			line := lipgloss.NewStyle().Width(innerWidth).Render("▸ " + nameStr + "   " + descStr)
			items = append(items, line)
		} else {
			nameStr := lipgloss.NewStyle().Foreground(colorAccentCool).Width(maxNameLen).Render(cmd.Name)
			descStr := lipgloss.NewStyle().Foreground(colorDim).Render(cmd.Description)
			line := lipgloss.NewStyle().Width(innerWidth).Render("  " + nameStr + "   " + descStr)
			items = append(items, line)
		}
	}

	if len(c.filtered) == 0 {
		items = append(items, lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render("  No matching commands"))
	}

	// Build content
	content := filterView + "\n" + sep + "\n" + strings.Join(items, "\n")

	// Wrap in bordered box
	box := s.CommandPaletteStyle.Width(paletteWidth).Render(content)

	return box
}
