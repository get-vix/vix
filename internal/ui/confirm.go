package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// maskSecret renders a secret for display with its first 6 runes in clear and
// the remainder replaced by bullets, so users can confirm they pasted the right
// key without exposing it. Secrets of 6 runes or fewer are shown verbatim.
func maskSecret(sval string) string {
	r := []rune(sval)
	if len(r) <= 6 {
		return sval
	}
	return string(r[:6]) + strings.Repeat("•", len(r)-6)
}

// keyInputDialog holds the data the credential-entry popup renders.
type keyInputDialog struct {
	Provider     string // human-readable provider label
	MethodLabel  string // credential method label ("API Key", "Token Plan")
	KeyMasked    string // masked key value (see maskSecret)
	NeedsBaseURL bool   // also show a base-URL field
	BaseURL      string // base-URL field value (shown verbatim)
	Focus        int    // 0 = key field, 1 = base-URL field
}

// renderKeyInputDialog renders the credential-entry popup as a centered overlay.
func renderKeyInputDialog(width, height int, s Styles, d keyInputDialog) string {
	dialogWidth := 56
	if dialogWidth > width-4 {
		dialogWidth = width - 4
	}
	innerWidth := dialogWidth - 4

	title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Width(innerWidth).Align(lipgloss.Center).
		Render("Set " + d.Provider + " " + d.MethodLabel)

	sep := s.CommandPaletteSepStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	boxed := func(label, value, placeholder string, active bool) string {
		field := value
		if field == "" {
			field = lipgloss.NewStyle().Foreground(s.ColorDimGray).Render(placeholder)
		}
		border := colorSecondary
		if !active {
			border = s.ColorDimGray
		}
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Width(innerWidth - 2).
			Render(field)
		if label == "" {
			return box
		}
		return lipgloss.NewStyle().Foreground(s.ColorDimGray).Render(label) + "\n" + box
	}

	var content string
	if d.NeedsBaseURL {
		keyBox := boxed("API key", d.KeyMasked, "Paste your key…", d.Focus == 0)
		urlBox := boxed("Base URL", d.BaseURL, "https://…/v1 (from your subscription page)", d.Focus == 1)
		hint := lipgloss.NewStyle().Foreground(s.ColorDimGray).
			Width(innerWidth).Align(lipgloss.Center).
			Render("Tab switch · Enter save · Esc cancel")
		content = title + "\n" + sep + "\n" + keyBox + "\n" + urlBox + "\n" + hint
	} else {
		box := boxed("", d.KeyMasked, "Paste your key…", true)
		hint := lipgloss.NewStyle().Foreground(s.ColorDimGray).
			Width(innerWidth).Align(lipgloss.Center).
			Render("Enter save · Esc cancel")
		content = title + "\n" + sep + "\n" + box + "\n" + hint
	}
	return s.CommandPaletteStyle.Width(dialogWidth).Render(content)
}

// renderKeyDeleteDialog renders the credential-deletion confirmation as a
// centered overlay. kind is "api_key" or "oauth"; selected: 0 = Yes, 1 = No.
func renderKeyDeleteDialog(width, height int, s Styles, provider, kind string, selected int) string {
	dialogWidth := 54
	if dialogWidth > width-4 {
		dialogWidth = width - 4
	}
	innerWidth := dialogWidth - 4

	what := "API key"
	if kind == "oauth" {
		what = "OAuth token"
	}

	title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Width(innerWidth).Align(lipgloss.Center).
		Render("Delete " + what + "?")

	sep := s.CommandPaletteSepStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	msg := lipgloss.NewStyle().Foreground(s.ColorDimGray).
		Width(innerWidth).Align(lipgloss.Center).
		Render("The stored " + provider + " " + what + " will be removed. This cannot be undone.")

	yesStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	noStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	if selected == 0 {
		yesStyle = yesStyle.Foreground(colorSecondary)
	} else {
		noStyle = noStyle.Foreground(colorSecondary)
	}

	buttons := lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).
		Render(yesStyle.Render("Yes") + "    " + noStyle.Render("No"))

	content := title + "\n" + sep + "\n" + msg + "\n\n" + buttons
	return s.CommandPaletteStyle.Width(dialogWidth).Render(content)
}

// renderTrimDialog renders the trim confirmation as a centered overlay box.
// selected: 0 = Yes, 1 = No.
func renderTrimDialog(width, height int, s Styles, selected int) string {
	dialogWidth := 50
	if dialogWidth > width-4 {
		dialogWidth = width - 4
	}
	innerWidth := dialogWidth - 4 // account for border + padding

	title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Width(innerWidth).Align(lipgloss.Center).
		Render("✂  Trim conversation?")

	sep := s.CommandPaletteSepStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	msg := lipgloss.NewStyle().Foreground(s.ColorDimGray).
		Width(innerWidth).Align(lipgloss.Center).
		Render("All messages below this point will be permanently deleted.")

	yesStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	noStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	if selected == 0 {
		yesStyle = yesStyle.Foreground(colorSecondary)
	} else {
		noStyle = noStyle.Foreground(colorSecondary)
	}

	yesBtn := yesStyle.Render("Yes")
	noBtn := noStyle.Render("No")
	buttons := lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).
		Render(yesBtn + "    " + noBtn)

	content := title + "\n" + sep + "\n" + msg + "\n\n" + buttons

	return s.CommandPaletteStyle.Width(dialogWidth).Render(content)
}

// renderAlertDialog renders a persistent, dismissible error popup as a centered
// overlay box, styled like the other dialogs. It stays until the user presses a
// key. width/height are the terminal dimensions; text is the message to show.
func renderAlertDialog(width, height int, s Styles, text string) string {
	dialogWidth := 60
	if dialogWidth > width-4 {
		dialogWidth = width - 4
	}
	if dialogWidth < 20 {
		dialogWidth = 20
	}
	innerWidth := dialogWidth - 4 // account for border + padding

	title := lipgloss.NewStyle().Bold(true).Foreground(colorError).
		Width(innerWidth).Align(lipgloss.Center).
		Render("Error")

	sep := s.CommandPaletteSepStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	msg := lipgloss.NewStyle().Foreground(s.ColorDimGray).
		Width(innerWidth).Align(lipgloss.Center).
		Render(text)

	hint := lipgloss.NewStyle().Foreground(s.ColorDimGray).
		Width(innerWidth).Align(lipgloss.Center).
		Render("press any key to dismiss")

	content := title + "\n" + sep + "\n" + msg + "\n\n" + hint

	return s.CommandPaletteStyle.Width(dialogWidth).Render(content)
}

// renderThreadCloseDialog renders the thread-close confirmation as a centered overlay box.
// selected: 0 = Yes, 1 = No.
func renderThreadCloseDialog(width, height int, s Styles, selected int, threadID string) string {
	dialogWidth := 52
	if dialogWidth > width-4 {
		dialogWidth = width - 4
	}
	innerWidth := dialogWidth - 4 // account for border + padding

	title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Width(innerWidth).Align(lipgloss.Center).
		Render("Close thread?")

	sep := s.CommandPaletteSepStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	body := "The thread will be terminated."
	if threadID != "" {
		body = body + "\n" + lipgloss.NewStyle().Foreground(s.ColorDimGray).Render(threadID)
	}
	msg := lipgloss.NewStyle().Foreground(s.ColorDimGray).
		Width(innerWidth).Align(lipgloss.Center).
		Render(body)

	yesStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	noStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	if selected == 0 {
		yesStyle = yesStyle.Foreground(colorSecondary)
	} else {
		noStyle = noStyle.Foreground(colorSecondary)
	}

	yesBtn := yesStyle.Render("Yes")
	noBtn := noStyle.Render("No")
	buttons := lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).
		Render(yesBtn + "    " + noBtn)

	content := title + "\n" + sep + "\n" + msg + "\n\n" + buttons

	return s.CommandPaletteStyle.Width(dialogWidth).Render(content)
}

// renderQuitDialog renders the quit confirmation as a centered overlay box,
// styled like the command palette. width/height are the terminal dimensions.
// selected: 0 = Yes, 1 = No. closeAll is the "close all threads" checkbox
// state (toggled with space, persisted as a preference).
func renderQuitDialog(width, height int, s Styles, selected int, closeAll bool) string {
	dialogWidth := 50
	if dialogWidth > width-4 {
		dialogWidth = width - 4
	}
	innerWidth := dialogWidth - 4 // account for border + padding

	title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Width(innerWidth).Align(lipgloss.Center).
		Render("Quit vix?")

	sep := s.CommandPaletteSepStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	body := "Threads will be restored on next launch."
	if closeAll {
		body = "All threads will be closed."
	}
	msg := lipgloss.NewStyle().Foreground(s.ColorDimGray).
		Width(innerWidth).Align(lipgloss.Center).
		Render(body)

	checkboxStyle := lipgloss.NewStyle().Foreground(s.ColorDimGray)
	box := "[ ]"
	if closeAll {
		box = "[x]"
		checkboxStyle = checkboxStyle.Foreground(colorSecondary)
	}
	checkbox := lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).
		Render(checkboxStyle.Render(box+" Close all threads") +
			lipgloss.NewStyle().Foreground(s.ColorDimGray).Render(" (space)"))

	yesStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	noStyle := lipgloss.NewStyle().Bold(true).Foreground(s.ColorDimGray)
	if selected == 0 {
		yesStyle = yesStyle.Foreground(colorSecondary)
	} else {
		noStyle = noStyle.Foreground(colorSecondary)
	}

	yesBtn := yesStyle.Render("Yes")
	noBtn := noStyle.Render("No")
	buttons := lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).
		Render(yesBtn + "    " + noBtn)

	content := title + "\n" + sep + "\n" + msg + "\n\n" + checkbox + "\n\n" + buttons

	return s.CommandPaletteStyle.Width(dialogWidth).Render(content)
}
