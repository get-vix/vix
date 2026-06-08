package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
)

// titleCaseLabel capitalizes the first letter of each space-separated word in a
// status-bar shortcut label, leaving the rest of each word untouched (so "new"
// becomes "New" and "Switch focus" becomes "Switch Focus").
func titleCaseLabel(label string) string {
	words := strings.Fields(label)
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// renderStatusBar renders the two-line status bar.
//
// Line 1 — always visible: shortcut hints on the left, connection status on the right.
// Line 2 — transient: a message (warning / info / error) shown for 3 s then cleared;
// rendered as a blank line when there is no active message so the layout stays stable.
func renderStatusBar(
	width int,
	connected bool,
	reconnecting bool,
	msg StatusMessage,
	s Styles,
	activeTab TabKind,
	focus FocusState,
	lastInputTokens int64,
	contextWindow int64,
) string {
	// ── Line 1: shortcuts + connection status ───────────────────────────────
	badgeStyle := lipgloss.NewStyle().Background(colorSecondary).Foreground(lipgloss.Color("0")).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	type shortcut struct{ key, label string }
	var defs []shortcut
	switch activeTab {
	case TabKindSessions:
		defs = []shortcut{
			{"t", "new session"},
			{"d", "duplicate session"},
			{"x", "delete session"},
			{"↑↓", "navigate sessions"},
			{"enter", "open in workspace"},
		}
	case TabKindModels:
		defs = []shortcut{
			{"↑↓", "navigate"},
			{"Tab", "Switch focus"},
			{"Enter", "Select"},
		}
	case TabKindSettings:
		defs = []shortcut{
			{"↑↓", "navigate"},
			{"Enter", "Toggle"},
			{"←→", "Adjust"},
		}
	default: // TabKindChat (Workspace)
		switch focus {
		case FocusChat:
			defs = []shortcut{
				{"g", "top"},
				{"Shift+G", "bottom"},
				{"Tab", "Switch focus"},
				{"Ctrl+T", "New session"},
				{"Ctrl+P", "Previous session"},
				{"Ctrl+N", "Next session"},
			}
		case FocusRightPanel:
			defs = []shortcut{
				{"Tab", "Switch focus"},
				{"Ctrl+T", "New session"},
				{"Ctrl+P", "Previous session"},
				{"Ctrl+N", "Next session"},
			}
		default: // FocusEditor
			defs = []shortcut{
				{"Tab", "Switch focus"},
				{"Shift+Tab", "Workflows"},
				{"Ctrl+T", "New session"},
				{"Ctrl+P", "Previous session"},
				{"Ctrl+N", "Next session"},
			}
		}
	}

	var b strings.Builder
	for _, d := range defs {
		b.WriteString(badgeStyle.Render(" " + d.key + " "))
		b.WriteString(labelStyle.Render(" " + titleCaseLabel(d.label) + " "))
	}
	shortcuts := b.String()

	var connStatus string
	if connected {
		connStatus = statusConnectedStyle.Render("● Connected")
	} else if reconnecting {
		connStatus = statusReconnectingStyle.Render("● Reconnecting")
	} else {
		connStatus = statusDisconnectedStyle.Render("● Disconnected")
	}

	shortcutsLen := lipgloss.Width(shortcuts)
	connLen := lipgloss.Width(connStatus)

	// Context-fill indicator — only on the Workspace (chat) tab and only when
	// the model's window is known. Rendered between the shortcuts and the
	// connection status, keeping connStatus right-aligned.
	var indicator string
	if activeTab == TabKindChat {
		indicator = contextIndicator(lastInputTokens, contextWindow)
	}
	indicatorLen := lipgloss.Width(indicator)

	if indicator != "" {
		indicator = indicator + "   "
		indicatorLen += 3
	}

	totalContent := shortcutsLen + connLen + indicatorLen
	remaining := width - totalContent - 2
	if remaining < 2 {
		remaining = 2
	}
	leftPad := remaining / 2
	rightPad := remaining - leftPad
	line1 := strings.Repeat(" ", leftPad) + shortcuts + strings.Repeat(" ", rightPad) + indicator + connStatus

	// ── Line 2: transient message ───────────────────────────────────────────
	var line2 string
	if msg.Text != "" {
		var msgStyle lipgloss.Style
		var prefix string
		switch msg.Kind {
		case StatusMsgWarning:
			msgStyle = lipgloss.NewStyle().Foreground(colorWarning).Italic(true)
			prefix = " ⚠ "
		case StatusMsgError:
			msgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
			prefix = " ✖ "
		default: // StatusMsgInfo
			msgStyle = lipgloss.NewStyle().Foreground(s.ColorDimGray).Italic(true)
			prefix = " ℹ "
		}
		line2 = msgStyle.Render(prefix + msg.Text)
	}
	// Always pad line 2 to full width so the layout never shifts.
	line2 = lipgloss.NewStyle().Width(width).Render(line2)

	return line2 + "\n" + s.StatusBarStyle.Width(width).Render(line1)
}

// contextIndicator renders the "context filled" status (e.g. "◔ 128k/200k · 64%")
// colored green→amber→red as it nears the window. Returns "" when the window is
// unknown (0) so callers omit it entirely.
func contextIndicator(used, window int64) string {
	if window <= 0 {
		return ""
	}
	pct := float64(used) / float64(window)
	color := colorSuccess
	switch {
	case pct >= 0.90:
		color = lipgloss.Color("9") // red
	case pct >= 0.75:
		color = colorWarning // amber
	}
	txt := fmt.Sprintf("◔ %s/%s · %d%%", formatTokenCount(used), formatTokenCount(window), int(pct*100))
	return lipgloss.NewStyle().Foreground(color).Render(txt)
}

func formatTokenCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%dk", n/1000)
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
