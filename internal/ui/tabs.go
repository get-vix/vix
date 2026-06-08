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
	TabKindModels                  // model + authentication management
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
func renderSessionsView(sessions []*SessionState, width, height int, s Styles, selectedRow int) string {
	const colSession = 10
	const colRunning = 10

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

	header := fmt.Sprintf("  %-*s  %-*s  %-*s%-*s", colSession, "Session", colMessage, "First message", colRunning, "Running", badgeVisible, "")
	rows := []string{s.TabActiveStyle.Render(header)}

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

	content := strings.Join(rows, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}

// truncateLabel shortens s to fit within w display columns, appending an
// ellipsis when truncation occurs. Rune-aware so multi-byte names don't split.
func truncateLabel(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// settingsItem identifies a selectable row in the Settings tab. The order here
// is the render order and the cursor index space (0..settingsItemCount-1).
type settingsItem int

const (
	settingShowThinking settingsItem = iota
	settingReadAgentsMD
	settingReadClaudeMD
	settingToolOrchestrator
	settingTelemetry
	settingCompactionAuto
	settingCompactionThreshold
	settingsItemCount
)

// settingsState carries the current values shown in the Settings tab plus the
// cursor position. Values are read from ~/.vix/settings.json at render time.
type settingsState struct {
	cursor              int
	showThinking        bool
	readAgentsMD        bool
	readClaudeMD        bool
	toolOrchestrator    bool
	telemetry           bool
	compactionAuto      bool
	compactionThreshold float64
}

// toggleSetting flips (or, for the threshold row, leaves unchanged) the setting
// at the given row and persists it to ~/.vix/settings.json.
func (m *Model) toggleSetting(item settingsItem) {
	switch item {
	case settingShowThinking:
		v := !config.ShowThinking()
		if sess := m.currentSession(); sess != nil {
			sess.showThinking = !sess.showThinking
			v = sess.showThinking
			if sess.showThinking && sess.thinkingBuf != "" {
				sess.thinkingRendered = renderThinkingText(sess.thinkingBuf, m.styles, m.mdRenderer.width+4)
			} else {
				sess.thinkingRendered = ""
			}
		}
		_ = config.SetShowThinking(v)
	case settingReadAgentsMD:
		_ = config.SetReadAgentsMD(!config.ReadAgentsMD())
	case settingReadClaudeMD:
		_ = config.SetReadClaudeMD(!config.ReadClaudeMD())
	case settingToolOrchestrator:
		_ = config.SetToolOrchestrator(!config.ToolOrchestrator())
	case settingTelemetry:
		_ = config.SetTelemetryEnabled(!config.TelemetryEnabled())
	case settingCompactionAuto:
		_ = config.SetCompactionAuto(!config.CompactionAuto())
	case settingCompactionThreshold:
		// Threshold is adjusted with ←/→, not toggled.
	}
}

// adjustCompactionThreshold nudges the auto-compaction threshold by delta,
// clamped to [0.1, 1.0] and rounded to the nearest 0.05.
func (m *Model) adjustCompactionThreshold(delta float64) {
	v := config.CompactionThreshold() + delta
	if v < 0.1 {
		v = 0.1
	}
	if v > 1.0 {
		v = 1.0
	}
	v = float64(int(v*20+0.5)) / 20 // round to nearest 0.05
	_ = config.SetCompactionThreshold(v)
}

// renderSettingsView renders the Settings tab content (global preferences).
func renderSettingsView(width, height int, s Styles, st settingsState) string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	innerWidth := width - 4
	if innerWidth < 0 {
		innerWidth = 0
	}

	sep := dimStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	var lines []string
	idx := 0 // running index of selectable settings, matches settingsItem

	row := func(text string) {
		if idx == st.cursor {
			lines = append(lines, titleStyle.Width(innerWidth).Render("▸ "+text))
		} else {
			lines = append(lines, dimStyle.Width(innerWidth).Render("  "+text))
		}
		idx++
	}

	toggleRow := func(label string, on bool) {
		box := "[ ]"
		if on {
			box = "[✓]"
		}
		row(box + "  " + label)
	}

	sliderRow := func(label string, val float64) {
		const barWidth = 20
		filled := int(val*float64(barWidth) + 0.5)
		if filled < 0 {
			filled = 0
		}
		if filled > barWidth {
			filled = barWidth
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		pct := int(val*100 + 0.5)
		row(fmt.Sprintf("%s  %s %3d%%", label, bar, pct))
	}

	section := func(name string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, titleStyle.Width(innerWidth).Render(name), sep)
	}

	section("Display")
	toggleRow("Show extended thinking", st.showThinking)

	section("Context")
	toggleRow("Read AGENTS.md", st.readAgentsMD)
	toggleRow("Read CLAUDE.md", st.readClaudeMD)

	section("Agent")
	toggleRow("Tool orchestrator", st.toolOrchestrator)

	section("Privacy")
	toggleRow("Send anonymous telemetry", st.telemetry)

	section("Compaction")
	toggleRow("Auto-compaction", st.compactionAuto)
	sliderRow("Threshold       ", st.compactionThreshold)

	lines = append(lines, "", dimStyle.Italic(true).Width(innerWidth).Render("↑↓ navigate · Enter toggle · ←→ adjust threshold"))

	content := strings.Join(lines, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}

// authButton is one actionable control in the Models-tab authentication panel.
// id drives the handler; label is what the user sees.
type authButton struct {
	id    string
	label string
}

// authRow indices for the Models-tab authentication panel.
const (
	authRowAPIKey = 0
	authRowOAuth  = 1
)

// authButtonsFor returns the ordered buttons shown for a given authentication
// row, given the provider's stored-credential status. This is the single source
// of truth shared by the renderer and the key handler so navigation indices and
// drawn controls never diverge. Delete buttons appear only when that credential
// is actually stored; "Make it default" only when the method isn't already the
// default and is usable. The OAuth row has no buttons for providers without an
// OAuth login.
func authButtonsFor(st config.ProviderAuthStatus, row int) []authButton {
	var btns []authButton
	switch row {
	case authRowAPIKey:
		if st.APIKeyStored {
			btns = append(btns, authButton{"set_key", "Update key"})
			btns = append(btns, authButton{"del_key", "Delete key"})
			if st.Default != config.AuthDefaultAPIKey {
				btns = append(btns, authButton{"default_key", "Make it default"})
			}
		} else {
			btns = append(btns, authButton{"set_key", "Create key"})
		}
	case authRowOAuth:
		if !st.OAuthSupported {
			return nil
		}
		if st.OAuthStored {
			btns = append(btns, authButton{"set_token", "Update token"})
			btns = append(btns, authButton{"del_token", "Delete token"})
			if st.Default != config.AuthDefaultOAuth {
				btns = append(btns, authButton{"default_token", "Make it default"})
			}
		} else {
			btns = append(btns, authButton{"set_token", "Create token"})
		}
	}
	return btns
}

// modelsProviderColWidth is the fixed width of the Models-tab provider column.
const modelsProviderColWidth = 20

// renderModelGrid lays out a slice of models as a row-major grid of
// modelGridCols columns and returns the rendered rows (without a header). The
// cursor is shown when focused; the active model is marked with ✓. modelSel is
// the cursor index relative to the given slice (-1 when the cursor is outside
// the slice, e.g. scrolled out of view).
func renderModelGrid(models []ModelInfo, colWidth int, focused bool, modelSel int, activeModel string) []string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	const cellGutter = 1
	cellWidth := (colWidth - cellGutter*(modelGridCols-1)) / modelGridCols
	if cellWidth < 8 {
		cellWidth = 8
	}

	rowCount := (len(models) + modelGridCols - 1) / modelGridCols
	cellGap := lipgloss.NewStyle().Width(cellGutter).Render("")
	var gridLines []string
	for r := 0; r < rowCount; r++ {
		var cells []string
		for c := 0; c < modelGridCols; c++ {
			if c > 0 {
				cells = append(cells, cellGap)
			}
			idx := r*modelGridCols + c
			if idx >= len(models) {
				cells = append(cells, dimStyle.Width(cellWidth).Render(""))
				continue
			}
			m := models[idx]
			isCursor := focused && idx == modelSel
			isActive := m.Spec == activeModel
			prefix := "  "
			if isCursor {
				prefix = "▸ "
			}
			label := prefix + m.DisplayName
			if isActive {
				label += " ✓"
			}
			label = truncateLabel(label, cellWidth)
			var rendered string
			switch {
			case isCursor:
				rendered = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(cellWidth).Render(label)
			case isActive:
				rendered = lipgloss.NewStyle().Foreground(colorSecondary).Width(cellWidth).Render(label)
			default:
				rendered = dimStyle.Width(cellWidth).Render(label)
			}
			cells = append(cells, rendered)
		}
		gridLines = append(gridLines, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	return gridLines
}

// modelsViewportChrome is the vertical space the Models-tab viewport border
// consumes: ViewportFocusedStyle draws only a bottom border (BorderTop is off)
// and no vertical padding.
const modelsViewportChrome = 1

// modelsHeaderLines returns the number of terminal lines the Models-tab right
// column renders before the model grid, for the given auth + login state. The
// renderer and the key handler both call it so the grid window and the scroll
// clamp agree on how many rows fit.
func modelsHeaderLines(st config.ProviderAuthStatus, loginStatus string) int {
	n := 2 // "Credentials" title + separator
	n += 2 // API Key row + its buttons row
	if st.OAuthSupported {
		n += 2 // OAuth token row + its buttons row
	} else {
		n++ // "OAuth token: (not available)"
	}
	if loginStatus != "" {
		n++
	}
	// Models section header: blank, "Models:" title (with count), separator,
	// filter line, two help lines, blank.
	n += 7
	return n
}

// modelsGridRows returns how many grid rows fit in a Models-tab viewport of the
// given height for the given auth/login state. Always >= 1.
func modelsGridRows(height int, st config.ProviderAuthStatus, loginStatus string) int {
	rows := height - modelsViewportChrome - modelsHeaderLines(st, loginStatus)
	if rows < 1 {
		rows = 1
	}
	return rows
}

// renderModelsView renders the Models tab: a provider column (split into logged
// in / available) on the left, and an authentication panel + model grid for the
// selected provider on the right.
func renderModelsView(width, height int, s Styles,
	loggedIn, available []string,
	status map[string]config.ProviderAuthStatus,
	providerSel int, focus modelsFocusArea,
	authRow, authBtn, modelSel, modelScroll int,
	modelFilter, activeModel, loginStatus string) string {

	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	secondaryStyle := lipgloss.NewStyle().Foreground(colorSecondary)
	innerWidth := width - 4
	if innerWidth < 0 {
		innerWidth = 0
	}

	colWidth := modelsProviderColWidth
	if colWidth > innerWidth-12 {
		colWidth = innerWidth - 12
	}
	if colWidth < 8 {
		colWidth = 8
	}
	rightWidth := innerWidth - colWidth - 2
	if rightWidth < 10 {
		rightWidth = 10
	}

	flat := append(append([]string{}, loggedIn...), available...)
	provider := ""
	if providerSel >= 0 && providerSel < len(flat) {
		provider = flat[providerSel]
	}
	activeProvider := ProviderOf(activeModel)

	// ---- left: provider column ----
	var leftLines []string
	leftLines = append(leftLines,
		lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(colWidth).Render("Providers"),
		dimStyle.Width(colWidth).Render(strings.Repeat("─", colWidth)),
	)
	flatIdx := 0
	renderGroup := func(header string, names []string) {
		leftLines = append(leftLines, "", dimStyle.Bold(true).Underline(true).Width(colWidth).Render(header))
		if len(names) == 0 {
			leftLines = append(leftLines, dimStyle.Italic(true).Width(colWidth).Render("  —"))
			return
		}
		for _, name := range names {
			isSelected := flatIdx == providerSel
			isCursor := focus == modelsFocusProviders && isSelected
			prefix := "  "
			if isSelected {
				prefix = "▸ "
			}
			label := prefix + DisplayNameForProvider(name)
			if name == activeProvider {
				label += " ★"
			}
			switch {
			case isCursor:
				leftLines = append(leftLines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(colWidth).Render(label))
			case isSelected:
				leftLines = append(leftLines, secondaryStyle.Width(colWidth).Render(label))
			default:
				leftLines = append(leftLines, dimStyle.Width(colWidth).Render(label))
			}
			flatIdx++
		}
	}
	renderGroup("Logged in:", loggedIn)
	renderGroup("Available:", available)

	// ---- right: authentication + models ----
	st := status[provider]
	authActive := focus == modelsFocusAuth
	sep := dimStyle.Width(rightWidth).Render(strings.Repeat("─", rightWidth))

	authTitle := lipgloss.NewStyle().Bold(true)
	if authActive {
		authTitle = authTitle.Foreground(colorPrimary)
	} else {
		authTitle = authTitle.Foreground(colorDim)
	}

	var rightLines []string
	rightLines = append(rightLines, authTitle.Render("Credentials"), sep)

	defaultTag := func(isDefault bool) string {
		if isDefault {
			return "   " + secondaryStyle.Render("Default method")
		}
		return ""
	}
	renderButtons := func(row int) string {
		btns := authButtonsFor(st, row)
		if len(btns) == 0 {
			return ""
		}
		var cells []string
		for i, b := range btns {
			text := "[ " + b.label + " ]"
			if authActive && authRow == row && authBtn == i {
				cells = append(cells, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(text))
			} else {
				cells = append(cells, dimStyle.Render(text))
			}
		}
		return "    " + strings.Join(cells, "  ")
	}

	// API key row — hidden when the provider has no API key auth method
	// (e.g. OAuth-only providers like GitHub Copilot).
	if provider == "" || config.ProviderHasAPIKeyAuth(provider) {
		keyVal := "(empty)"
		if st.APIKeyStored {
			keyVal = st.APIKeyPrefix + "..."
		}
		rightLines = append(rightLines, "API Key: "+keyVal+defaultTag(st.Default == config.AuthDefaultAPIKey))
		rightLines = append(rightLines, renderButtons(authRowAPIKey))
	}

	// OAuth row.
	if st.OAuthSupported {
		tokVal := "(empty)"
		if st.OAuthStored {
			tokVal = "active"
		}
		rightLines = append(rightLines, "OAuth token: "+tokVal+defaultTag(st.Default == config.AuthDefaultOAuth))
		rightLines = append(rightLines, renderButtons(authRowOAuth))
	} else {
		rightLines = append(rightLines, dimStyle.Render("OAuth token: "+keyValNotAvailable))
	}

	if loginStatus != "" {
		rightLines = append(rightLines, secondaryStyle.Render(loginStatus))
	}

	// Models section.
	modelsTitle := lipgloss.NewStyle().Bold(true)
	if focus == modelsFocusModels {
		modelsTitle = modelsTitle.Foreground(colorPrimary)
	} else {
		modelsTitle = modelsTitle.Foreground(colorDim)
	}

	allModels := DisplayModelsForProvider(provider)
	filtered := FilterModels(allModels, modelFilter)

	// Window the filtered list to the rows that fit, keeping the cursor visible.
	gridRows := modelsGridRows(height, st, loginStatus)
	maxVisible := gridRows * modelGridCols
	totalRows := (len(filtered) + modelGridCols - 1) / modelGridCols
	maxScrollRow := totalRows - gridRows
	if maxScrollRow < 0 {
		maxScrollRow = 0
	}
	scrollRow := modelScroll
	if scrollRow > maxScrollRow {
		scrollRow = maxScrollRow
	}
	if scrollRow < 0 {
		scrollRow = 0
	}
	startIdx := scrollRow * modelGridCols
	if startIdx > len(filtered) {
		startIdx = len(filtered)
	}
	endIdx := startIdx + maxVisible
	if endIdx > len(filtered) {
		endIdx = len(filtered)
	}
	window := filtered[startIdx:endIdx]
	shown := len(window)

	titleLine := modelsTitle.Render("Models:") + "   " +
		dimStyle.Render(fmt.Sprintf("showing %d of %d", shown, len(filtered)))
	rightLines = append(rightLines, "", titleLine, sep)

	// Filter line — type-to-filter while the grid is focused.
	caret := ""
	if focus == modelsFocusModels {
		caret = "▌"
	}
	var filterLine string
	if modelFilter == "" && focus != modelsFocusModels {
		filterLine = dimStyle.Render("Filter: (type while focused to filter)")
	} else {
		filterLine = "Filter: " + secondaryStyle.Render(modelFilter) + caret
	}
	rightLines = append(rightLines,
		filterLine,
		dimStyle.Render("Selecting a model updates the default model for chat."),
		dimStyle.Render("For workflows see https://getvix.dev/doc#workflows"),
		"",
	)

	selInWindow := modelSel - startIdx
	if selInWindow < 0 || selInWindow >= shown {
		selInWindow = -1
	}
	grid := renderModelGrid(window, rightWidth, focus == modelsFocusModels, selInWindow, activeModel)
	rightLines = append(rightLines, grid...)

	// Footer for an active model that isn't in the provider's catalogue at all
	// (e.g. a custom OpenRouter route set via agent frontmatter).
	if activeModel != "" && ProviderOf(activeModel) == provider {
		found := false
		for _, mm := range allModels {
			if mm.Spec == activeModel {
				found = true
				break
			}
		}
		if !found {
			rightLines = append(rightLines, dimStyle.Italic(true).Width(rightWidth).Render("  (custom: "+activeModel+")"))
		}
	}

	leftCol := lipgloss.NewStyle().Width(colWidth).Render(strings.Join(leftLines, "\n"))
	rightCol := lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(rightLines, "\n"))
	gap := lipgloss.NewStyle().Width(2).Render("")
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, gap, rightCol)

	return s.ViewportFocusedStyle.Width(width).Height(height).Render(body)
}

// keyValNotAvailable is the marker shown for an auth method a provider doesn't
// offer (e.g. OAuth for MiniMax / Xiaomi MiMo).
const keyValNotAvailable = "(not available)"

// renderTabBar renders the two-tab bar: Sessions | Chat.
// alertActive is true when some session is waiting for user input; the Sessions
// tab title then blinks (alertBlinkOn is the current blink phase). When no alert
// is active but unseen is true (a message arrived while the Sessions tab was not
// focused), the Sessions title is tinted secondary statically (no blink).
func renderTabBar(activeTab TabKind, width int, s Styles, viewportFocused bool, alertActive bool, alertBlinkOn bool, unseen bool) string {
	type tabDef struct {
		label string
		kind  TabKind
	}
	defs := []tabDef{
		{" Sessions [F1] ", TabKindSessions},
		{" Workspace [F2] ", TabKindChat},
		{" Models [F3] ", TabKindModels},
		{" Settings [F4] ", TabKindSettings},
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
		case d.kind == TabKindSessions && alertActive:
			// Waiting for input: blink between the alert color and inactive.
			if alertBlinkOn {
				textStyle = s.TabAlertStyle
			} else {
				textStyle = s.TabInactiveStyle
			}
		case d.kind == TabKindSessions && unseen:
			// Unseen activity: static secondary tint (superseded by the blink above).
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
