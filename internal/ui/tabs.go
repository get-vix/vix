package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/config"
)

// TabKind identifies the type of a tab.
type TabKind int

const (
	TabKindSessions TabKind = iota // sessions list overview
	TabKindChat                    // chat display for the selected session
	TabKindSettings                // global settings
)

// formatRunningTime formats a duration as a human-readable running time string.
func formatRunningTime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

// waitingBadge is the "Waiting for input" styled tag shown on sessions that need user attention.
var waitingBadge = lipgloss.NewStyle().Background(colorSecondary).Foreground(lipgloss.Color("0")).Bold(true).Render(" Waiting for input ")

// unreadDotStyle styles the ● indicator for sessions with unread messages.
var unreadDotStyle = lipgloss.NewStyle().Foreground(colorSecondary)

// renderSessionsView renders the sessions list overview.
func renderSessionsView(sessions []*SessionState, width, height int, s Styles, filter, inputView string, selectedRow int) string {
	const colSession = 10
	const colRunning = 10

	// Help banner: description line + shortcuts line + separator.
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	whiteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	shortcut := func(key, action string) string {
		return keyStyle.Render(key) + " " + dimStyle.Render(action)
	}
	shortcuts := strings.Join([]string{
		shortcut("a", "new"),
		shortcut("x", "close"),
		shortcut("↑↓", "navigate"),
		shortcut("enter", "open"),
		shortcut("type", "filter"),
	}, "   ")
	innerWidth := width - 4 // width outer − 2 border sides − 2 padding sides
	if innerWidth < 0 {
		innerWidth = 0
	}

	// colMessage fills the remaining space: innerWidth minus the two fixed columns,
	// the 6 characters of inter-column padding ("  " × 3 in the header), and the
	// 22-character badge slot ("  " + " Waiting for input ") always reserved so
	// the layout stays stable whether or not any session needs input.
	const badgeVisible = 22 // len("  ") + len(" Waiting for input ")
	colMessage := innerWidth - colSession - colRunning - 6 - badgeVisible
	if colMessage < 20 {
		colMessage = 20
	}
	helpBlock := whiteStyle.Render("Manage your coding sessions across workspaces.") + "\n" +
		shortcuts + "\n" +
		dimStyle.Render(strings.Repeat("─", innerWidth))

	header := fmt.Sprintf("  %-*s  %-*s  %-*s%-*s", colSession, "Session", colMessage, "First message", colRunning, "Running", badgeVisible, "")
	rows := []string{s.TabActiveStyle.Render(header)}

	filterLower := strings.ToLower(filter)
	rowIdx := 0

	for _, sess := range sessions {
		sessionCol := "connecting…"
		runningCol := "—"
		if sess.client != nil {
			id := sess.client.SessionID()
			if dash := strings.Index(id, "-"); dash >= 0 {
				sessionCol = id[:dash]
			} else if len(id) > colSession {
				sessionCol = id[:colSession]
			} else {
				sessionCol = id
			}
			if !sess.client.StartedAt().IsZero() {
				runningCol = formatRunningTime(time.Since(sess.client.StartedAt()))
			}
		}

		msgCol := "—"
		if sess.parentID != "" {
			parentShort := sess.parentID
			if dash := strings.Index(parentShort, "-"); dash >= 0 {
				parentShort = parentShort[:dash]
			} else if len(parentShort) > 8 {
				parentShort = parentShort[:8]
			}
			prefix := "⎇ " + parentShort + "/" + fmt.Sprintf("%d", sess.forkTurnIdx+1) + "  "
			rest := "—"
			for _, msg := range sess.chatMessages {
				if msg.Type == MsgUser {
					rest = strings.SplitN(msg.Text, "\n", 2)[0]
					break
				}
			}
			full := prefix + rest
			if len(full) > colMessage {
				full = full[:colMessage-1] + "…"
			}
			msgCol = full
		} else {
			for _, msg := range sess.chatMessages {
				if msg.Type == MsgUser {
					line := strings.SplitN(msg.Text, "\n", 2)[0]
					if len(line) > colMessage {
						line = line[:colMessage-1] + "…"
					}
					msgCol = line
					break
				}
			}
		}

		if filterLower != "" &&
			!strings.Contains(strings.ToLower(sessionCol), filterLower) &&
			!strings.Contains(strings.ToLower(msgCol), filterLower) {
			continue
		}

		hasUnread := sess.unreadCount > 0
		needsInput := sess.agentState == StateConfirmPending || sess.agentState == StateUserQuestion
		var badgeSlot string
		if needsInput {
			badgeSlot = "  " + waitingBadge
		} else {
			badgeSlot = strings.Repeat(" ", badgeVisible)
		}
		plainCols := fmt.Sprintf("%-*s  %-*s  %-*s", colSession, sessionCol, colMessage, msgCol, colRunning, runningCol) + badgeSlot
		if rowIdx == selectedRow {
			dotChar := " "
			if hasUnread {
				dotChar = "●"
			}
			rows = append(rows, s.TabAlertStyle.Render(dotChar+" "+plainCols))
		} else if hasUnread {
			rows = append(rows, unreadDotStyle.Render("●")+" "+plainCols)
		} else {
			rows = append(rows, "  "+plainCols)
		}
		rowIdx++
	}

	content := helpBlock + "\n" + inputView + "\n" + strings.Join(rows, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}

// renderSettingsView renders the Settings tab content.
func renderSettingsView(width, height int, s Styles, activeSection, providerSel, modelSel, modelColumn int, activeModel string, keys []config.ProviderKey, keySel int, inKeyInput bool, keyInputProvider, keyInputView string, showThinking bool, modelsLoading bool, modelsFor func(string) []ModelInfo, emptyHint func(string) []string) string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	innerWidth := width - 4
	if innerWidth < 0 {
		innerWidth = 0
	}

	var lines []string

	sep := dimStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	// --- Model section ---
	var modelTitleStyle lipgloss.Style
	if activeSection == 0 && !inKeyInput {
		modelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	} else {
		modelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorDim)
	}
	lines = append(lines, modelTitleStyle.Width(innerWidth).Render("Model"), sep)

	// Two-column layout: provider column on the left, model column on the
	// right. The cursor lives in column 0 or column 1 (modelColumn) and
	// navigation only moves within that column.
	const providerColWidth = 18
	modelColWidth := innerWidth - providerColWidth - 2
	if modelColWidth < 10 {
		modelColWidth = 10
	}

	// activeProviderName: the provider that owns the active model (when known).
	activeProviderName := ProviderOf(activeModel)
	sectionActive := activeSection == 0 && !inKeyInput

	// Build the provider column rows.
	var providerLines []string
	for i, p := range AvailableProviders {
		isCursor := sectionActive && modelColumn == 0 && i == providerSel
		isActiveProv := p.Name == activeProviderName
		prefix := "  "
		if isCursor {
			prefix = "▸ "
		}
		label := prefix + p.DisplayName
		if isActiveProv {
			label += " ★"
		}
		var rendered string
		switch {
		case isCursor:
			rendered = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(providerColWidth).Render(label)
		case isActiveProv:
			rendered = lipgloss.NewStyle().Foreground(colorSecondary).Width(providerColWidth).Render(label)
		default:
			rendered = dimStyle.Width(providerColWidth).Render(label)
		}
		providerLines = append(providerLines, rendered)
	}

	// Build the model column rows for the currently-selected provider.
	var providerForModels string
	if providerSel >= 0 && providerSel < len(AvailableProviders) {
		providerForModels = AvailableProviders[providerSel].Name
	}
	models := modelsFor(providerForModels)
	var modelLines []string
	customFooter := ""
	if modelsLoading && len(models) == 0 {
		// First fetch in flight (nothing cached yet) — show a waiting line.
		modelLines = append(modelLines, dimStyle.Italic(true).Width(modelColWidth).Render("  Loading available models…"))
	} else {
		for i, m := range models {
			isCursor := sectionActive && modelColumn == 1 && i == modelSel
			isActive := m.Spec == activeModel
			prefix := "  "
			if isCursor {
				prefix = "▸ "
			}
			label := prefix + m.DisplayName
			if isActive {
				label += " ✓"
			}
			var rendered string
			switch {
			case isCursor && isActive:
				rendered = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(modelColWidth).Render(label)
			case isCursor:
				rendered = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(modelColWidth).Render(label)
			case isActive:
				rendered = lipgloss.NewStyle().Foreground(colorSecondary).Width(modelColWidth).Render(label)
			default:
				rendered = dimStyle.Width(modelColWidth).Render(label)
			}
			modelLines = append(modelLines, rendered)
		}
		if len(models) == 0 {
			for _, h := range emptyHint(providerForModels) {
				modelLines = append(modelLines, dimStyle.Italic(true).Width(modelColWidth).Render("  "+h))
			}
		}
		// If the active model isn't in the catalogue for this provider, surface
		// it as a dim footer so users see what's really running.
		if activeModel != "" && activeProviderName == providerForModels {
			found := false
			for _, m := range models {
				if m.Spec == activeModel {
					found = true
					break
				}
			}
			if !found {
				customFooter = dimStyle.Italic(true).Width(modelColWidth).Render("  (custom: " + activeModel + ")")
			}
		}
	}

	// Pad the shorter column with blank rows so JoinHorizontal aligns
	// cleanly; otherwise lipgloss truncates the taller column.
	maxRows := len(providerLines)
	if len(modelLines) > maxRows {
		maxRows = len(modelLines)
	}
	for len(providerLines) < maxRows {
		providerLines = append(providerLines, dimStyle.Width(providerColWidth).Render(""))
	}
	for len(modelLines) < maxRows {
		modelLines = append(modelLines, dimStyle.Width(modelColWidth).Render(""))
	}

	providerCol := strings.Join(providerLines, "\n")
	modelCol := strings.Join(modelLines, "\n")
	if customFooter != "" {
		modelCol += "\n" + customFooter
	}
	gap := lipgloss.NewStyle().Width(2).Render("")
	lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, providerCol, gap, modelCol))

	if sectionActive {
		lines = append(lines, "", dimStyle.Italic(true).Width(innerWidth).Render("↑/↓ navigate  ←/→ switch column  Enter select  Tab → API Keys"))
	} else {
		lines = append(lines, "")
	}

	// --- API Keys section ---
	var keysTitleStyle lipgloss.Style
	if activeSection == 1 || inKeyInput {
		keysTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	} else {
		keysTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorDim)
	}
	lines = append(lines, keysTitleStyle.Width(innerWidth).Render("API Keys"), sep)

	if inKeyInput {
		sub := dimStyle.Width(innerWidth).Render("Provider: " + keyInputProvider)
		hint := dimStyle.Italic(true).Width(innerWidth).Render("Enter confirm  Esc cancel")
		lines = append(lines, sub, sep, keyInputView, "", hint)
	} else {
		for i, pk := range keys {
			var statusStr string
			if pk.Prefix != "" {
				statusStr = pk.Prefix + "..."
			} else {
				statusStr = "(not stored)"
			}
			label := pk.Provider + ": " + statusStr
			if i == keySel && activeSection == 1 {
				lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("▸ "+label))
			} else {
				lines = append(lines, dimStyle.Width(innerWidth).Render("  "+label))
			}
		}
		if activeSection == 1 {
			lines = append(lines, "", dimStyle.Italic(true).Width(innerWidth).Render("↑/↓ navigate  Enter add/update  Del delete  Tab → Display"))
		}
	}

	// --- Display section ---
	var displayTitleStyle lipgloss.Style
	if activeSection == 2 {
		displayTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	} else {
		displayTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorDim)
	}
	lines = append(lines, "", displayTitleStyle.Width(innerWidth).Render("Display"), sep)

	thinkingToggle := "[ ]"
	if showThinking {
		thinkingToggle = "[✓]"
	}
	thinkingLine := thinkingToggle + "  Show extended thinking"
	if activeSection == 2 {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("▸ "+thinkingLine))
		lines = append(lines, "", dimStyle.Italic(true).Width(innerWidth).Render("Enter toggle  Tab → Model"))
	} else {
		lines = append(lines, dimStyle.Width(innerWidth).Render("  "+thinkingLine))
	}

	content := strings.Join(lines, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}

// renderTabBar renders the two-tab bar: Sessions | Chat.
// alertBlink is true when some session needs user attention (shown on Chat tab label).
func renderTabBar(activeTab TabKind, width int, s Styles, viewportFocused bool, alertBlink bool) string {
	type tabDef struct {
		label string
		kind  TabKind
	}
	defs := []tabDef{
		{" Sessions ", TabKindSessions},
		{" Workspace ", TabKindChat},
		{" Settings ", TabKindSettings},
	}

	var sepStyle lipgloss.Style
	if viewportFocused {
		sepStyle = lipgloss.NewStyle().Foreground(s.ColorWhite)
	} else {
		sepStyle = lipgloss.NewStyle().Foreground(s.ColorBlurBorder)
	}

	var top, mid, bot strings.Builder
	top.WriteString(" ")
	mid.WriteString(" ")
	bot.WriteString(sepStyle.Render("╭"))
	visPos := 1

	for i, d := range defs {
		if i > 0 {
			top.WriteString(" ")
			mid.WriteString(" ")
			bot.WriteString(sepStyle.Render("─"))
			visPos++
		}
		lw := len(d.label)
		topLine := "╭" + strings.Repeat("─", lw) + "╮"
		var botLine string
		if d.kind == activeTab {
			botLine = "╯" + strings.Repeat(" ", lw) + "╰"
		} else {
			botLine = "┴" + strings.Repeat("─", lw) + "┴"
		}

		var textStyle lipgloss.Style
		switch {
		case d.kind == activeTab:
			textStyle = s.TabActiveStyle
		case alertBlink && d.kind == TabKindSessions:
			textStyle = s.TabAlertStyle
		default:
			textStyle = s.TabInactiveStyle
		}

		top.WriteString(sepStyle.Render(topLine))
		mid.WriteString(sepStyle.Render("│") + textStyle.Render(d.label) + sepStyle.Render("│"))
		bot.WriteString(sepStyle.Render(botLine))
		visPos += lw + 2
	}

	rem := width - visPos
	if rem < 0 {
		rem = 0
	}
	top.WriteString(strings.Repeat(" ", rem))
	mid.WriteString(strings.Repeat(" ", rem))
	if rem > 0 {
		bot.WriteString(sepStyle.Render(strings.Repeat("─", rem-1) + "╮"))
	} else {
		bot.WriteString(sepStyle.Render("╮"))
	}

	return top.String() + "\n" + mid.String() + "\n" + bot.String()
}
