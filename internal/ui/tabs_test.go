package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/get-vix/vix/internal/protocol"
)

// TestVixRowTitle covers the bare-title Threads-tab rendering: a clean title
// for ok/skipped runs and a ⚠ marker for failures. The job-id/status prefix is
// gone for titled rows.
func TestVixRowTitle(t *testing.T) {
	title := "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — env bug"
	cases := []struct {
		name   string
		status string
		want   string
	}{
		{"ok run is bare title", "ok", title},
		{"skipped run is bare title", "skipped", title},
		{"empty status is bare title", "", title},
		{"error run is flagged", "error", "⚠ " + title},
		{"timeout run is flagged", "timeout", "⚠ " + title},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sum := protocol.ThreadSummary{Title: title, JobStatus: c.status}
			got := vixRowTitle(sum)
			if got != c.want {
				t.Errorf("vixRowTitle(status=%q) = %q, want %q", c.status, got, c.want)
			}
			// The job-id badge and "· ok" prefix never appear for a titled row.
			if strings.Contains(got, " · ") {
				t.Errorf("titled row should not contain the job-id/status prefix: %q", got)
			}
		})
	}
}

// TestVixRowTitleMarkerWidth guards the alignment fix: the warning marker must
// measure exactly two display cells ("⚠" + " ") in lipgloss so the padded Title
// column keeps the Running column aligned across flagged and clean rows. The
// emoji-presentation "⚠️" (with U+FE0F) measures three here, which regressed the
// layout, so assert the variation selector is absent.
func TestVixRowTitleMarkerWidth(t *testing.T) {
	flagged := vixRowTitle(protocol.ThreadSummary{Title: "x", JobStatus: "error"})
	clean := vixRowTitle(protocol.ThreadSummary{Title: "x", JobStatus: "ok"})

	if strings.ContainsRune(flagged, '\uFE0F') {
		t.Fatalf("marker must not contain the U+FE0F variation selector: %q", flagged)
	}
	if got := lipgloss.Width(flagged) - lipgloss.Width(clean); got != 2 {
		t.Errorf("marker width = %d cells, want 2", got)
	}
}

// TestRenderJobsView covers the Jobs & Triggers tab: the header blurb (with the
// docs link and prompt example), the two grouped sections, the enabled
// checkboxes, and the running spinner shown for jobs only.
func TestRenderJobsView(t *testing.T) {
	s := NewStyles(true)
	jobs := []protocol.JobSummary{
		{ID: "alpha", Name: "Alpha", Enabled: true, Schedule: "@every 1m", NextRunAt: "2999-01-01T00:00:00Z"},
		{ID: "beta", Name: "Beta", Enabled: false, Schedule: "@daily"},
		{ID: "gamma", Name: "Gamma", Enabled: true, Running: true},
	}
	hooks := []protocol.HookSummary{
		{ID: "guard", Name: "Guard", Enabled: true, Event: "PreToolUse"},
	}
	out := renderJobsView(jobs, hooks, 120, 40, s, 0, "⠙")

	for _, want := range []string{
		jobsTabDocURL,          // docs link
		"Every weekday at 9am", // prompt example
		"Jobs", "Triggers",     // group headers
		"Alpha", "Beta", "Gamma", "Guard",
		"[✓]", "[ ]", // enabled + disabled checkboxes
		"PreToolUse",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderJobsView output missing %q\n---\n%s", want, out)
		}
	}

	// The running job (gamma) drives the spinner glyph; hooks never do.
	if !strings.Contains(out, "⠙") {
		t.Errorf("running job should render the spinner glyph\n%s", out)
	}
}

func TestTruncateLabel(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"short", 10, "short"},
		{"exactfit!!", 10, "exactfit!!"},
		{"toolongforcell", 10, "toolongfo…"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
		{"abc", -3, ""},
		{"héllo wörld", 6, "héllo…"},
	}
	for _, c := range cases {
		if got := truncateLabel(c.in, c.w); got != c.want {
			t.Errorf("truncateLabel(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}

// TestBackendLabel covers the three display states: unreported (empty →
// "unknown"), a plain effective backend, and the PATH-fallback annotation shown
// when the configured backend differs from the one that resolved.
func TestBackendLabel(t *testing.T) {
	cases := []struct {
		name       string
		effective  string
		configured string
		want       string
	}{
		{"unreported", "", "", "unknown"},
		{"unreported ignores configured", "", "fd", "unknown"},
		{"plain effective", "rg", "rg", "rg"},
		{"default unset treated as no-fallback", "builtin", "builtin", "builtin"},
		{"fallback annotated", "builtin", "fd", "builtin  (fd configured — not found in PATH)"},
		{"grep fallback annotated", "grep", "rg", "grep  (rg configured — not found in PATH)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := backendLabel(c.effective, c.configured); got != c.want {
				t.Errorf("backendLabel(%q, %q) = %q, want %q", c.effective, c.configured, got, c.want)
			}
		})
	}
}

// TestRenderSettingsViewToolsSection asserts the Settings tab shows the Tools
// section with both resolved backends, including the fallback annotation.
func TestRenderSettingsViewToolsSection(t *testing.T) {
	s := NewStyles(true)
	st := settingsState{
		grepBackend: backendLabel("rg", "rg"),
		globBackend: backendLabel("builtin", "fd"),
	}
	out := renderSettingsView(120, 40, s, st)

	for _, want := range []string{
		"Tools",
		"Grep backend",
		"Glob backend",
		"rg",
		"builtin  (fd configured — not found in PATH)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderSettingsView output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderSettingsViewCursorUnaffectedByTools guards that the non-selectable
// Tools info rows don't shift the cursor index space: with the cursor on the
// last selectable row (closed-thread retention), the "▸" marker must land on
// that row, not be swallowed by the Tools section that renders after it.
func TestRenderSettingsViewCursorUnaffectedByTools(t *testing.T) {
	s := NewStyles(true)
	st := settingsState{
		cursor:              int(settingClosedRetention),
		closedRetentionMins: 60,
		grepBackend:         "rg",
		globBackend:         "fd",
	}
	out := renderSettingsView(120, 40, s, st)

	var cursorLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "▸") {
			cursorLine = line
			break
		}
	}
	if !strings.Contains(cursorLine, "Closed thread retention") {
		t.Errorf("cursor marker should be on the retention row, got %q", cursorLine)
	}
}
