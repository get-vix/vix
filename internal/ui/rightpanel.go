package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kirby88/vix/internal/config"
	"github.com/kirby88/vix/internal/protocol"
)

// rightPanelMode is the display mode of the right panel.
type rightPanelMode int

const (
	rpModeModel    rightPanelMode = iota // model selection list
	rpModeKeys                           // stored API key manager
	rpModeKeyInput                       // inline key entry form
	rpModeWorkflow                       // live workflow step progress
	rpModeTodos                          // pending todo list
)

// RightPanelAction is the action returned by HandleKey.
type RightPanelAction int

const (
	rpActionNone          RightPanelAction = iota
	rpActionClose                          // close the panel
	rpActionModelSelected                  // payload = model API name
	rpActionKeyDeleted                     // payload = provider name
	rpActionKeyStored                      // payload = "provider:key"
	rpActionNeedKey                        // payload = "provider:pendingModel"
)

// RightPanel is a full-height sidebar on the right side of the screen that
// contains either a model-selection list, an API key manager, or a key-input form.
type RightPanel struct {
	visible bool
	mode    rightPanelMode
	height  int

	// Model selection state
	modelSel      int
	models        []ModelInfo // the catalogue to choose from (dynamic when available)
	modelsLoading bool        // a live fetch is in flight

	// Key manager state
	keySel int
	keys   []config.ProviderKey

	// Key input state
	keyInputProvider string
	keyInputPending  string // model name waiting for the key
	keyInput         textinput.Model
}

// panelWidth is the fixed display width of the right panel.
const panelWidth = 42

// PanelWidth returns the fixed width of the right panel.
func (rp *RightPanel) PanelWidth() int { return panelWidth }

// IsVisible returns true when the panel is open.
func (rp *RightPanel) IsVisible() bool { return rp.visible }

// Close hides the panel.
func (rp *RightPanel) Close() { rp.visible = false }

// OpenModelSelect opens the model selection list, pre-selecting the active
// model. models is the catalogue to show (caller passes the dynamic list when
// available); loading reports whether a live refresh is in flight.
func (rp *RightPanel) OpenModelSelect(height int, activeModel string, models []ModelInfo, loading bool) {
	rp.visible = true
	rp.mode = rpModeModel
	rp.height = height
	rp.models = models
	rp.modelsLoading = loading
	// Pre-position cursor on the currently active model
	rp.modelSel = 0
	for i, m := range rp.models {
		if m.Spec == activeModel {
			rp.modelSel = i
			break
		}
	}
}

// SetModels replaces the selector's catalogue (e.g. when an async fetch
// completes) and clears the loading state, keeping the cursor on the active
// model when possible.
func (rp *RightPanel) SetModels(models []ModelInfo, activeModel string) {
	rp.models = models
	rp.modelsLoading = false
	rp.modelSel = 0
	for i, m := range models {
		if m.Spec == activeModel {
			rp.modelSel = i
			break
		}
	}
}

// IsModelMode reports whether the panel is currently showing the model selector.
func (rp *RightPanel) IsModelMode() bool {
	return rp.visible && rp.mode == rpModeModel
}

// OpenKeyManager opens the API key manager.
func (rp *RightPanel) OpenKeyManager(height int) {
	rp.visible = true
	rp.mode = rpModeKeys
	rp.keySel = 0
	rp.height = height
	rp.keys = config.ListStoredProviderKeys()
}

// OpenWorkflow opens the panel in workflow-progress mode.
func (rp *RightPanel) OpenWorkflow(height int) {
	rp.visible = true
	rp.mode = rpModeWorkflow
	rp.height = height
}

// OpenTodos opens the panel in todo-list mode.
func (rp *RightPanel) OpenTodos(height int) {
	rp.visible = true
	rp.mode = rpModeTodos
	rp.height = height
}

// OpenKeyInput opens the inline key-entry form for the given provider.
// pendingModel is the model the user wants to switch to once the key is saved.
func (rp *RightPanel) OpenKeyInput(provider, pendingModel string, height int) {
	rp.visible = true
	rp.mode = rpModeKeyInput
	rp.height = height
	rp.keyInputProvider = provider
	rp.keyInputPending = pendingModel

	ti := textinput.New()
	ti.Placeholder = "Paste your " + provider + " API key..."
	ti.EchoMode = textinput.EchoPassword
	ti.Focus()
	rp.keyInput = ti
}

// HandleKey processes a key press and returns the resulting action and its payload.
func (rp *RightPanel) HandleKey(msg tea.KeyPressMsg) (RightPanelAction, string) {
	key := msg.String()

	// Workflow and todos modes are read-only; ignore all keys.
	if rp.mode == rpModeWorkflow || rp.mode == rpModeTodos {
		return rpActionNone, ""
	}

	// ESC always closes
	if key == "esc" {
		return rpActionClose, ""
	}

	switch rp.mode {
	case rpModeModel:
		switch key {
		case "up", "k":
			if rp.modelSel > 0 {
				rp.modelSel--
			}
		case "down", "j":
			if rp.modelSel < len(rp.models)-1 {
				rp.modelSel++
			}
		case "enter":
			if rp.modelSel < len(rp.models) {
				m := rp.models[rp.modelSel]
				apiKey, _ := config.ResolveProviderKey(m.Provider, true)
				if apiKey != "" {
					return rpActionModelSelected, m.Spec
				}
				// No key stored — need to request one
				return rpActionNeedKey, m.Provider + ":" + m.Spec
			}
		}

	case rpModeKeys:
		switch key {
		case "up", "k":
			if rp.keySel > 0 {
				rp.keySel--
			}
		case "down", "j":
			if rp.keySel < len(rp.keys)-1 {
				rp.keySel++
			}
		case "enter":
			if rp.keySel < len(rp.keys) {
				provider := rp.keys[rp.keySel].Provider
				return rpActionNeedKey, provider + ":"
			}
		case "delete", "backspace":
			if rp.keySel < len(rp.keys) {
				return rpActionKeyDeleted, rp.keys[rp.keySel].Provider
			}
		}

	case rpModeKeyInput:
		if key == "enter" {
			val := strings.TrimSpace(rp.keyInput.Value())
			if val != "" {
				return rpActionKeyStored, rp.keyInputProvider + ":" + val
			}
			return rpActionNone, ""
		}
		// Forward key to textinput
		var cmd tea.Cmd
		rp.keyInput, cmd = rp.keyInput.Update(msg)
		_ = cmd
	}

	return rpActionNone, ""
}

// View renders the right panel as a bordered, full-height string.
// focused controls whether the panel border uses the focus color.
// activeModel is the currently active model API name (used to mark the selected model).
// wfp is the workflow graph panel (used when mode is rpModeWorkflow).
// todos is the current todo list (used in rpModeTodos and appended below steps in rpModeWorkflow).
func (rp *RightPanel) View(height int, s Styles, focused bool, activeModel string, wfp *WorkflowGraphPanel, todos []protocol.TodoItem) string {
	innerWidth := panelWidth - 4 // border (2) + padding (2)

	var lines []string

	switch rp.mode {
	case rpModeModel:
		title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("Select Model")
		sep := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth))
		lines = append(lines, title, sep)
		if rp.modelsLoading && len(rp.models) == 0 {
			lines = append(lines, lipgloss.NewStyle().Italic(true).Foreground(colorDim).Width(innerWidth).Render("Loading available models…"))
			break
		}
		if len(rp.models) == 0 {
			hintStyle := lipgloss.NewStyle().Italic(true).Foreground(colorDim).Width(innerWidth)
			lines = append(lines, hintStyle.Render("No models available."))
			lines = append(lines, hintStyle.Render("Set an API key or run 'vix login'."))
			break
		}
		for i, m := range rp.models {
			label := m.DisplayName
			if m.Provider == "openai" {
				label = "[OpenAI] " + m.DisplayName
			}
			isActive := m.Spec == activeModel
			isCursor := i == rp.modelSel
			switch {
			case isCursor && isActive:
				// Cursor is on the currently active model
				line := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("▸ " + label + " ✓")
				lines = append(lines, line)
			case isCursor:
				line := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("▸ " + label)
				lines = append(lines, line)
			case isActive:
				// Active model without cursor focus
				line := lipgloss.NewStyle().Foreground(colorSecondary).Width(innerWidth).Render("  " + label + " ✓")
				lines = append(lines, line)
			default:
				line := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render("  " + label)
				lines = append(lines, line)
			}
		}
		hint := lipgloss.NewStyle().Foreground(colorDim).Italic(true).Width(innerWidth).Render("↑/↓ navigate  Enter select  Esc close")
		lines = append(lines, "", hint)

	case rpModeKeys:
		title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("API Keys")
		sep := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth))
		lines = append(lines, title, sep)
		for i, pk := range rp.keys {
			var statusStr string
			if pk.Prefix != "" {
				statusStr = pk.Prefix + "..."
			} else {
				statusStr = "(not stored)"
			}
			label := pk.Provider + ": " + statusStr
			if i == rp.keySel {
				line := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("▸ " + label)
				lines = append(lines, line)
			} else {
				line := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render("  " + label)
				lines = append(lines, line)
			}
		}
		hint := lipgloss.NewStyle().Foreground(colorDim).Italic(true).Width(innerWidth).Render("↑/↓ navigate  Enter add/update  Del delete  Esc close")
		lines = append(lines, "", hint)

	case rpModeKeyInput:
		title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("Enter API Key")
		sub := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render("Provider: " + rp.keyInputProvider)
		sep := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth))
		rp.keyInput.SetWidth(innerWidth)
		inputView := rp.keyInput.View()
		hint := lipgloss.NewStyle().Foreground(colorDim).Italic(true).Width(innerWidth).Render("Enter confirm  Esc cancel")
		lines = append(lines, title, sub, sep, inputView, "", hint)

	case rpModeWorkflow:
		if wfp != nil {
			title := lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Width(innerWidth).Render("Workflow: " + wfp.workflowName)
			sep := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth))
			lines = append(lines, title, sep)
			for _, step := range wfp.steps {
				lines = append(lines, renderTodoOrStepLine(stepLabel(step), stepStatus(step), innerWidth))
			}
		}
		if hasPendingTodos(todos) {
			lines = append(lines, "", lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("Todos"))
			lines = append(lines, lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth)))
			for _, t := range todos {
				lines = append(lines, renderTodoOrStepLine(t.Content, string(t.Status), innerWidth))
			}
		}

	case rpModeTodos:
		title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("Todos")
		sep := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth))
		lines = append(lines, title, sep)
		for _, t := range todos {
			lines = append(lines, renderTodoOrStepLine(t.Content, string(t.Status), innerWidth))
		}
	}

	// Pad to fill height (subtract 2 for border top+bottom).
	// Each element in lines may contain embedded newlines from word-wrapping, so
	// we count actual terminal lines rather than slice elements.
	innerHeight := height - 2
	if innerHeight < 1 {
		innerHeight = 1
	}
	termLines := func(ss []string) int {
		n := 0
		for _, s := range ss {
			n += strings.Count(s, "\n") + 1
		}
		return n
	}
	for termLines(lines) < innerHeight {
		lines = append(lines, "")
	}
	// Trim from the end until we are within innerHeight terminal lines.
	for len(lines) > 0 && termLines(lines) > innerHeight {
		lines = lines[:len(lines)-1]
	}

	content := strings.Join(lines, "\n")
	panelStyle := s.RightPanelStyle
	if focused {
		panelStyle = panelStyle.BorderForeground(s.ColorWhite)
	}
	box := panelStyle.Width(panelWidth).Height(height).Render(content)
	return box
}

// stepStatus converts a workflowGraphStep into a string status token shared with renderTodoOrStepLine.
func stepStatus(step workflowGraphStep) string {
	switch {
	case step.active:
		return "in_progress"
	case step.success == nil:
		return "pending"
	case *step.success:
		return "completed"
	default:
		return "failed"
	}
}

// renderTodoOrStepLine renders a single labelled item with a status icon, wrapped to innerWidth.
// status values: "pending", "in_progress", "completed", "failed".
func renderTodoOrStepLine(label, status string, innerWidth int) string {
	var bullet, text string
	switch status {
	case "in_progress":
		bullet = lipgloss.NewStyle().Foreground(colorSecondary).Render("▶ ")
		text = lipgloss.NewStyle().Foreground(colorSecondary).Width(innerWidth - 2).Render(label)
	case "completed":
		bullet = lipgloss.NewStyle().Foreground(colorSuccess).Render("✓ ")
		text = lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth - 2).Render(label)
	case "failed":
		bullet = lipgloss.NewStyle().Foreground(colorError).Render("✗ ")
		text = lipgloss.NewStyle().Foreground(colorError).Width(innerWidth - 2).Render(label)
	default: // pending
		bullet = lipgloss.NewStyle().Foreground(colorDim).Render("○ ")
		text = lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth - 2).Render(label)
	}
	// Indent continuation lines to align under the text, not the bullet.
	textLines := strings.Split(text, "\n")
	result := bullet + textLines[0]
	for _, l := range textLines[1:] {
		result += "\n  " + l
	}
	return result
}
