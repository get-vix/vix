package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/update"
)

// TabKind identifies the type of a tab.
type TabKind int

const (
	TabKindThreads  TabKind = iota // threads list overview
	TabKindChat                    // chat display for the selected thread
	TabKindModels                  // model + authentication management
	TabKindMcp                     // configured MCP servers
	TabKindJobs                    // scheduled jobs + lifecycle triggers
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
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %dh", days, h)
}

// waitingBadge is the "Waiting for input" styled tag shown on threads that need user attention.
var waitingBadge = lipgloss.NewStyle().Background(colorSecondary).Foreground(lipgloss.Color("0")).Bold(true).Render(" Waiting for input ")

// threadGroupHeaderStyle styles the "User-initiated" / "Vix-initiated" group
// headers in the Threads tab (and the "Jobs" / "Triggers" headers in the Jobs
// & Triggers tab): a purple-background title block mirroring the markdown H1
// look (bold cream text on a violet background, one cell of padding each side).
var threadGroupHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("228")).Background(lipgloss.Color("63")).Padding(0, 1)

// threadColumnHeaderStyle styles the column header row ("Thread", "Title",
// "Running") of the Threads tab.
var threadColumnHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))

// threadHeaderRuleStyle styles the horizontal rule drawn below the column
// header row of the Threads tab.
var threadHeaderRuleStyle = lipgloss.NewStyle().Foreground(colorPrimary)

// unreadDotStyle styles the ● indicator for threads with unread messages.
var unreadDotStyle = lipgloss.NewStyle().Foreground(colorSecondary)

// threadRowSelectedStyle highlights the row under the navigation cursor in
// the Threads tab: a dark gray background spanning the row, with
// secondary-colored text. Leading indicators (unread dot, spinner) keep
// their own foreground color on top of it.
var threadRowSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Background(lipgloss.Color("#262626"))

// threadsSpinnerStyle styles the loading spinner shown for threads that are
// actively working. Primary color distinguishes it from the secondary-tinted
// unread dot.
var threadsSpinnerStyle = lipgloss.NewStyle().Foreground(colorPrimary)

// threadDirSubtitleStyle styles the per-directory path subtitle shown above
// each working directory's rows inside the User-initiated group: an italic
// path in the primary color so it reads as a sub-section label under the group
// header.
var threadDirSubtitleStyle = lipgloss.NewStyle().Italic(true).Foreground(colorPrimary)

// abbreviatePath shortens an absolute path for display, replacing the user's
// home-directory prefix with "~". Empty paths render as "(unknown)".
func abbreviatePath(p string) string {
	if strings.TrimSpace(p) == "" {
		return "(unknown)"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(os.PathSeparator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}

// renderThreadsView renders the threads list overview from the single ordered
// display list built by Model.threadListRows (the same list the selection
// helpers index). spinnerFrame is the current loading-spinner glyph (empty when
// the spinner is inactive); it is shown in a busy thread's leading-indicator
// slot in place of the unread dot. selectedRow is the index of the highlighted
// row among the selectable rows (directory headers and thread rows) — chrome
// rows (section headers, column headers, rules) are skipped when counting.
func renderThreadsView(rows []threadListRow, width, height int, s Styles, selectedRow int, spinnerFrame string) string {
	const colThread = 10
	const colRunning = 10

	innerWidth := width - 4 // width outer − 2 border sides − 2 padding sides
	if innerWidth < 0 {
		innerWidth = 0
	}

	// colMessage fills the remaining space: innerWidth minus the two fixed columns,
	// the 6 characters of inter-column padding ("  " × 3 in the header), and the
	// 22-character badge slot ("  " + " Waiting for input ") always reserved so
	// the layout stays stable whether or not any thread needs input.
	const badgeVisible = 22 // len("  ") + len(" Waiting for input ")
	colMessage := innerWidth - colThread - colRunning - 6 - badgeVisible
	if colMessage < 20 {
		colMessage = 20
	}

	header := fmt.Sprintf("  %-*s  %-*s  %-*s%-*s", colThread, "Thread", colMessage, "Title", colRunning, "Running", badgeVisible, "")
	headerRule := "  " + threadHeaderRuleStyle.Render(strings.Repeat("─", colThread+colMessage+colRunning+4))
	// groupHeader renders a group title as a purple-background block (matching
	// the markdown H1 look) followed by a blank line separating it from the
	// table below.
	groupHeader := func(title string) []string {
		return []string{
			"  " + threadGroupHeaderStyle.Render(title),
			"",
		}
	}
	// shortID trims an id to its first segment for the Thread column.
	shortID := func(id string) string {
		if dash := strings.Index(id, "-"); dash >= 0 {
			return id[:dash]
		}
		if len(id) > colThread {
			return id[:colThread]
		}
		return id
	}

	lines := []string{}
	selIdx := 0 // index among selectable rows, compared to selectedRow

	// appendRow styles one thread row from its precomputed columns and state,
	// applying the selected/busy/unread leading indicators.
	appendRow := func(plainCols string, selected, busy, unread bool) {
		switch {
		case selected:
			lead, leadStyle := "  ", threadRowSelectedStyle
			if busy {
				lead = spinnerFrame + " "
				leadStyle = leadStyle.Foreground(colorPrimary)
			} else if unread {
				lead = "● "
				leadStyle = leadStyle.Foreground(colorSecondary)
			}
			lines = append(lines, leadStyle.Render(lead)+threadRowSelectedStyle.Render(plainCols))
		case busy:
			lines = append(lines, threadsSpinnerStyle.Render(spinnerFrame)+" "+plainCols)
		case unread:
			lines = append(lines, unreadDotStyle.Render("●")+" "+plainCols)
		default:
			lines = append(lines, "  "+plainCols)
		}
	}

	// dirHeaderLine renders a foldable directory path line: a ▾ (expanded) or ▸
	// (collapsed, with a hidden-count hint) glyph before the abbreviated path.
	// When selected it takes the same row highlight as a thread row.
	dirHeaderLine := func(dir string, collapsed bool, count int, selected bool) string {
		glyph := "▾"
		label := abbreviatePath(dir)
		if collapsed {
			glyph = "▸"
			label = fmt.Sprintf("%s  (%d)", label, count)
		}
		if selected {
			return threadRowSelectedStyle.Render("  " + glyph + " " + label)
		}
		return "  " + threadDirSubtitleStyle.Render(glyph+" "+label)
	}

	// liveCols formats the three shared columns for a live user thread.
	liveCols := func(sess *ThreadState) string {
		threadCol := "connecting…"
		runningCol := "—"
		if sess.client != nil {
			threadCol = shortID(sess.client.ThreadID())
			if !sess.client.StartedAt().IsZero() {
				runningCol = formatRunningTime(renderSince(sess.client.StartedAt()))
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
			if sess.title != "" {
				rest = sess.title
			} else {
				for _, msg := range sess.chatMessages {
					if msg.Type == MsgUser {
						rest = strings.SplitN(msg.Text, "\n", 2)[0]
						break
					}
				}
			}
			full := prefix + rest
			if len(full) > colMessage {
				full = full[:colMessage-1] + "…"
			}
			msgCol = full
		} else if sess.title != "" {
			line := sess.title
			if len(line) > colMessage {
				line = line[:colMessage-1] + "…"
			}
			msgCol = line
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
		return fmt.Sprintf("%-*s  %-*s  %-*s", colThread, threadCol, colMessage, msgCol, colRunning, runningCol)
	}

	// recordCols formats the columns for a persisted, not-attached user record:
	// its short id, title (or first message fallback), and time since last
	// activity.
	recordCols := func(sum protocol.ThreadSummary) string {
		msg := sum.Title
		if msg == "" {
			msg = sum.FirstMessage
		}
		if msg == "" {
			msg = "—"
		}
		msg = truncateLabel(msg, colMessage)
		if pad := colMessage - lipgloss.Width(msg); pad > 0 {
			msg += strings.Repeat(" ", pad)
		}
		ranCol := "—"
		raw := sum.LastRequestAt
		if raw == "" {
			raw = sum.StartedAt
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			ranCol = formatRunningTime(renderSince(t)) + " ago"
		}
		return fmt.Sprintf("%-*s  %s  %-*s", colThread, shortID(sum.ID), msg, colRunning, ranCol)
	}

	// vixCols formats the three shared columns of a vix-initiated row from its
	// record summary (id, Title, ran ago). A titled record shows the bare title
	// (e.g. the per-item GitHub-plan title), with a ⚠ marker when the run failed;
	// an untitled record (a raw alert) falls back to the "<job> · <status>
	// <first message>" form.
	vixCols := func(sum protocol.ThreadSummary) string {
		var msgCol string
		if sum.Title != "" {
			msgCol = vixRowTitle(sum)
		} else {
			badge := ""
			if sum.Trigger != nil && sum.Trigger.Ref != "" {
				badge = sum.Trigger.Ref
			}
			status := sum.JobStatus
			if status == "" {
				status = "alert"
			}
			msgCol = badge + " · " + status
			if sum.FirstMessage != "" {
				msgCol += "  " + sum.FirstMessage
			}
		}
		// Rune-aware truncate, then pad to the column's display width so the
		// Running column stays aligned even when a wide glyph (⚠) is present.
		msgCol = truncateLabel(msgCol, colMessage)
		if pad := colMessage - lipgloss.Width(msgCol); pad > 0 {
			msgCol += strings.Repeat(" ", pad)
		}

		ranCol := "—"
		if t, err := time.Parse(time.RFC3339, sum.StartedAt); err == nil {
			ranCol = formatRunningTime(renderSince(t)) + " ago"
		}
		return fmt.Sprintf("%-*s  %s  %-*s", colThread, shortID(sum.ID), msgCol, colRunning, ranCol)
	}

	// threadRowFlags derives the leading-indicator state for a thread row from
	// its live thread (busy/needs-input/unread) or its record summary (unread).
	threadRowFlags := func(r threadListRow) (busy, needsInput, unread bool) {
		if r.live != nil {
			sess := r.live
			unread = sess.unreadCount > 0
			busy = spinnerFrame != "" &&
				(sess.agentState == StateStreaming ||
					sess.agentState == StateToolExecuting ||
					sess.agentState == StatePlanExecuting)
			needsInput = sess.agentState == StateConfirmPending || sess.agentState == StateUserQuestion
			return
		}
		return false, false, r.sum.Unread
	}

	for _, r := range rows {
		switch r.kind {
		case rowUserHeader:
			lines = append(lines, groupHeader("User-initiated")...)
			lines = append(lines, threadColumnHeaderStyle.Render(header), headerRule)
		case rowVixHeader:
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, groupHeader("Vix-initiated")...)
			lines = append(lines, threadColumnHeaderStyle.Render(header), headerRule)
		case rowDirHeader:
			lines = append(lines, dirHeaderLine(r.dir, r.collapsed, r.count, selIdx == selectedRow))
			selIdx++
		case rowUserThread:
			busy, needsInput, unread := threadRowFlags(r)
			plainCols := recordCols(r.sum)
			if r.live != nil {
				plainCols = liveCols(r.live)
			}
			badgeSlot := strings.Repeat(" ", badgeVisible)
			if needsInput {
				badgeSlot = "  " + waitingBadge
			}
			appendRow(plainCols+badgeSlot, selIdx == selectedRow, busy, unread)
			selIdx++
		case rowVixThread:
			busy, needsInput, unread := threadRowFlags(r)
			badgeSlot := strings.Repeat(" ", badgeVisible)
			if needsInput {
				badgeSlot = "  " + waitingBadge
			}
			appendRow(vixCols(r.sum)+badgeSlot, selIdx == selectedRow, busy, unread)
			selIdx++
		}
	}

	content := strings.Join(lines, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}

// vixRowTitle returns the Title-column text for a titled vix-initiated row: the
// bare thread title, prefixed with a ⚠ marker when the run failed (error or
// timeout). Callers handle the untitled (raw-alert) fallback separately.
//
// The marker is the plain warning sign U+26A0 (no U+FE0F variation selector):
// lipgloss and terminals agree it is one cell wide, so the padded Title column
// keeps the Running column aligned. The emoji-presentation "⚠️" measures as two
// cells in lipgloss but renders as one in many terminals, which shifts the
// Running column left on flagged rows.
func vixRowTitle(sum protocol.ThreadSummary) string {
	if st := sum.JobStatus; st == "error" || st == "timeout" {
		return "⚠ " + sum.Title
	}
	return sum.Title
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

// jobsTabDocURL is the documentation anchor advertised in the Jobs & Triggers
// tab header so users can jump to the guide.
const jobsTabDocURL = "https://getvix.dev/docs#guide-jobs"

// nextRunLabel renders an RFC3339 future time as "in 12m" relative to now, or
// "—" when empty/past/unparseable.
func nextRunLabel(rfc string) string {
	if rfc == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return "—"
	}
	until := time.Until(t)
	if until <= 0 {
		return "due"
	}
	return "in " + formatRunningTime(until)
}

// agoLabel renders an RFC3339 past time as "3m ago", or "never" when empty.
func agoLabel(rfc string) string {
	if rfc == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return "never"
	}
	return formatRunningTime(renderSince(t)) + " ago"
}

// jobsCell truncates s to w display columns and right-pads it so columns stay
// aligned even with wide glyphs.
func jobsCell(s string, w int) string {
	s = truncateLabel(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// enabledBox renders the on/off checkbox shown at the start of each Jobs &
// Triggers row.
func enabledBox(on bool) string {
	if on {
		return "[✓]"
	}
	return "[ ]"
}

// lastStatusLabel renders a last-run/last-fire status, prefixing a ⚠ marker for
// failures so a problem run stands out in the Last column.
func lastStatusLabel(when, status string) string {
	if status == "error" || status == "timeout" || status == "deny" {
		return "⚠ " + when
	}
	return when
}

// jobErrorBadge renders a job's recent-run health as "<errors>/<runs>" (e.g.
// "2/10"), or "—" when the job has no recorded runs yet. Kept plain (no ANSI) so
// it flows through jobsCell's width math unchanged.
func jobErrorBadge(runCount, errCount int) string {
	if runCount == 0 {
		return "—"
	}
	return fmt.Sprintf("%d/%d", errCount, runCount)
}

// renderJobsView renders the Jobs & Triggers tab: a short header (description,
// docs link, prompt example) followed by two grouped tables — scheduled Jobs
// (with a live "running" spinner and next/last run) and lifecycle Triggers
// (hooks, with their last fire). selectedRow indexes jobs first, then hooks.
// spinnerFrame is the loading glyph shown for a job that is currently executing
// (empty when the spinner is inactive); hooks never show a spinner.
func renderJobsView(jobs []protocol.JobSummary, hooks []protocol.HookSummary, width, height int, s Styles, selectedRow int, spinnerFrame string) string {
	innerWidth := width - 4
	if innerWidth < 0 {
		innerWidth = 0
	}

	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	hintStyle := lipgloss.NewStyle().Italic(true).Foreground(colorSecondary)
	linkStyle := lipgloss.NewStyle().Foreground(colorPrimary)

	var rows []string
	rows = append(rows,
		"",
		"  "+descStyle.Render("Here is the list of all the scheduled jobs and triggers that vix runs for you."),
		"  "+descStyle.Render("Press space to enable/disable the selected row."),
		"  "+descStyle.Render("Docs: ")+linkStyle.Render(jobsTabDocURL),
		"",
		"  "+hintStyle.Render(`Tip: ask in chat — "Every weekday at 9am, audit my dependencies and open an issue for anything outdated."`),
		"",
	)

	// Column widths: [box] Name  Schedule/Event  When  Last  Errors. Name flexes.
	const colBox = 3
	const colMid = 22
	const colWhen = 12
	const colLast = 16
	const colErr = 7
	colName := innerWidth - colBox - colMid - colWhen - colLast - colErr - 12
	if colName < 12 {
		colName = 12
	}

	header := fmt.Sprintf("    %-*s  %-*s  %-*s  %-*s  %-*s",
		colName, "Name", colMid, "Schedule / Event", colWhen, "Next", colLast, "Last", colErr, "Errors")
	headerRule := "  " + threadHeaderRuleStyle.Render(strings.Repeat("─", min(colBox+colName+colMid+colWhen+colLast+colErr+10, innerWidth)))

	groupHeader := func(title string) {
		if len(rows) > 0 {
			rows = append(rows, "")
		}
		rows = append(rows,
			"  "+threadGroupHeaderStyle.Render(title),
			"",
			threadColumnHeaderStyle.Render(header),
			headerRule,
		)
	}

	rowIdx := 0
	// appendRow renders one selectable row. running drives the spinner lead
	// (jobs only). plain is the box+columns text.
	appendRow := func(plain string, running bool) {
		switch {
		case rowIdx == selectedRow:
			lead, leadStyle := "  ", threadRowSelectedStyle
			if running {
				lead = spinnerFrame + " "
				leadStyle = leadStyle.Foreground(colorPrimary)
			}
			rows = append(rows, leadStyle.Render(lead)+threadRowSelectedStyle.Render(plain))
		case running:
			rows = append(rows, threadsSpinnerStyle.Render(spinnerFrame)+" "+plain)
		default:
			rows = append(rows, "  "+plain)
		}
		rowIdx++
	}

	// --- Jobs group ---
	groupHeader("Jobs")
	if len(jobs) == 0 {
		rows = append(rows, "  "+descStyle.Italic(true).Render("No scheduled jobs yet."))
	}
	for _, j := range jobs {
		running := j.Running && spinnerFrame != ""
		when := nextRunLabel(j.NextRunAt)
		if j.Running {
			when = "running"
		} else if !j.Enabled {
			when = "off"
		}
		last := lastStatusLabel(agoLabel(j.LastRunAt), j.LastStatus)
		plain := enabledBox(j.Enabled) + " " +
			jobsCell(j.Name, colName) + "  " +
			jobsCell(j.Schedule, colMid) + "  " +
			jobsCell(when, colWhen) + "  " +
			jobsCell(last, colLast) + "  " +
			jobsCell(jobErrorBadge(j.RecentRunCount, j.RecentErrorCount), colErr)
		appendRow(plain, running)
	}

	// --- Triggers (hooks) group ---
	groupHeader("Triggers")
	if len(hooks) == 0 {
		rows = append(rows, "  "+descStyle.Italic(true).Render("No lifecycle hooks yet."))
	}
	for _, h := range hooks {
		event := h.Event
		if h.Matcher != "" && h.Matcher != "*" {
			event += " · " + h.Matcher
		}
		last := lastStatusLabel(agoLabel(h.LastFiredAt), h.LastStatus)
		plain := enabledBox(h.Enabled) + " " +
			jobsCell(h.Name, colName) + "  " +
			jobsCell(event, colMid) + "  " +
			jobsCell("—", colWhen) + "  " +
			jobsCell(last, colLast) + "  " +
			jobsCell("—", colErr)
		appendRow(plain, false) // hooks never show a running spinner
	}

	content := strings.Join(rows, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}

// settingsItem identifies a selectable row in the Settings tab. The order here
// is the render order and the cursor index space (0..settingsItemCount-1).
type settingsItem int

const (
	settingUpdateAction settingsItem = iota
	settingUpdateCheck
	settingShowThinking
	settingReadAgentsMD
	settingReadClaudeMD
	settingTelemetry
	settingCompactionAuto
	settingCompactionThreshold
	settingClosedRetention
	settingsItemCount
)

// settingsState carries the current values shown in the Settings tab plus the
// cursor position. Values are read from ~/.vix/settings.json at render time.
type settingsState struct {
	cursor              int
	showThinking        bool
	readAgentsMD        bool
	readClaudeMD        bool
	telemetry           bool
	compactionAuto      bool
	compactionThreshold float64
	closedRetentionMins int
	updateCheck         bool
	updateCurrent       string
	updateLatest        string // newer release tag, "" when up-to-date/unknown
	updateMethod        string
	updateInstalled     bool
	updateErr           string
	// grepBackend/globBackend are display-ready labels for the resolved search
	// tools (e.g. "rg", or "builtin  (fd configured — not found in PATH)").
	// Empty until the daemon reports them via event.tool_backends.
	grepBackend string
	globBackend string
}

// toggleSetting flips (or, for the threshold row, leaves unchanged) the setting
// at the given row and persists it to ~/.vix/settings.json.
func (m *Model) toggleSetting(item settingsItem) {
	switch item {
	case settingShowThinking:
		v := !config.ShowThinking()
		if sess := m.currentThread(); sess != nil {
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
	case settingTelemetry:
		_ = config.SetTelemetryEnabled(!config.TelemetryEnabled())
	case settingCompactionAuto:
		_ = config.SetCompactionAuto(!config.CompactionAuto())
	case settingUpdateCheck:
		_ = config.SetUpdateCheckEnabled(!config.UpdateCheckEnabled())
	case settingCompactionThreshold:
		// Threshold is adjusted with ←/→, not toggled.
	case settingClosedRetention:
		// Retention is adjusted with ←/→, not toggled.
	case settingUpdateAction:
		// Handled in the Settings key handler (model.go), not here — it triggers
		// an install/quit rather than flipping a persisted flag.
	}
}

// handleUpdateAction runs the Settings "Updates" action row. Depending on
// state it starts the install (in the foreground via ExecProcess, so sudo can
// prompt), or — once installed — sends a quit-all so every vix instance and the
// daemon exit and the new binaries take effect on relaunch. Returns nil when
// there is nothing to do (up to date, or manual-only).
func (m *Model) handleUpdateAction() tea.Cmd {
	switch {
	case m.updateInstalled:
		// Intentionally no closeThreadsForQuit here: an update quit-all is a
		// restart, not an exit — threads must stay in open/ and restore on
		// relaunch regardless of the close_all_threads_on_quit preference.
		if sess := m.currentThread(); sess != nil {
			_ = sess.client.SendUpdateQuit()
		}
		return tea.Quit
	case m.updateLatest == "":
		return nil // up to date
	case m.updateMethod == update.MethodUnknown:
		return nil // manual instructions only — nothing to run
	default:
		cmd := update.InstallCommand(m.updateMethod, m.updateLatest)
		if cmd == nil {
			return nil
		}
		m.updateErr = ""
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return updateInstallDoneMsg{err: err}
		})
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

// closedRetentionPresets is the ←/→ ladder for the closed-thread retention
// row, in minutes. "Never" (0) is deliberately not on the ladder — it is only
// settable by editing settings.json by hand.
var closedRetentionPresets = []int{
	60,           // 1 hour
	6 * 60,       // 6 hours
	24 * 60,      // 1 day
	3 * 24 * 60,  // 3 days
	7 * 24 * 60,  // 1 week
	14 * 24 * 60, // 2 weeks
	30 * 24 * 60, // 1 month
}

// retentionLabel renders a retention value (minutes) for display.
func retentionLabel(mins int) string {
	switch {
	case mins <= 0:
		return "Never"
	case mins == 60:
		return "1 hour"
	case mins < 24*60:
		return fmt.Sprintf("%d hours", mins/60)
	case mins == 24*60:
		return "1 day"
	case mins == 7*24*60:
		return "1 week"
	case mins == 14*24*60:
		return "2 weeks"
	case mins == 30*24*60:
		return "1 month"
	case mins%(24*60) == 0:
		return fmt.Sprintf("%d days", mins/(24*60))
	default:
		return fmt.Sprintf("%d mn", mins)
	}
}

// adjustClosedRetention steps the closed-thread retention to the next (dir>0)
// or previous (dir<0) preset. From a non-preset value (including the JSON-only
// "Never"), adjusting steps onto the nearest preset in the requested direction.
func (m *Model) adjustClosedRetention(dir int) {
	cur := config.ClosedThreadRetentionMinutes()
	idx := -1
	for i, p := range closedRetentionPresets {
		if p == cur {
			idx = i
			break
		}
	}
	var next int
	if idx >= 0 {
		i := idx + dir
		if i < 0 || i >= len(closedRetentionPresets) {
			return
		}
		next = closedRetentionPresets[i]
	} else {
		// Off-ladder value: step onto the first preset above (→) or the last
		// preset below (←) the current value. "Never" (0) always lands on the
		// first preset.
		next = closedRetentionPresets[0]
		if dir > 0 {
			for _, p := range closedRetentionPresets {
				if p > cur {
					next = p
					break
				}
			}
		} else if cur > 0 {
			for _, p := range closedRetentionPresets {
				if p < cur {
					next = p
				}
			}
		}
	}
	_ = config.SetClosedThreadRetentionMinutes(next)
}

// updateActionLabel returns the text for the selectable Updates action row,
// reflecting the current upgrade state.
func updateActionLabel(st settingsState) string {
	switch {
	case st.updateErr != "":
		return "⚠ Update failed — Enter to retry"
	case st.updateInstalled:
		return "⏻ Quit all & finish update"
	case st.updateLatest == "":
		return "✓ Up to date"
	case st.updateMethod == "unknown":
		return "Update manually: curl -fsSL https://getvix.dev/install.sh | bash"
	default:
		return "↓ Download & install " + st.updateLatest
	}
}

// backendLabel formats a resolved search-tool backend for the Settings tab.
// When the effective backend differs from what was configured (a PATH
// fallback), it appends a note naming the missing tool so the user understands
// why. Returns "unknown" until the daemon has reported the backends.
func backendLabel(effective, configured string) string {
	if effective == "" {
		return "unknown"
	}
	if configured != "" && configured != effective {
		return fmt.Sprintf("%s  (%s configured — not found in PATH)", effective, configured)
	}
	return effective
}

// renderSettingsView renders the Settings tab content (global preferences).
func renderSettingsView(width, height int, s Styles, st settingsState) string {
	// Body text and section titles are white (matching the Threads/Models
	// tabs); primary marks the cursor row and the separator rules.
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	sectionTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	cursorStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	innerWidth := width - 4
	if innerWidth < 0 {
		innerWidth = 0
	}

	sep := threadHeaderRuleStyle.Width(innerWidth).Render(strings.Repeat("─", innerWidth))

	var lines []string
	idx := 0 // running index of selectable settings, matches settingsItem

	row := func(text string) {
		if idx == st.cursor {
			lines = append(lines, cursorStyle.Width(innerWidth).Render("▸ "+text))
		} else {
			lines = append(lines, textStyle.Width(innerWidth).Render("  "+text))
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
		lines = append(lines, sectionTitleStyle.Width(innerWidth).Render(name), sep)
	}

	// infoRow renders a non-selectable display line (does not advance idx).
	infoRow := func(label, value string) {
		lines = append(lines, textStyle.Width(innerWidth).Render(fmt.Sprintf("  %-16s %s", label, value)))
	}

	updateAvail := st.updateLatest != ""
	secondary := lipgloss.NewStyle().Foreground(colorSecondary)
	secondaryBold := lipgloss.NewStyle().Bold(true).Foreground(colorSecondary)

	// actionRow renders the selectable Updates action. When an update is
	// available it is tinted with the secondary color to mirror the Threads
	// tab's new-activity highlight. Always occupies one cursor slot.
	actionRow := func(text string, highlight bool) {
		switch {
		case idx == st.cursor && highlight:
			lines = append(lines, secondaryBold.Width(innerWidth).Render("▸ "+text))
		case idx == st.cursor:
			lines = append(lines, cursorStyle.Width(innerWidth).Render("▸ "+text))
		case highlight:
			lines = append(lines, secondary.Width(innerWidth).Render("  "+text))
		default:
			lines = append(lines, textStyle.Width(innerWidth).Render("  "+text))
		}
		idx++
	}

	// Updates section — first, so a pending upgrade is the first thing seen. The
	// title is tinted secondary when an update is available.
	updTitle := sectionTitleStyle
	if updateAvail {
		updTitle = secondaryBold
	}
	lines = append(lines, updTitle.Width(innerWidth).Render("Updates"), sep)
	current := st.updateCurrent
	if current == "" {
		current = Version
	}
	infoRow("Current version", current)
	if updateAvail {
		infoRow("Latest version", st.updateLatest+"  ← update available")
	} else {
		infoRow("Latest version", "up to date")
	}
	actionRow(updateActionLabel(st), updateAvail)
	toggleRow("Check for updates daily", st.updateCheck)

	section("Display")
	toggleRow("Show extended thinking", st.showThinking)

	section("Context")
	toggleRow("Read AGENTS.md", st.readAgentsMD)
	toggleRow("Read CLAUDE.md", st.readClaudeMD)

	section("Privacy")
	toggleRow("Send anonymous telemetry", st.telemetry)

	section("Compaction")
	toggleRow("Auto-compaction", st.compactionAuto)
	sliderRow("Threshold       ", st.compactionThreshold)

	section("Threads")
	row(fmt.Sprintf("Closed thread retention  ‹ %s ›", retentionLabel(st.closedRetentionMins)))

	section("Tools")
	infoRow("Grep backend", st.grepBackend)
	infoRow("Glob backend", st.globBackend)

	lines = append(lines, "", textStyle.Italic(true).Width(innerWidth).Render("↑↓ navigate · Enter toggle/select · ←→ adjust"))

	content := strings.Join(lines, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}

// authButton is one actionable control in the Models-tab authentication panel.
// id drives the handler; label is what the user sees.
type authButton struct {
	id    string
	label string
}

// authButtonsFor returns the ordered buttons shown for a single credential
// method, given its stored-credential status. This is the single source of
// truth shared by the renderer and the key handler so navigation indices and
// drawn controls never diverge. Delete appears only when the credential is
// stored; "Make it default" only when stored and not already the default.
func authButtonsFor(ms config.MethodStatus) []authButton {
	setID, delID, defID := "set_key", "del_key", "default_key"
	createLabel, updateLabel, deleteLabel := "Create key", "Update key", "Delete key"
	if ms.OAuth {
		setID, delID, defID = "set_token", "del_token", "default_token"
		createLabel, updateLabel, deleteLabel = "Create token", "Update token", "Delete token"
	}
	if !ms.Stored {
		return []authButton{{setID, createLabel}}
	}
	btns := []authButton{{setID, updateLabel}, {delID, deleteLabel}}
	if !ms.IsDefault {
		btns = append(btns, authButton{defID, "Make it default"})
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
	// Body text on the Models tab is white by design (matches the Threads tab).
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
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
				cells = append(cells, textStyle.Width(cellWidth).Render(""))
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
				rendered = textStyle.Width(cellWidth).Render(label)
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
// clamp agree on how many rows fit. Local providers render one extra line: the
// server reachability status.
func modelsHeaderLines(st config.ProviderAuthStatus, loginStatus string, isLocal bool) int {
	n := 2 // "Credentials" title + separator
	// Each credential method renders a status row plus a buttons row; a method
	// with a stored user-supplied base URL adds one more line.
	for _, ms := range st.Methods {
		n += 2
		if ms.RequiresBaseURL && ms.Stored && ms.BaseURL != "" {
			n++
		}
	}
	if loginStatus != "" {
		n++
	}
	if isLocal {
		n++ // server status line
	}
	// Models section header: blank, "Models:" title (with count), separator,
	// filter line, two help lines, blank.
	n += 7
	return n
}

// modelsGridRows returns how many grid rows fit in a Models-tab viewport of the
// given height for the given auth/login state. Always >= 1.
func modelsGridRows(height int, st config.ProviderAuthStatus, loginStatus string, isLocal bool) int {
	rows := height - modelsViewportChrome - modelsHeaderLines(st, loginStatus, isLocal)
	if rows < 1 {
		rows = 1
	}
	return rows
}

// renderModelsView renders the Models tab: a provider column (split into logged
// in / local / available) on the left, and an authentication panel + model grid
// for the selected provider on the right. Local providers carry a reachability
// dot and their model grid is the live-discovered server list.
func renderModelsView(width, height int, s Styles,
	loggedIn, local, available []string,
	status map[string]config.ProviderAuthStatus,
	localUI map[string]LocalProviderUI,
	providerSel int, focus modelsFocusArea,
	authRow, authBtn, modelSel, modelScroll int,
	modelFilter, activeModel, loginStatus string) string {

	// Body text on the Models tab is white by design (matches the Threads
	// tab); primary marks the focused cursor, secondary marks active/status.
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
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

	// Display (and navigation) order: logged in, then available, then local
	// last — must stay in lockstep with Model.modelsFlat.
	flat := append(append(append([]string{}, loggedIn...), available...), local...)
	provider := ""
	if providerSel >= 0 && providerSel < len(flat) {
		provider = flat[providerSel]
	}
	activeProvider := ProviderOf(activeModel)
	_, providerIsLocal := localUI[provider]
	if !providerIsLocal {
		for _, name := range local {
			if name == provider {
				providerIsLocal = true
				break
			}
		}
	}

	// ---- left: provider column ----
	var leftLines []string
	leftLines = append(leftLines,
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Width(colWidth).Render("Providers"),
		threadHeaderRuleStyle.Width(colWidth).Render(strings.Repeat("─", colWidth)),
	)
	flatIdx := 0
	renderGroup := func(header string, names []string, dots bool) {
		leftLines = append(leftLines, "", textStyle.Bold(true).Underline(true).Width(colWidth).Render(header))
		if len(names) == 0 {
			leftLines = append(leftLines, textStyle.Italic(true).Width(colWidth).Render("  —"))
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
			if dots {
				dot := "○ " // unreachable, or probe not answered yet
				if localUI[name].Reachable {
					dot = "● "
				}
				label = prefix + dot + DisplayNameForProvider(name)
			}
			if name == activeProvider {
				label += " ★"
			}
			switch {
			case isCursor:
				leftLines = append(leftLines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(colWidth).Render(label))
			case isSelected:
				leftLines = append(leftLines, secondaryStyle.Width(colWidth).Render(label))
			default:
				leftLines = append(leftLines, textStyle.Width(colWidth).Render(label))
			}
			flatIdx++
		}
	}
	renderGroup("Logged in:", loggedIn, false)
	renderGroup("Available:", available, false)
	renderGroup("Local:", local, true)

	// ---- right: authentication + models ----
	st := status[provider]
	authActive := focus == modelsFocusAuth
	sep := threadHeaderRuleStyle.Width(rightWidth).Render(strings.Repeat("─", rightWidth))

	authTitle := lipgloss.NewStyle().Bold(true)
	if authActive {
		authTitle = authTitle.Foreground(colorPrimary)
	} else {
		authTitle = authTitle.Foreground(lipgloss.Color("15"))
	}

	var rightLines []string
	rightLines = append(rightLines, authTitle.Render("Credentials"), sep)

	defaultTag := func(isDefault bool) string {
		if isDefault {
			return "   " + secondaryStyle.Render("Default method")
		}
		return ""
	}
	renderButtons := func(row int, btns []authButton) string {
		if len(btns) == 0 {
			return ""
		}
		var cells []string
		for i, b := range btns {
			text := "[ " + b.label + " ]"
			if authActive && authRow == row && authBtn == i {
				cells = append(cells, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(text))
			} else {
				cells = append(cells, textStyle.Render(text))
			}
		}
		return "    " + strings.Join(cells, "  ")
	}

	// One status row + buttons row per credential method, in declared order.
	for row, ms := range st.Methods {
		val := "(empty)"
		if ms.Stored {
			if ms.OAuth {
				val = "active"
			} else {
				val = ms.Prefix + "..."
			}
		}
		rightLines = append(rightLines, ms.Label+": "+val+defaultTag(ms.IsDefault))
		if ms.RequiresBaseURL && ms.Stored && ms.BaseURL != "" {
			rightLines = append(rightLines, textStyle.Render("    ↳ "+ms.BaseURL))
		}
		rightLines = append(rightLines, renderButtons(row, authButtonsFor(ms)))
	}

	if loginStatus != "" {
		rightLines = append(rightLines, secondaryStyle.Render(loginStatus))
	}

	// Server status line for local providers: reachability dot + endpoint.
	// The API key above is optional (proxied servers only) — say so.
	if providerIsLocal {
		ui := localUI[provider]
		var serverLine string
		switch {
		case !ui.Fetched:
			serverLine = textStyle.Render("Server: probing…")
		case ui.Reachable:
			serverLine = "Server: " + secondaryStyle.Render("●") + " " + ui.BaseURL + textStyle.Render(" — running · no API key required")
		default:
			serverLine = "Server: " + textStyle.Render("○ "+ui.BaseURL+" — not reachable")
		}
		rightLines = append(rightLines, serverLine)
	}

	// Models section.
	modelsTitle := lipgloss.NewStyle().Bold(true)
	if focus == modelsFocusModels {
		modelsTitle = modelsTitle.Foreground(colorPrimary)
	} else {
		modelsTitle = modelsTitle.Foreground(lipgloss.Color("15"))
	}

	allModels := DisplayModelsForProvider(provider)
	if providerIsLocal {
		allModels = localUI[provider].Models
	}
	filtered := FilterModels(allModels, modelFilter)

	// Window the filtered list to the rows that fit, keeping the cursor visible.
	gridRows := modelsGridRows(height, st, loginStatus, providerIsLocal)
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
		textStyle.Render(fmt.Sprintf("showing %d of %d", shown, len(filtered)))
	rightLines = append(rightLines, "", titleLine, sep)

	// Filter line — type-to-filter while the grid is focused.
	caret := ""
	if focus == modelsFocusModels {
		caret = "▌"
	}
	var filterLine string
	if modelFilter == "" && focus != modelsFocusModels {
		filterLine = textStyle.Render("Filter: (type while focused to filter)")
	} else {
		filterLine = "Filter: " + secondaryStyle.Render(modelFilter) + caret
	}
	rightLines = append(rightLines,
		filterLine,
		textStyle.Render("Selecting a model updates the default model for chat."),
		textStyle.Render("For workflows see https://getvix.dev/doc#workflows"),
		"",
	)

	selInWindow := modelSel - startIdx
	if selInWindow < 0 || selInWindow >= shown {
		selInWindow = -1
	}
	grid := renderModelGrid(window, rightWidth, focus == modelsFocusModels, selInWindow, activeModel)
	rightLines = append(rightLines, grid...)

	// Empty-state hints for local providers (rendered in the grid's space).
	if providerIsLocal && len(filtered) == 0 && modelFilter == "" {
		ui := localUI[provider]
		hint := ""
		switch {
		case ui.Fetched && !ui.Reachable && provider == "ollama":
			hint = "  server not reachable — start it with: ollama serve"
		case ui.Fetched && !ui.Reachable:
			hint = "  server not reachable — start it with: llama-server -m <model.gguf>"
		case ui.Fetched && provider == "ollama":
			hint = "  no models installed — try: ollama pull qwen3"
		case ui.Fetched:
			hint = "  no models reported by the server"
		}
		if hint != "" {
			rightLines = append(rightLines, textStyle.Italic(true).Width(rightWidth).Render(hint))
		}
	}

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
			rightLines = append(rightLines, textStyle.Italic(true).Width(rightWidth).Render("  (custom: "+activeModel+")"))
		}
	}

	leftCol := lipgloss.NewStyle().Width(colWidth).Render(strings.Join(leftLines, "\n"))
	rightCol := lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(rightLines, "\n"))
	gap := lipgloss.NewStyle().Width(2).Render("")
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, gap, rightCol)

	return s.ViewportFocusedStyle.Width(width).Height(height).Render(body)
}

// renderTabBar renders the two-tab bar: Threads | Chat.
// alertActive is true when some background thread (one the user isn't currently
// viewing) is waiting for user input; the Threads tab title then blinks
// (alertBlinkOn is the current blink phase). A thread the user is already
// looking at shows its question on screen, so it's excluded and doesn't blink.
// When no alert is active but unseen is true (a message arrived while the
// Threads tab was not focused), the Threads title is tinted secondary
// statically (no blink).
func renderTabBar(activeTab TabKind, width int, s Styles, viewportFocused bool, alertActive bool, alertBlinkOn bool, unseen bool, updateAvailable bool) string {
	type tabDef struct {
		label string
		kind  TabKind
	}
	defs := []tabDef{
		{" Threads [F1] ", TabKindThreads},
		{" Workspace [F2] ", TabKindChat},
		{" Models [F3] ", TabKindModels},
		{" MCP [F4] ", TabKindMcp},
		{" Jobs & Triggers [F5] ", TabKindJobs},
		{" Settings [F6] ", TabKindSettings},
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
		case d.kind == TabKindThreads && alertActive:
			// Waiting for input: blink between the alert color and inactive.
			if alertBlinkOn {
				textStyle = s.TabAlertStyle
			} else {
				textStyle = s.TabInactiveStyle
			}
		case d.kind == TabKindThreads && unseen:
			// Unseen activity: static secondary tint (superseded by the blink above).
			textStyle = s.TabAlertStyle
		case d.kind == TabKindSettings && updateAvailable:
			// A newer release is available: static secondary tint, mirroring the
			// Threads tab's unseen-activity highlight.
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
