package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/protocol"
)

// capitalizeFirst returns s with its first letter uppercased.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// sectionTitle returns a consistently spaced section heading.
func sectionTitle(text string) string {
	return "\n" + planTitleStyle.Render(text) + "\n"
}

// ChatMessageType identifies the kind of chat message.
type ChatMessageType int

const (
	MsgUser ChatMessageType = iota
	MsgAssistant
	MsgThinking
	MsgToolCall
	MsgToolResult
	MsgError
	MsgSystem
	MsgPlanProposal
	MsgPlanTaskStart
	MsgPlanTaskDone
	MsgPlanSummary
	MsgWorkflowStart
	MsgWorkflowStepStart
	MsgWorkflowStepDone
	MsgWorkflowComplete
)

// ChatMessage represents a single rendered message in the chat.
type ChatMessage struct {
	Type       ChatMessageType
	Text       string    // raw text
	Rendered   string    // cached lipgloss/glamour output
	Timestamp  time.Time // when the message was created
	ToolName   string
	IsError    bool
	Detail     string // optional rich detail (e.g. edit diff)
	FilePath   string // for grouping file operations
	IsGrouped  bool   // true if this is part of a file group
	GroupIndex int    // index within the group (0 = header, >0 = sub-items)

	// Re-render metadata: fields needed to re-render at a different width.
	ShowToolName bool          // mirrors the showToolName arg of renderToolResultWithContext
	TurnModel    string        // model name passed to renderTurnInfo
	TurnElapsed  time.Duration // elapsed duration passed to renderTurnInfo
	TurnCost     float64       // cost value passed to renderTurnInfo
	TurnNum      int           // 1-based turn number passed to renderTurnInfo
}

// renderUserMessage creates a rendered user message.
// width is the total terminal width used for wrapping long lines.
func renderUserMessage(text string, width int) ChatMessage {
	now := time.Now()
	bar := userPromptIcon.Render("▎")
	ts := userTimestampStyle.Render("Sent at " + now.Format("3:04 PM"))

	// bar(1) + 2 spaces = 3 columns of prefix per visual line
	const prefix = 3
	contentWidth := width - prefix
	if contentWidth < 20 {
		contentWidth = 20
	}

	lines := strings.Split(text, "\n")
	var sb strings.Builder
	sb.WriteString("\n")
	for _, line := range lines {
		wrapped := wrapLine(line, contentWidth)
		for _, wl := range wrapped {
			sb.WriteString(fmt.Sprintf("%s  %s\n", bar, userPromptStyle.Render(wl)))
		}
	}
	sb.WriteString(fmt.Sprintf("%s  %s\n", bar, ts))
	rendered := sb.String() + "\n"
	return ChatMessage{
		Type:      MsgUser,
		Text:      text,
		Timestamp: now,
		Rendered:  rendered,
	}
}

// wrapLine splits a single line into multiple lines that fit within maxWidth columns.
func wrapLine(line string, maxWidth int) []string {
	if maxWidth <= 0 {
		return []string{line}
	}
	if utf8.RuneCountInString(line) == 0 {
		return []string{""}
	}

	var result []string
	runes := []rune(line)
	start := 0
	col := 0
	lastSpace := -1

	for i, r := range runes {
		w := 1
		if r >= 0x1100 { // rough check for wide chars
			w = 2
		}
		if r == ' ' || r == '\t' {
			lastSpace = i
		}
		if col+w > maxWidth {
			// wrap at last space if available, otherwise hard-wrap
			end := i
			if lastSpace > start {
				end = lastSpace + 1
			}
			result = append(result, string(runes[start:end]))
			start = end
			// skip leading spaces on the new line
			for start < len(runes) && runes[start] == ' ' {
				start++
			}
			col = 0
			lastSpace = -1
			// recount from start to current position
			for k := start; k <= i && k < len(runes); k++ {
				kw := 1
				if runes[k] >= 0x1100 {
					kw = 2
				}
				col += kw
				if runes[k] == ' ' || runes[k] == '\t' {
					lastSpace = k
				}
			}
			continue
		}
		col += w
	}
	if start < len(runes) {
		result = append(result, string(runes[start:]))
	}
	if len(result) == 0 {
		result = []string{""}
	}
	return result
}

// visualRows returns the number of visual terminal rows that a rendered line
// occupies when displayed in a container of innerWidth columns. Lines with no
// visible content still occupy one row.
func visualRows(line string, innerWidth int) int {
	if innerWidth <= 0 {
		return 1
	}
	w := lipgloss.Width(line)
	if w == 0 {
		return 1
	}
	rows := (w + innerWidth - 1) / innerWidth
	if rows < 1 {
		return 1
	}
	return rows
}

// bashReasonLabelStyle is applied to the "Reason for not using X:" prefix in bash tool calls.
var bashReasonLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD080")).Bold(true)

// renderToolCall creates a rendered tool call indicator.
// bashReasons is an optional [4]string of {notReadFile, notEditFile, notGlobFiles, increaseTimeout} justifications;
// empty or "N/A" entries are omitted.
func renderToolCall(name, summary, reason string, bashReasons [4]string, s Styles) ChatMessage {
	dot := toolCallDot.Render("●")
	displayName := name
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.SplitN(name, "__", 3)
		if len(parts) == 3 {
			displayName = "[MCP] " + parts[1] + "." + parts[2]
		}
	}
	text := toolCallStyle.Render(fmt.Sprintf("🔨 %s  %s", displayName, summary))
	rendered := fmt.Sprintf("  %s %s\n", dot, text)
	if reason != "" {
		rendered += s.ToolCallReasonStyle.Render("    ↳ "+reason) + "\n"
	}
	labels := [4]string{
		"Reason for not using read_file: ",
		"Reason for not using edit_file: ",
		"Reason for not using glob_files: ",
		"Reason for increasing timeout: ",
	}
	for i, r := range bashReasons {
		if r != "" && r != "N/A" {
			rendered += "    ↳ " + bashReasonLabelStyle.Render(labels[i]) + s.ToolCallReasonStyle.Render(r) + "\n"
		}
	}

	// Extract file path for grouping
	filePath := extractFilePathFromSummary(name, summary)

	return ChatMessage{
		Type:     MsgToolCall,
		Text:     summary,
		Rendered: rendered,
		ToolName: name,
		FilePath: filePath,
	}
}

// extractFilePathFromSummary extracts the file path from a tool summary.
// Returns empty string if not a file operation or path cannot be determined.
func extractFilePathFromSummary(toolName, summary string) string {
	if toolName != "edit_file" && toolName != "edit_minified_file" && toolName != "read_file" && toolName != "read_minified_file" && toolName != "write_file" && toolName != "write_minified_file" {
		return ""
	}

	// Summary format examples:
	// "path/to/file.go (5 lines changed)"
	// "path/to/file.go (100 chars)"
	// "path/to/file.go:10-20"
	// "path/to/file.go"

	// Find first space or colon to isolate the path
	for i, ch := range summary {
		if ch == ' ' || ch == ':' {
			return summary[:i]
		}
	}
	return summary
}

// formatFetchBytes formats a byte count as a human-readable string.
func formatFetchBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// formatFetchElapsed formats a millisecond duration for display.
func formatFetchElapsed(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// summarizeToolOutput returns a compact one-line summary for known tool outputs.
// It returns "" for tools whose output is already compact and should be shown as-is.
func summarizeToolOutput(name, output string) string {
	lines := strings.Count(output, "\n")
	if output != "" && !strings.HasSuffix(output, "\n") {
		lines++
	}

	switch name {
	case "skill":
		// The skill tool's output is the full rendered body (sent to the model);
		// in the viewport just preview its first non-empty line.
		for _, line := range strings.Split(output, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
		return "skill loaded"
	case "read_file", "read_minified_file":
		return fmt.Sprintf("%d lines read", lines)
	case "grep":
		if lines == 0 || output == "" {
			return "no matches"
		}
		return fmt.Sprintf("%d results", lines)
	case "glob_files":
		if lines == 0 || output == "" {
			return "no matches"
		}
		return fmt.Sprintf("%d files", lines)
	case "lsp_query":
		if lines == 0 || output == "" {
			return "no results"
		}
		return fmt.Sprintf("%d results", lines)
	case "bash":
		if lines == 0 || output == "" {
			return "no output"
		}
		return fmt.Sprintf("%d lines of output", lines)
	case "write_file", "write_minified_file":
		return "file written"
	case "todo_write":
		return "TODO list updated."
	case "web_fetch":
		return formatFetchBytes(len(output))
	default:
		if strings.HasPrefix(name, "mcp__") {
			if lines == 0 || output == "" {
				return "0 rows"
			}
			return fmt.Sprintf("%d rows", lines)
		}
		return ""
	}
}

// renderToolResult creates a rendered tool result.
func renderToolResult(name, output string, isError bool, s Styles, md *MarkdownRenderer, width int) ChatMessage {
	return renderToolResultWithContext(name, output, isError, false, "", s, md, width)
}

// renderToolResultWithContext creates a rendered tool result, optionally prefixing
// with the tool name when multiple tools are executing concurrently.
// detail is an optional rich string (e.g. edit diff) shown below the summary.
// md is used to render code-block previews (e.g. for write_file); may be nil.
// width is the inner content width used for side-by-side diff rendering.
func renderToolResultWithContext(name, output string, isError bool, showToolName bool, detail string, s Styles, md *MarkdownRenderer, width int) ChatMessage {
	// Suppress tool_orchestrator preview entirely
	if name == "tool_orchestrator" {
		return ChatMessage{
			Type:         MsgToolResult,
			ToolName:     name,
			ShowToolName: showToolName,
		}
	}

	prefix := "    ↳ "
	if showToolName {
		prefix = fmt.Sprintf("    ↳ [%s] ", name)
	}

	if isError {
		short := output
		if len(short) > 1000 {
			short = short[:1000] + "..."
		}
		rendered := "  " + errorStyle.Render("ERROR: "+short) + "\n\n"
		if output == "Cancelled" {
			rendered = "  Command cancelled.\n\n"
		}
		return ChatMessage{
			Type:         MsgToolResult,
			Text:         output,
			Rendered:     rendered,
			ToolName:     name,
			IsError:      true,
			ShowToolName: showToolName,
		}
	}

	// web_fetch: show bytes downloaded and elapsed time.
	if name == "web_fetch" {
		summary := formatFetchBytes(len(output))
		if detail != "" {
			if ms, err := strconv.ParseInt(detail, 10, 64); err == nil {
				summary = fmt.Sprintf("%s in %s", summary, formatFetchElapsed(ms))
			}
		}
		return ChatMessage{
			Type:         MsgToolResult,
			Text:         output,
			Rendered:     s.ToolResultStyle.Render(prefix+summary) + "\n\n",
			ToolName:     name,
			Detail:       detail,
			ShowToolName: showToolName,
		}
	}

	// write_file / write_minified_file with a preview: skip the summary line entirely and render
	// only the code box, trimming any leading newline glamour adds.
	if (name == "write_file" || name == "write_minified_file") && detail != "" && md != nil {
		detailRendered := strings.TrimLeft(md.Render(detail), "\n")
		return ChatMessage{
			Type:         MsgToolResult,
			Text:         output,
			Rendered:     detailRendered,
			ToolName:     name,
			Detail:       detail,
			ShowToolName: showToolName,
		}
	}

	var rendered string
	if summary := summarizeToolOutput(name, output); summary != "" {
		rendered = s.ToolResultStyle.Render(prefix+summary) + "\n"
	} else {
		short := output
		if len(short) > 1000 {
			short = short[:1000] + "..."
		}
		rendered = s.ToolResultStyle.Render(prefix+short) + "\n"
	}

	if detail != "" {
		rendered += renderDiffDetail(detail, s, width)
	}
	rendered += "\n"

	return ChatMessage{
		Type:         MsgToolResult,
		Text:         output,
		Rendered:     rendered,
		ToolName:     name,
		Detail:       detail,
		ShowToolName: showToolName,
	}
}

// renderDiffDetail formats an edit diff for side-by-side display below a tool result.
// It parses the structured tag format emitted by FormatEditDiff:
//
//	"H <text>"              — header / info line (shown full-width)
//	"C <leftN> <rightN> <text>" — context line (shown full-width)
//	"R <lineN> <text>"      — removed line (left column)
//	"A <lineN> <text>"      — added line  (right column)
//
// Consecutive R/A rows that belong to the same hunk are rendered side-by-side.
func renderDiffDetail(detail string, s Styles, width int) string {
	const indent = "      " // 6-space indent from left edge
	const indentWidth = 6

	// Derive per-column width.
	// Layout: indent(6) | leftCol | " │ "(3) | rightCol
	// Each column gets equal space; minimum 10 chars each.
	colWidth := (width - indentWidth - 3) / 2
	if colWidth < 10 {
		colWidth = 10
	}

	// Trim and split into tag rows
	rows := strings.Split(strings.TrimRight(detail, "\n"), "\n")

	// Collect R and A rows per hunk position so we can pair them.
	// We process rows in order, flushing a hunk when we see a non-R/A tag.
	type hunkRow struct {
		removeNum  string
		removeText string
		addNum     string
		addText    string
	}

	var sb strings.Builder

	var pendingR []struct{ num, text string }
	var pendingA []struct{ num, text string }

	flushHunk := func() {
		if len(pendingR) == 0 && len(pendingA) == 0 {
			return
		}
		maxLen := len(pendingR)
		if len(pendingA) > maxLen {
			maxLen = len(pendingA)
		}
		for i := 0; i < maxLen; i++ {
			// Left cell: removed line
			var leftNum, leftText string
			if i < len(pendingR) {
				leftNum = pendingR[i].num
				leftText = pendingR[i].text
			}
			// Right cell: added line
			var rightNum, rightText string
			if i < len(pendingA) {
				rightNum = pendingA[i].num
				rightText = pendingA[i].text
			}

			// Expand tabs first so all width measurements are accurate.
			leftExpanded := expandTabs(leftText, 4)
			rightExpanded := expandTabs(rightText, 4)

			// When both sides are present and the text is identical, treat as a
			// context line (unchanged) and render in white on both sides.
			paired := leftNum != "" && rightNum != ""
			unchanged := paired && leftExpanded == rightExpanded

			// Build left cell.
			// Context (unchanged) lines get a neutral "  " gutter; changed lines get "- ".
			leftMarker := "- "
			if unchanged {
				leftMarker = "  "
			}
			leftPlain := ""
			if leftNum != "" {
				leftPlain = leftNum + leftMarker + leftExpanded
			}
			if lipgloss.Width(leftPlain) > colWidth {
				leftPlain = leftPlain[:colWidth]
			}
			leftPad := colWidth - lipgloss.Width(leftPlain)
			if leftPad < 0 {
				leftPad = 0
			}
			var leftCell string
			if unchanged {
				// Identical on both sides — render as plain context (white).
				leftCell = s.ToolResultStyle.Render(leftPlain) + strings.Repeat(" ", leftPad)
			} else if leftNum != "" {
				leftCell = diffRemoveStyle.Render(leftPlain) + strings.Repeat(" ", leftPad)
			} else {
				// Pure add: show a dimgray background placeholder on the left.
				leftCell = diffEmptyStyle.Render(strings.Repeat(" ", colWidth))
			}

			// Build right cell.
			// Context (unchanged) lines get a neutral "  " gutter; changed lines get "+ ".
			rightMarker := "+ "
			if unchanged {
				rightMarker = "  "
			}
			rightPlain := ""
			if rightNum != "" {
				rightPlain = rightNum + rightMarker + rightExpanded
			}
			if lipgloss.Width(rightPlain) > colWidth {
				rightPlain = rightPlain[:colWidth]
			}
			rightPad := colWidth - lipgloss.Width(rightPlain)
			if rightPad < 0 {
				rightPad = 0
			}
			var rightCell string
			if unchanged {
				// Identical on both sides — render as plain context (white).
				rightCell = s.ToolResultStyle.Render(rightPlain) + strings.Repeat(" ", rightPad)
			} else if rightNum != "" {
				rightCell = diffAddStyle.Render(rightPlain) + strings.Repeat(" ", rightPad)
			} else {
				// Pure delete: show a dimgray background placeholder on the right.
				rightCell = diffEmptyStyle.Render(strings.Repeat(" ", colWidth))
			}

			sep := s.ToolResultStyle.Render(" │ ")
			sb.WriteString(indent + leftCell + sep + rightCell + "\n")
		}
		pendingR = pendingR[:0]
		pendingA = pendingA[:0]
	}

	for _, row := range rows {
		if row == "" {
			continue
		}
		tag := string(row[0])
		rest := ""
		if len(row) > 2 {
			rest = row[2:]
		}

		switch tag {
		case "R":
			// "R <lineN> <text>" — removed line
			parts := strings.SplitN(rest, " ", 2)
			num := parts[0]
			text := ""
			if len(parts) > 1 {
				text = parts[1]
			}
			pendingR = append(pendingR, struct{ num, text string }{num, text})

		case "A":
			// "A <lineN> <text>" — added line
			parts := strings.SplitN(rest, " ", 2)
			num := parts[0]
			text := ""
			if len(parts) > 1 {
				text = parts[1]
			}
			pendingA = append(pendingA, struct{ num, text string }{num, text})

		case "C":
			// Context line — flush pending hunk first, then render side-by-side.
			flushHunk()
			// "C <leftN> <rightN> <text>"
			parts := strings.SplitN(rest, " ", 3)
			leftNum := parts[0]
			rightNum := ""
			if len(parts) > 1 {
				rightNum = parts[1]
			}
			text := ""
			if len(parts) > 2 {
				text = parts[2]
			}
			expanded := expandTabs(text, 4)

			leftPlain := leftNum + "  " + expanded
			if lipgloss.Width(leftPlain) > colWidth {
				leftPlain = leftPlain[:colWidth]
			}
			leftPad := colWidth - lipgloss.Width(leftPlain)
			if leftPad < 0 {
				leftPad = 0
			}
			leftCell := s.ToolResultStyle.Render(leftPlain) + strings.Repeat(" ", leftPad)

			rightPlain := rightNum + "  " + expanded
			if lipgloss.Width(rightPlain) > colWidth {
				rightPlain = rightPlain[:colWidth]
			}
			rightCell := s.ToolResultStyle.Render(rightPlain)

			sep := s.ToolResultStyle.Render(" │ ")
			sb.WriteString(indent + leftCell + sep + rightCell + "\n")

		case "H":
			// Header / info line — flush pending hunk first, then render full-width
			flushHunk()
			sb.WriteString(indent + s.ToolResultStyle.Render(rest) + "\n")

		default:
			// Unknown tag — flush hunk and render raw (backward compat)
			flushHunk()
			sb.WriteString(indent + s.ToolResultStyle.Render(row) + "\n")
		}
	}
	flushHunk()

	return sb.String()
}

// renderAssistantMessage creates a rendered assistant message using Glamour.
func renderAssistantMessage(text string, md *MarkdownRenderer) ChatMessage {
	rendered := strings.TrimLeft(md.Render(text), "\n")
	return ChatMessage{
		Type:     MsgAssistant,
		Text:     text,
		Rendered: rendered,
	}
}

// renderThinkingText styles and wraps thinking content with a 2-col indent
// matching other chat messages. Used by both the live streaming preview and
// the persisted scrollback entry.
func renderThinkingText(text string, s Styles, width int) string {
	const prefix = 2
	contentWidth := width - prefix - 2 // also subtract panel border
	if contentWidth < 20 {
		contentWidth = 20
	}
	var sb strings.Builder
	for _, line := range strings.Split(text, "\n") {
		wrapped := wrapLine(line, contentWidth)
		if len(wrapped) == 0 {
			sb.WriteString("\n")
			continue
		}
		for _, wl := range wrapped {
			sb.WriteString("  ")
			sb.WriteString(s.ThinkingStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// renderThinkingMessage creates a persisted extended-thinking chat message.
func renderThinkingMessage(text string, s Styles, width int) ChatMessage {
	return ChatMessage{
		Type:     MsgThinking,
		Text:     text,
		Rendered: renderThinkingText(text, s, width) + "\n",
	}
}

// formatConversationPlainText serializes chat messages to a plain-text
// transcript suitable for copying to the clipboard. Uses msg.Text (raw)
// rather than msg.Rendered (ANSI-styled) so the output is clean.
func formatConversationPlainText(msgs []ChatMessage) string {
	var sb strings.Builder
	for _, msg := range msgs {
		switch msg.Type {
		case MsgUser:
			sb.WriteString("User: ")
			sb.WriteString(msg.Text)
			sb.WriteString("\n\n")
		case MsgAssistant:
			sb.WriteString("Assistant: ")
			sb.WriteString(msg.Text)
			sb.WriteString("\n\n")
		case MsgThinking:
			sb.WriteString("Thinking: ")
			sb.WriteString(msg.Text)
			sb.WriteString("\n\n")
		case MsgToolCall:
			fmt.Fprintf(&sb, "[Tool call: %s] %s\n\n", msg.ToolName, msg.Text)
		case MsgToolResult:
			if msg.Text != "" {
				fmt.Fprintf(&sb, "[Tool result: %s]\n%s\n\n", msg.ToolName, msg.Text)
			}
		case MsgError:
			fmt.Fprintf(&sb, "[Error] %s\n\n", msg.Text)
		case MsgSystem:
			fmt.Fprintf(&sb, "[System] %s\n\n", msg.Text)
		default:
			if msg.Text != "" {
				sb.WriteString(msg.Text)
				sb.WriteString("\n\n")
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

// renderErrorMessage creates a rendered error message.
func renderErrorMessage(err error) ChatMessage {
	rendered := "  " + errorStyle.Render(fmt.Sprintf("Error: %s", err)) + "\n\n"
	return ChatMessage{
		Type:     MsgError,
		Text:     err.Error(),
		Rendered: rendered,
		IsError:  true,
	}
}

// renderRetryMessage creates a rendered retry status message.
func renderRetryMessage(retry protocol.EventRetry) ChatMessage {
	text := fmt.Sprintf("%s — retrying in %ds (attempt %d/%d)",
		retry.Reason, retry.WaitSecs, retry.Attempt, retry.MaxRetries)
	rendered := "  " + retryStyle.Render(text) + "\n"
	return ChatMessage{
		Type:     MsgSystem,
		Text:     text,
		Rendered: rendered,
	}
}

// renderSystemMessage creates a rendered system message.
func renderSystemMessage(text string, s Styles) ChatMessage {
	rendered := "  " + s.SystemStyle.Render(text) + "\n\n"
	return ChatMessage{
		Type:     MsgSystem,
		Text:     text,
		Rendered: rendered,
	}
}

// renderSystemSuccessMessage creates a rendered system success message (in green).
func renderSystemSuccessMessage(text string) ChatMessage {
	rendered := "  " + systemSuccessStyle.Render(text) + "\n\n"
	return ChatMessage{
		Type:     MsgSystem,
		Text:     text,
		Rendered: rendered,
	}
}

// groupFileOperations groups consecutive file operations on the same file.
// It identifies sequences of tool calls/results for the same file and marks them
// for grouped rendering.
func groupFileOperations(messages []ChatMessage) []ChatMessage {
	if len(messages) == 0 {
		return messages
	}

	result := make([]ChatMessage, 0, len(messages))
	i := 0

	for i < len(messages) {
		msg := messages[i]

		// Only group file operations (edit_file, write_file, read_file)
		if msg.Type != MsgToolCall || msg.FilePath == "" {
			result = append(result, msg)
			i++
			continue
		}

		// Look ahead to find consecutive operations on the same file
		groupPath := msg.FilePath
		groupItems := []ChatMessage{msg}

		// Collect the result for this call (if it exists and is next)
		if i+1 < len(messages) && messages[i+1].Type == MsgToolResult && messages[i+1].ToolName == msg.ToolName {
			groupItems = append(groupItems, messages[i+1])
			i++
		}
		i++

		// Look for more operations on the same file
		for i < len(messages) {
			nextMsg := messages[i]

			// Stop if not a tool call or different file
			if nextMsg.Type != MsgToolCall || nextMsg.FilePath != groupPath {
				break
			}

			groupItems = append(groupItems, nextMsg)

			// Include the result if it follows
			if i+1 < len(messages) && messages[i+1].Type == MsgToolResult && messages[i+1].ToolName == nextMsg.ToolName {
				groupItems = append(groupItems, messages[i+1])
				i++
			}
			i++
		}

		// If we found multiple operations on the same file, create a group
		// Group if we have at least 2 tool calls (with or without results)
		callCount := 0
		for _, item := range groupItems {
			if item.Type == MsgToolCall {
				callCount++
			}
		}

		if callCount >= 2 {
			// Create group header
			header := createFileGroupHeader(groupPath, groupItems)
			result = append(result, header)

			// Add sub-items
			for idx, item := range groupItems {
				subItem := item
				subItem.IsGrouped = true
				subItem.GroupIndex = idx + 1
				result = append(result, subItem)
			}
		} else {
			// Not enough items to group, add them normally
			result = append(result, groupItems...)
		}
	}

	return result
}

// createFileGroupHeader creates a header message for a group of file operations.
func createFileGroupHeader(filePath string, items []ChatMessage) ChatMessage {
	// Get the tool name from the first operation
	toolName := "edit_file"
	for _, item := range items {
		if item.Type == MsgToolCall {
			toolName = item.ToolName
			break
		}
	}

	dot := toolCallDot.Render("●")
	text := toolCallStyle.Render(fmt.Sprintf("🔨 %s  %s", toolName, filePath))
	rendered := fmt.Sprintf("  %s %s\n", dot, text)

	return ChatMessage{
		Type:       MsgToolCall,
		Text:       filePath,
		Rendered:   rendered,
		ToolName:   toolName,
		FilePath:   filePath,
		IsGrouped:  true,
		GroupIndex: 0, // 0 indicates this is the group header
	}
}

// buildRenderedChat concatenates all rendered messages into a single string.
// It applies grouping to file operations before rendering.
// width is the inner content width, used for side-by-side diff rendering.
func buildRenderedChat(messages []ChatMessage, s Styles, width int) string {
	grouped := groupFileOperations(messages)

	var sb strings.Builder
	for i, msg := range grouped {
		if msg.Rendered == "" {
			continue
		}

		// Skip rendering grouped items' original format, render them as sub-items instead
		if msg.IsGrouped && msg.GroupIndex > 0 {
			rendered := renderGroupedItem(msg, s, width)
			sb.WriteString(rendered)

			// After the last sub-item of a group, add a blank line to match the
			// spacing that ungrouped tool results produce.
			isLastInGroup := i+1 >= len(grouped) ||
				!grouped[i+1].IsGrouped ||
				grouped[i+1].GroupIndex == 0
			if isLastInGroup {
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString(msg.Rendered)
		}
	}
	return sb.String()
}

// renderGroupedItem renders a tool call or result as a sub-item in a file group.
// width is the inner content width, used for side-by-side diff rendering.
func renderGroupedItem(msg ChatMessage, s Styles, width int) string {
	switch msg.Type {
	case MsgToolCall:
		// Extract the operation details from the summary (everything after the file path)
		// The summary format is like "path/to/file (details)" or just "path/to/file"
		details := msg.Text
		if msg.FilePath != "" && strings.HasPrefix(details, msg.FilePath) {
			// Remove the file path part, keep only the details
			remainder := strings.TrimPrefix(details, msg.FilePath)
			remainder = strings.TrimSpace(remainder)
			if remainder != "" {
				details = remainder
			}
		}
		return toolCallStyle.Render(fmt.Sprintf("    ↳ %s", details)) + "\n"

	case MsgToolResult:
		// Show result with proper indentation, prefixed with tool name
		if msg.IsError {
			if msg.Text == "Cancelled" {
				return "      Command cancelled.\n"
			}
			short := msg.Text
			if len(short) > 1000 {
				short = short[:1000] + "..."
			}
			return "      " + errorStyle.Render("ERROR: "+short) + "\n"
		}

		// Mirror the ungrouped rendering: summary line + optional diff detail
		prefix := fmt.Sprintf("    ↳ [%s] ", msg.ToolName)
		var line string
		if summary := summarizeToolOutput(msg.ToolName, msg.Text); summary != "" {
			line = s.ToolResultStyle.Render(prefix+summary) + "\n"
		} else {
			short := msg.Text
			if len(short) > 1000 {
				short = short[:1000] + "..."
			}
			line = s.ToolResultStyle.Render(prefix+short) + "\n"
		}
		if msg.Detail != "" {
			line += renderDiffDetail(msg.Detail, s, width)
		}
		return line
	default:
		return msg.Rendered
	}
}

// renderQuestionMessage renders a question into the chat viewport as a styled
// bordered box, so it is visible in the scrollback regardless of terminal height.
// category is shown in the top border (e.g. "Question", "Permission").
// question is the question text, rendered via the markdown renderer.
// width is the total content width (same convention as renderTurnInfo: mdRenderer.width+4).
func renderQuestionMessage(category, question string, width int, md *MarkdownRenderer) ChatMessage {
	// For permission prompts, render as plain text (no bordered box).
	if category == "Permission" {
		questionStyle := lipgloss.NewStyle().Foreground(colorSecondary).Bold(true)
		rendered := "  " + questionStyle.Render(question) + "\n"
		return ChatMessage{
			Type:     MsgSystem,
			Text:     question,
			Rendered: rendered,
		}
	}

	borderStyle := md.codeBoxBorder

	// Inner content width: total width minus 2 indent ("  "), 2 border chars, 2 padding spaces.
	innerWidth := width - 6
	if innerWidth < 10 {
		innerWidth = 10
	}
	totalBorderWidth := innerWidth + 2 // +2 for the padding spaces inside the box

	// Build top border with category label
	label := " " + category + " "
	labelWidth := lipgloss.Width(label)
	remainingDashes := totalBorderWidth - 1 - labelWidth
	if remainingDashes < 0 {
		remainingDashes = 0
	}
	topBorder := "  " + borderStyle.Render("╭─"+label+strings.Repeat("─", remainingDashes)+"╮")

	// Render question text (strip leading newline glamour may add)
	rendered := strings.TrimLeft(md.Render(question), "\n")
	// Wrap each line in a bordered row
	var contentLines []string
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		visualWidth := lipgloss.Width(line)
		padding := innerWidth - visualWidth
		if padding < 0 {
			padding = 0
		}
		padded := line + strings.Repeat(" ", padding)
		contentLines = append(contentLines, "  "+borderStyle.Render("│")+" "+padded+" "+borderStyle.Render("│"))
	}

	bottomBorder := "  " + borderStyle.Render("╰"+strings.Repeat("─", totalBorderWidth)+"╯")

	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(topBorder + "\n")
	for _, line := range contentLines {
		sb.WriteString(line + "\n")
	}
	sb.WriteString(bottomBorder + "\n")

	return ChatMessage{
		Type:     MsgSystem,
		Text:     question,
		Rendered: sb.String(),
	}
}

// renderQuestionAnswer renders answered Q&A pairs into the chat scrollback.
func renderQuestionAnswer(pairs []QAPair, s Styles) ChatMessage {
	var sb strings.Builder
	arrowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(boldColor))
	answerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	for _, p := range pairs {
		if p.Category == "Permission" {
			// The question was already shown when the confirm prompt appeared;
			// only render the user's selection.
			arrow := arrowStyle.Render("  → User selection: ")
			answer := answerStyle.Render(p.Answer)
			sb.WriteString(arrow + answer + "\n\n")
			continue
		}
		sb.WriteString("\n  " + s.QuestionTextStyle.Render(p.Question) + "\n")
		arrow := arrowStyle.Render("  → User selection: ")
		answer := answerStyle.Render(p.Answer)
		sb.WriteString(arrow + answer + "\n\n")
	}
	return ChatMessage{
		Type:     MsgSystem,
		Rendered: sb.String(),
	}
}

// renderPlanProposal renders a plan for user review.
func renderPlanProposal(plan *protocol.Plan, s Styles) ChatMessage {
	var sb strings.Builder

	header := planHeaderStyle.Render(" " + plan.Name + " ")
	sb.WriteString("\n" + header + "\n")
	sb.WriteString(s.PlanDescStyle.Render("  "+plan.Context) + "\n")

	if plan.Architecture != "" {
		sb.WriteString(sectionTitle("  Architecture"))
		sb.WriteString(s.PlanDescStyle.Render("  "+plan.Architecture) + "\n")
	}

	if len(plan.Files) > 0 {
		sb.WriteString(sectionTitle("  Files"))
		for _, f := range plan.Files {
			sb.WriteString(s.PlanDescStyle.Render("    "+f) + "\n")
		}
	}

	sb.WriteString("\n")
	for _, task := range plan.Tasks {
		bullet := s.PlanBulletStyle.Render(fmt.Sprintf("  [ ] %d.", task.ID))
		title := planRunningStyle.Render(task.Title)
		sb.WriteString(fmt.Sprintf("%s %s\n", bullet, title))
		if task.Description != "" {
			desc := s.PlanDescStyle.Render("      " + task.Description)
			sb.WriteString(desc + "\n")
		}
		for _, sub := range task.Substeps {
			sb.WriteString(s.PlanDescStyle.Render("        - "+sub) + "\n")
		}
	}

	if plan.Risks != "" {
		sb.WriteString(sectionTitle("  Risks"))
		for _, line := range strings.Split(plan.Risks, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				sb.WriteString(s.PlanDescStyle.Render("  "+line) + "\n")
			}
		}
	}

	separator := s.PlanPromptDimStyle.Render("  ──────────────────────────────────────────")
	sb.WriteString("\n" + separator + "\n\n")
	sb.WriteString("  " + planPromptActionStyle.Render("Accept") + s.PlanPromptDimStyle.Render(" — ") + planPromptKeyStyle.Render("press Enter or y") + "\n")
	sb.WriteString("  " + planPromptActionStyle.Render("Modify") + s.PlanPromptDimStyle.Render(" — ") + planPromptKeyStyle.Render("type feedback + Enter") + "\n")
	sb.WriteString("  " + planPromptActionStyle.Render("Reject") + s.PlanPromptDimStyle.Render(" — ") + planPromptKeyStyle.Render("press n or Esc") + "\n")

	return ChatMessage{
		Type:     MsgPlanProposal,
		Text:     plan.Name,
		Rendered: sb.String(),
	}
}

// renderPlanTaskStart renders a task-starting indicator.
func renderPlanTaskStart(taskIdx int, title string, total int) ChatMessage {
	indicator := planRunningStyle.Render(fmt.Sprintf(">>> Task %d/%d:", taskIdx, total))
	rendered := fmt.Sprintf("\n%s\n%s\n", indicator, planRunningStyle.Render(title))
	return ChatMessage{
		Type:     MsgPlanTaskStart,
		Text:     title,
		Rendered: rendered,
	}
}

// renderPlanTaskDone renders a task-completion indicator.
func renderPlanTaskDone(taskIdx int, title string, success bool, summary string, s Styles) ChatMessage {
	var marker string
	if success {
		marker = planDoneStyle.Render(fmt.Sprintf("[x] Task %d:", taskIdx))
	} else {
		marker = planFailStyle.Render(fmt.Sprintf("[!] Task %d:", taskIdx))
	}

	short := summary
	if len(short) > 120 {
		short = short[:120] + "..."
	}

	rendered := fmt.Sprintf("%s %s\n", marker, title)
	if short != "" {
		rendered += s.PlanDescStyle.Render("    "+short) + "\n"
	}

	return ChatMessage{
		Type:     MsgPlanTaskDone,
		Text:     title,
		Rendered: rendered,
	}
}

// renderPlanSummary renders the final plan summary.
func renderPlanSummary(plan *protocol.Plan) ChatMessage {
	succeeded := 0
	for _, t := range plan.Tasks {
		if t.Status == protocol.TaskCompleted {
			succeeded++
		}
	}

	header := planDoneHeaderStyle.Render(" DONE ")
	summary := fmt.Sprintf("%s %d/%d tasks succeeded\n", header, succeeded, len(plan.Tasks))

	return ChatMessage{
		Type:     MsgPlanSummary,
		Text:     summary,
		Rendered: "\n" + summary,
	}
}

// renderWorkflowStart renders a workflow-starting indicator.
func renderWorkflowStart(name string, totalSteps int, s Styles) ChatMessage {
	header := planHeaderStyle.Render(fmt.Sprintf(" Workflow: %s ", name))
	rendered := fmt.Sprintf("\n%s %s\n", header, s.PlanDescStyle.Render(fmt.Sprintf("(%d steps)", totalSteps)))
	return ChatMessage{
		Type:     MsgWorkflowStart,
		Text:     name,
		Rendered: rendered,
	}
}

// renderWorkflowStatus renders a workflow run status transition (paused,
// blocked, budget limited) as a dim system line in the transcript.
func renderWorkflowStatus(name, status, stepID string, iteration int, tokensUsed, tokenBudget int64, note string, s Styles) ChatMessage {
	var label string
	switch status {
	case "paused":
		label = "Workflow paused"
	case "blocked":
		label = "Workflow blocked"
	case "budget_limited":
		label = "Workflow budget exhausted"
	default:
		label = "Workflow " + status
	}
	detail := fmt.Sprintf("%s — %q", label, name)
	if stepID != "" {
		detail += fmt.Sprintf(" at step '%s'", stepID)
	}
	if iteration > 0 {
		detail += fmt.Sprintf(" (iteration %d", iteration)
		if tokenBudget > 0 {
			detail += fmt.Sprintf(" · %d/%d tokens", tokensUsed, tokenBudget)
		} else if tokensUsed > 0 {
			detail += fmt.Sprintf(" · %d tokens", tokensUsed)
		}
		detail += ")"
	}
	if note != "" {
		detail += ": " + note
	}
	rendered := fmt.Sprintf("\n%s\n", s.PlanDescStyle.Render(detail))
	return ChatMessage{
		Type:     MsgSystem,
		Text:     detail,
		Rendered: rendered,
	}
}

// renderWorkflowStepStart renders a workflow step starting indicator.
func renderWorkflowStepStart(stepID string, stepIdx, total int, explanation string) ChatMessage {
	var prefix string
	if total > 0 {
		prefix = fmt.Sprintf(">>> Step %d/%d:", stepIdx, total)
	} else {
		prefix = fmt.Sprintf(">>> Step %d:", stepIdx)
	}
	title := capitalizeFirst(explanation)
	stepStyle := lipgloss.NewStyle().Foreground(colorSecondary)
	rendered := fmt.Sprintf("\n%s\n\n", stepStyle.Render(prefix+" "+title))
	return ChatMessage{
		Type:     MsgWorkflowStepStart,
		Text:     stepID,
		Rendered: rendered,
	}
}

// renderWorkflowStepDone renders a workflow step completion indicator with tool summaries.
func renderWorkflowStepDone(stepID string, stepIdx, total int, success bool, display, command, bashOutput string, toolStats []protocol.ToolStat, md *MarkdownRenderer, s Styles) ChatMessage {
	var sb strings.Builder

	// Bash step: show the command and first 5 lines of output
	if command != "" {
		sb.WriteString(s.PlanDescStyle.Render("  $ "+command) + "\n")
		if bashOutput != "" {
			for _, line := range strings.Split(bashOutput, "\n") {
				sb.WriteString(s.ToolResultStyle.Render("    "+line) + "\n")
			}
		}
		sb.WriteString("\n")
	}

	// Summary section
	if display != "" {
		sb.WriteString(sectionTitle("  Summary"))
	}
	if display != "" {
		sb.WriteString(md.Render(display))
	}
	if len(toolStats) > 0 {
		sb.WriteString(s.PlanDescStyle.Render("  Tool usage") + "\n\n")

		// Compute column widths
		colTool, colCalls, colSummary := len("Tool"), len("Calls"), len("Summary")
		for _, ts := range toolStats {
			if len(ts.Name) > colTool {
				colTool = len(ts.Name)
			}
			c := fmt.Sprintf("%d", ts.Calls)
			if len(c) > colCalls {
				colCalls = len(c)
			}
			if len(ts.Summary) > colSummary {
				colSummary = len(ts.Summary)
			}
		}

		hLine := fmt.Sprintf("  ├─%s─┼─%s─┼─%s─┤",
			strings.Repeat("─", colTool), strings.Repeat("─", colCalls), strings.Repeat("─", colSummary))
		top := fmt.Sprintf("  ┌─%s─┬─%s─┬─%s─┐",
			strings.Repeat("─", colTool), strings.Repeat("─", colCalls), strings.Repeat("─", colSummary))
		bottom := fmt.Sprintf("  └─%s─┴─%s─┴─%s─┘",
			strings.Repeat("─", colTool), strings.Repeat("─", colCalls), strings.Repeat("─", colSummary))
		row := func(a, b, c string) string {
			return fmt.Sprintf("  │ %-*s │ %-*s │ %-*s │", colTool, a, colCalls, b, colSummary, c)
		}

		sb.WriteString(s.PlanDescStyle.Render(top) + "\n")
		sb.WriteString(s.PlanDescStyle.Render(row("Tool", "Calls", "Summary")) + "\n")
		sb.WriteString(s.PlanDescStyle.Render(hLine) + "\n")
		for i, ts := range toolStats {
			sb.WriteString(s.PlanDescStyle.Render(row(ts.Name, fmt.Sprintf("%d", ts.Calls), ts.Summary)) + "\n")
			if i < len(toolStats)-1 {
				sb.WriteString(s.PlanDescStyle.Render(hLine) + "\n")
			}
		}
		sb.WriteString(s.PlanDescStyle.Render(bottom) + "\n")
	}

	// Completion marker (failure only)
	if !success {
		var marker string
		if total > 0 {
			marker = planFailStyle.Render(fmt.Sprintf("[!] Step %d/%d:", stepIdx, total))
		} else {
			marker = planFailStyle.Render(fmt.Sprintf("[!] Step %d:", stepIdx))
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", marker, planFailStyle.Render(capitalizeFirst(stepID))))
	}

	return ChatMessage{
		Type:     MsgWorkflowStepDone,
		Text:     stepID,
		Rendered: sb.String(),
	}
}

// renderWorkflowComplete renders a workflow completion summary with a cost table.
func renderWorkflowComplete(name string, success bool, summary string, stepCosts []protocol.StepCost, durationMs int64, s Styles) ChatMessage {
	var sb strings.Builder

	// Workflow summary header
	sb.WriteString("\n" + planRunningStyle.Render(">>> Workflow summary") + "\n\n")

	// Config-driven summary (replaces hardcoded "Steps executed:" list)
	if summary != "" {
		sb.WriteString(s.PlanDescStyle.Render("    "+summary) + "\n")
	}

	// Cost summary table
	if len(stepCosts) > 0 {
		sb.WriteString("\n" + s.PlanDescStyle.Render("    Cost & Time breakdown") + "\n")

		var totalInput, totalOutput, totalCacheWrite, totalCacheRead, totalDurationMs int64
		var totalCost float64
		for _, sc := range stepCosts {
			totalInput += sc.InputTokens
			totalOutput += sc.OutputTokens
			totalCacheWrite += sc.CacheCreationTokens
			totalCacheRead += sc.CacheReadTokens
			totalCost += sc.Cost
			totalDurationMs += sc.DurationMs
		}

		// Compute column widths from headers and data
		colStep := len("Steps")
		colInput := len("Input Tokens")
		colCacheW := len("Cache Writes")
		colCacheR := len("Cache Hits")
		colOutput := len("Output Tokens")
		colCost := len("Cost")
		colPct := len("% (Cost)")
		colTime := len("Time")
		colTimePct := len("% (Time)")

		type rowData struct {
			step, input, cacheW, cacheR, output, cost, pct, time, timePct string
		}
		rows := make([]rowData, 0, len(stepCosts))
		for i, sc := range stepCosts {
			pct := "0%"
			if totalCost > 0 {
				pct = fmt.Sprintf("%.0f%%", sc.Cost/totalCost*100)
			}
			timePct := "0%"
			if totalDurationMs > 0 {
				timePct = fmt.Sprintf("%.0f%%", float64(sc.DurationMs)/float64(totalDurationMs)*100)
			}
			r := rowData{
				step:    fmt.Sprintf("%d. %s", i+1, capitalizeFirst(sc.StepID)),
				input:   formatTokenCount(sc.InputTokens),
				cacheW:  formatTokenCount(sc.CacheCreationTokens),
				cacheR:  formatTokenCount(sc.CacheReadTokens),
				output:  formatTokenCount(sc.OutputTokens),
				cost:    fmt.Sprintf("$%.2f", sc.Cost),
				pct:     pct,
				time:    fmt.Sprintf("%.1fs", float64(sc.DurationMs)/1000.0),
				timePct: timePct,
			}
			rows = append(rows, r)
			if len(r.step) > colStep {
				colStep = len(r.step)
			}
			if len(r.input) > colInput {
				colInput = len(r.input)
			}
			if len(r.cacheW) > colCacheW {
				colCacheW = len(r.cacheW)
			}
			if len(r.cacheR) > colCacheR {
				colCacheR = len(r.cacheR)
			}
			if len(r.output) > colOutput {
				colOutput = len(r.output)
			}
			if len(r.cost) > colCost {
				colCost = len(r.cost)
			}
			if len(r.pct) > colPct {
				colPct = len(r.pct)
			}
			if len(r.time) > colTime {
				colTime = len(r.time)
			}
			if len(r.timePct) > colTimePct {
				colTimePct = len(r.timePct)
			}
		}
		totalRow := rowData{
			step:    "Total",
			input:   formatTokenCount(totalInput),
			cacheW:  formatTokenCount(totalCacheWrite),
			cacheR:  formatTokenCount(totalCacheRead),
			output:  formatTokenCount(totalOutput),
			cost:    fmt.Sprintf("$%.2f", totalCost),
			pct:     "100%",
			time:    fmt.Sprintf("%.1fs", float64(durationMs)/1000.0),
			timePct: "100%",
		}
		if len(totalRow.step) > colStep {
			colStep = len(totalRow.step)
		}
		if len(totalRow.input) > colInput {
			colInput = len(totalRow.input)
		}
		if len(totalRow.cacheW) > colCacheW {
			colCacheW = len(totalRow.cacheW)
		}
		if len(totalRow.cacheR) > colCacheR {
			colCacheR = len(totalRow.cacheR)
		}
		if len(totalRow.output) > colOutput {
			colOutput = len(totalRow.output)
		}
		if len(totalRow.cost) > colCost {
			colCost = len(totalRow.cost)
		}
		if len(totalRow.pct) > colPct {
			colPct = len(totalRow.pct)
		}
		if len(totalRow.time) > colTime {
			colTime = len(totalRow.time)
		}
		if len(totalRow.timePct) > colTimePct {
			colTimePct = len(totalRow.timePct)
		}

		cols := []int{colStep, colInput, colCacheW, colCacheR, colOutput, colCost, colPct, colTime, colTimePct}
		borderLine := func(left, mid, right string) string {
			parts := make([]string, len(cols))
			for i, w := range cols {
				parts[i] = strings.Repeat("─", w+2)
			}
			return "    " + left + strings.Join(parts, mid) + right
		}
		row := func(r rowData) string {
			return fmt.Sprintf("    │ %-*s │ %-*s │ %-*s │ %-*s │ %-*s │ %*s │ %*s │ %*s │ %*s │",
				colStep, r.step, colInput, r.input, colCacheW, r.cacheW,
				colCacheR, r.cacheR, colOutput, r.output, colCost, r.cost, colPct, r.pct,
				colTime, r.time, colTimePct, r.timePct)
		}

		sb.WriteString(s.PlanDescStyle.Render(borderLine("┌", "┬", "┐")) + "\n")
		sb.WriteString(s.PlanDescStyle.Render(row(rowData{step: "Steps", input: "Input Tokens", cacheW: "Cache Writes", cacheR: "Cache Hits", output: "Output Tokens", cost: "Cost", pct: "% (Cost)", time: "Time", timePct: "% (Time)"})) + "\n")
		sb.WriteString(s.PlanDescStyle.Render(borderLine("├", "┼", "┤")) + "\n")
		for _, r := range rows {
			sb.WriteString(s.PlanDescStyle.Render(row(r)) + "\n")
		}
		sb.WriteString(s.PlanDescStyle.Render(borderLine("├", "┼", "┤")) + "\n")
		sb.WriteString(s.PlanDescStyle.Bold(true).Render(row(totalRow)) + "\n")
		sb.WriteString(s.PlanDescStyle.Render(borderLine("└", "┴", "┘")) + "\n")
	}

	// DONE / FAILED header at the end
	if success {
		header := planDoneHeaderStyle.Render(" DONE ")
		sb.WriteString(fmt.Sprintf("\n%s Workflow '%s' completed\n\n", header, name))
	} else {
		header := planFailStyle.Render(" FAILED ")
		sb.WriteString(fmt.Sprintf("\n%s Workflow '%s' failed\n\n", header, name))
	}

	return ChatMessage{
		Type:     MsgWorkflowComplete,
		Text:     name,
		Rendered: sb.String(),
	}
}

// formatModelName converts a model ID like "claude-sonnet-4-6" to "Claude Sonnet 4.6".
// Non-Claude models are title-cased as-is.
func formatModelName(model string) string {
	isClaude := strings.HasPrefix(model, "claude-")
	if isClaude {
		model = strings.TrimPrefix(model, "claude-")
	}
	parts := strings.Split(model, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = string(unicode.ToUpper(rune(p[0]))) + p[1:]
		}
	}
	// Join version numbers with dots: "Sonnet 4 6" → "Sonnet 4.6"
	result := strings.Join(parts, " ")
	// Find last space-separated number pair and join with dot
	words := strings.Fields(result)
	if len(words) >= 2 {
		last := words[len(words)-1]
		prev := words[len(words)-2]
		if len(last) <= 2 && last[0] >= '0' && last[0] <= '9' && len(prev) <= 2 && prev[0] >= '0' && prev[0] <= '9' {
			words = append(words[:len(words)-2], prev+"."+last)
			result = strings.Join(words, " ")
		}
	}
	if isClaude {
		return "Claude " + result
	}
	return result
}

// rerender re-renders msg at the given width using the provided markdown renderer and styles.
// Width-insensitive message types are returned unchanged.
func (msg ChatMessage) rerender(md *MarkdownRenderer, s Styles, width int) ChatMessage {
	switch msg.Type {
	case MsgUser:
		result := renderUserMessage(msg.Text, width-4)
		result.Timestamp = msg.Timestamp
		return result
	case MsgAssistant:
		return renderAssistantMessage(msg.Text, md)
	case MsgThinking:
		return renderThinkingMessage(msg.Text, s, width)
	case MsgToolCall:
		// Reasons are not width-sensitive so we skip them on re-render.
		return renderToolCall(msg.ToolName, msg.Text, "", [4]string{}, s)
	case MsgToolResult:
		return renderToolResultWithContext(msg.ToolName, msg.Text, msg.IsError, msg.ShowToolName, msg.Detail, s, md, width-4)
	case MsgSystem:
		if msg.TurnModel != "" {
			return renderTurnInfo(msg.TurnModel, msg.TurnElapsed, msg.TurnCost, msg.TurnNum, width, s)
		}
		return msg
	default:
		return msg
	}
}

// TurnSepInfo describes the position of a turn separator in the rendered chat.
type TurnSepInfo struct {
	LineIdx int // 0-based index in the allLines slice from buildRenderedChat
	MsgIdx  int // index in the original (pre-group) chatMessages slice
	TurnIdx int // 0-based turn number (0 = after first turn)
}

// turnSeparatorInfos returns the line position of each turn separator
// (MsgSystem with TurnModel != "") in the string produced by buildRenderedChat.
// The positions are stable: streaming content is always appended after the
// committed messages, so these indices are valid for the full allLines array.
func turnSeparatorInfos(messages []ChatMessage, s Styles, width int) []TurnSepInfo {
	grouped := groupFileOperations(messages)

	// Map original message indices for turn separators.
	var origSepIdxs []int
	for i, msg := range messages {
		if msg.Type == MsgSystem && msg.TurnModel != "" {
			origSepIdxs = append(origSepIdxs, i)
		}
	}

	lineIdx := 0
	origSepCursor := 0
	var result []TurnSepInfo

	for i, msg := range grouped {
		if msg.Rendered == "" {
			continue
		}

		var written string
		if msg.IsGrouped && msg.GroupIndex > 0 {
			written = renderGroupedItem(msg, s, width)
			isLastInGroup := i+1 >= len(grouped) ||
				!grouped[i+1].IsGrouped ||
				grouped[i+1].GroupIndex == 0
			if isLastInGroup {
				written += "\n"
			}
		} else {
			written = msg.Rendered
		}

		if msg.Type == MsgSystem && msg.TurnModel != "" {
			if origSepCursor < len(origSepIdxs) {
				result = append(result, TurnSepInfo{
					LineIdx: lineIdx,
					MsgIdx:  origSepIdxs[origSepCursor],
					TurnIdx: origSepCursor,
				})
				origSepCursor++
			}
		}

		lineIdx += strings.Count(written, "\n")
	}

	return result
}

// countTurnSeparators returns the number of turn separators (MsgSystem with
// TurnModel set) currently in messages. Used to assign the next 1-based turn
// number when a turn ends.
func countTurnSeparators(messages []ChatMessage) int {
	n := 0
	for _, msg := range messages {
		if msg.Type == MsgSystem && msg.TurnModel != "" {
			n++
		}
	}
	return n
}

// renderTurnInfo renders a turn-end separator line. The left zone keeps the
// model, elapsed time, and cost; the right zone shows the turn number and the
// actions available from this point. Dashes fill the gap between them. The
// right zone degrades gracefully (and is dropped entirely) on narrow widths.
// width is the total content width (mdRenderer.width + 4). turnNum is 1-based.
func renderTurnInfo(model string, elapsed time.Duration, cost float64, turnNum int, width int, s Styles) ChatMessage {
	dimStyle := lipgloss.NewStyle().Foreground(s.ColorDimGray)

	secs := int(elapsed.Seconds())
	info := fmt.Sprintf("◇ %s · %ds · $%.2f ", formatModelName(model), secs, cost)
	infoRendered := dimStyle.Render(info)

	// Content is inside a bordered viewport: width - 2 (border) - 2 (padding) - 2 (indent)
	contentWidth := width - 6
	leftVisual := lipgloss.Width(info)

	// Right-zone candidates, longest first; pick the widest that still leaves
	// room for at least one dash of separation.
	right := ""
	if turnNum > 0 {
		candidates := []string{
			fmt.Sprintf(" Turn #%d · From here: /fork /trim /copy", turnNum),
			fmt.Sprintf(" Turn #%d · /fork /trim /copy", turnNum),
			fmt.Sprintf(" Turn #%d", turnNum),
		}
		for _, c := range candidates {
			// Each /command badge is padded with a space on both sides, adding
			// 2 cells per command beyond the plain text width.
			padded := lipgloss.Width(c) + 2*strings.Count(c, "/")
			if leftVisual+padded+1 <= contentWidth {
				right = c
				break
			}
		}
	}

	rightRendered := ""
	rightVisual := 0
	if right != "" {
		cmdStyle := lipgloss.NewStyle().Background(colorSecondary).Foreground(lipgloss.Color("0")).Bold(true)
		parts := strings.Split(right, " ")
		for i, p := range parts {
			if strings.HasPrefix(p, "/") {
				parts[i] = cmdStyle.Render(" " + p + " ")
			} else {
				parts[i] = dimStyle.Render(p)
			}
		}
		rightRendered = strings.Join(parts, " ")
		rightVisual = lipgloss.Width(rightRendered)
	}

	dashCount := contentWidth - leftVisual - rightVisual
	if dashCount < 1 {
		dashCount = 1
	}
	dashes := dimStyle.Render(strings.Repeat("─", dashCount))

	rendered := "  " + infoRendered + dashes + rightRendered + "\n"
	return ChatMessage{
		Type:        MsgSystem,
		Text:        info,
		Rendered:    rendered,
		TurnModel:   model,
		TurnElapsed: elapsed,
		TurnCost:    cost,
		TurnNum:     turnNum,
	}
}
