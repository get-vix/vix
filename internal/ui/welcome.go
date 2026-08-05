package ui

import (
	"charm.land/lipgloss/v2" // Version is the vix version string rendered on the welcome screen. Set by
	"image/color"
	"strings"

	"github.com/get-vix/vix/internal/protocol"
)

// cmd/vix/main.go at startup from the ldflags-provided build version.
var Version = "dev" // renderVixBanner returns the VIX ASCII art with a vertical gradient
// from purple (top) to yellow (bottom).
func renderVixBanner() string {
	lines := []string{"██╗   ██╗ ██╗ ██╗  ██╗", "██║   ██║ ██║ ╚██╗██╔╝", "██║   ██║ ██║  ╚███╔╝ ", "╚██╗ ██╔╝ ██║  ██╔██╗ ", " ╚████╔╝  ██║ ██╔╝ ██╗", "  ╚═══╝   ╚═╝ ╚═╝  ╚═╝"} // Hand-picked vertical ramp: purple → pink → orange → gold → yellow → green.
	ramp := []color.Color{lipgloss.Color("#BC63FC"),
		// purple
		lipgloss.Color("#F04EAA"),
		// pink
		lipgloss.Color("#FF7A55"),
		// orange
		lipgloss.Color("#FFAB22"),
		// gold
		lipgloss.Color("#FFD700"),
		// yellow
		lipgloss.Color("#39E831"),
		// green
	}
	var result strings.Builder
	for i, line := range lines {
		style := lipgloss.NewStyle().Foreground(ramp[i])
		result.WriteString(style.Render(line))
		result.WriteRune('\n')
	}
	return result.String()
} // renderWelcomeInline renders a centered welcome message for inline mode.
// workDir is the thread's working directory; when draft is true the thread
// has not started yet, so the directory is shown as editable (Ctrl+o) and the
// recent-directories list is offered for quick switching. recentDirs holds the
// most-used working directories (already ranked, trimmed to at most 5 by the
// caller); recentSel is the highlighted index when the welcome area is focused
// (-1 = none highlighted). Layout order: working directory, then recent
// directories, then shortcuts.
func renderWelcomeInline(width, height int, s Styles, workDir string, draft bool, recentDirs []protocol.DirUsage, recentSel int) string {
	// Build the welcome block (uncentered)
	var block strings.Builder
	block.WriteString(renderVixBanner())
	version := lipgloss.NewStyle().Foreground(s.ColorDimGray).Render(Version)
	block.WriteString(version + "\n\n")
	subtitle := lipgloss.NewStyle().Foreground(s.ColorWhite).Italic(true).Render("AI coding assistant")
	block.WriteString(subtitle + "\n\n")

	shortcutStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(s.ColorWhite)
	// Only the working-directory picker shortcut is advertised on the welcome
	// screen; the rest of the keymap lives in the docs.
	shortcuts := []struct {
		key  string
		desc string
	}{{"Ctrl+o", "Change working directory"}}

	if !draft {
		recentDirs = nil
	}

	// The working directory is rendered as a single two-column row, styled and
	// aligned exactly like the shortcut rows: a right-aligned label in the left
	// column and a left-aligned value in the right column. The label reuses the
	// status-bar shortcut badge (highlighted secondary color); the value is the
	// path in the secondary color.
	var wdBadge, wdValue string
	if workDir != "" {
		badgeStyle := lipgloss.NewStyle().Background(colorSecondary).Foreground(lipgloss.Color("0")).Bold(true)
		wdBadge = badgeStyle.Render(" Working directory ")
		wdValue = lipgloss.NewStyle().Foreground(colorSecondary).Render(workDir)
	}

	// Find the longest key and longest desc to build fixed-width rows. The
	// working-directory row participates in the same column widths so its
	// divider lines up with the shortcuts below it.
	maxKeyWidth := 0
	maxDescWidth := 0
	for _, sc := range shortcuts {
		if len(sc.key) > maxKeyWidth {
			maxKeyWidth = len(sc.key)
		}
		if len(sc.desc) > maxDescWidth {
			maxDescWidth = len(sc.desc)
		}
	}
	leftColWidth := maxKeyWidth
	rightColWidth := maxDescWidth
	if workDir != "" {
		leftColWidth = max(leftColWidth, lipgloss.Width(wdBadge))
		rightColWidth = max(rightColWidth, lipgloss.Width(wdValue))
	}
	recentBadge := ""
	if len(recentDirs) > 0 {
		recentBadge = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("Recent")
		leftColWidth = max(leftColWidth, lipgloss.Width(recentBadge))
		for _, d := range recentDirs {
			rightColWidth = max(rightColWidth, lipgloss.Width(d.Path))
		}
	}
	rowWidth := leftColWidth + 2 + rightColWidth // left + gap + right

	// Working directory row first.
	if workDir != "" {
		label := lipgloss.NewStyle().Width(leftColWidth).AlignHorizontal(lipgloss.Right).Render(wdBadge)
		row := lipgloss.NewStyle().Width(rowWidth).Render(label + "  " + wdValue)
		block.WriteString(row + "\n")
	}

	// Recent directories (draft only), between the working directory and the
	// shortcuts: a "Recent" badge on the first row, then one row per directory.
	// The highlighted row (recentSel) is shown inverted so up/down selection is
	// visible when the welcome area is focused.
	if len(recentDirs) > 0 {
		if workDir != "" {
			block.WriteString("\n")
		}
		for i, d := range recentDirs {
			label := ""
			if i == 0 {
				label = recentBadge
			}
			labelCol := lipgloss.NewStyle().Width(leftColWidth).AlignHorizontal(lipgloss.Right).Render(label)
			pathStyle := lipgloss.NewStyle().Foreground(colorPrimary)
			if i == recentSel {
				pathStyle = pathStyle.Background(colorPrimary).Foreground(lipgloss.Color("0")).Bold(true)
			}
			value := pathStyle.Width(rightColWidth).Render(d.Path)
			row := lipgloss.NewStyle().Width(rowWidth).Render(labelCol + "  " + value)
			block.WriteString(row + "\n")
		}
	}

	// Shortcuts last, separated by a blank line.
	if workDir != "" || len(recentDirs) > 0 {
		block.WriteString("\n")
	}
	for _, sc := range shortcuts {
		key := shortcutStyle.Width(leftColWidth).AlignHorizontal(lipgloss.Right).Render(sc.key)
		desc := descStyle.Width(rightColWidth).Render(sc.desc)
		row := lipgloss.NewStyle().Width(rowWidth).Render(key + "  " + desc)
		block.WriteString(row + "\n")
	}
	// Center horizontally and vertically
	centered := lipgloss.NewStyle().Width(width).Height(height).AlignHorizontal(lipgloss.Center).AlignVertical(lipgloss.Center).Render(block.String())
	return centered
}

// renderRestoringInline renders a centered loading placeholder for a thread
// that was attached on launch and is still waiting for its event.replay. It
// mirrors the welcome screen (vix banner + subtitle) but without version or
// shortcuts. spinner is the current animation frame (from ThinkingAnim.View);
// it may be empty, in which case only the banner and subtitle are shown.
func renderRestoringInline(width, height int, s Styles, spinner string) string {
	var block strings.Builder
	block.WriteString(renderVixBanner())
	block.WriteString("\n")
	subtitle := lipgloss.NewStyle().Foreground(s.ColorWhite).Italic(true).Render("loading threads")
	block.WriteString(subtitle + "\n\n")
	if spinner != "" {
		block.WriteString(strings.TrimLeft(spinner, " "))
	}
	centered := lipgloss.NewStyle().Width(width).Height(height).AlignHorizontal(lipgloss.Center).AlignVertical(lipgloss.Center).Render(block.String())
	return centered
}
