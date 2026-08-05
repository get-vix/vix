package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/daemon/jobs"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

func TestCountEndTurns(t *testing.T) {
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("u0")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "read_file", map[string]any{"path": "/x"})), // tool turn: not counted
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "contents", false)),
		llm.NewAssistantMessage(llm.NewTextBlock("a0")), // end turn 1
		llm.NewUserMessage(llm.NewTextBlock("u1")),
		llm.NewAssistantMessage(llm.NewTextBlock("a1")), // end turn 2
	}
	if got := countEndTurns(msgs); got != 2 {
		t.Errorf("countEndTurns = %d, want 2", got)
	}
	if got := countEndTurns(nil); got != 0 {
		t.Errorf("countEndTurns(nil) = %d, want 0", got)
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Fixing the heartbeat scheduler", "Fixing the heartbeat scheduler"},
		{`"Quoted title"`, "Quoted title"},
		{"  Title line\nsecond line", "Title line"},
		{"", ""},
		{"\n\n", ""},
		{strings.Repeat("x", 150), strings.Repeat("x", 100)},
	}
	for _, c := range cases {
		if got := sanitizeTitle(c.in); got != c.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJobRunTitle(t *testing.T) {
	ts := time.Date(2026, 6, 12, 9, 30, 0, 0, time.UTC)
	spec := jobs.Spec{ID: "heartbeat", Name: "Heartbeat"}
	if got := jobRunTitle(spec, ts); got != "Heartbeat - 06/12/2026 9:30 AM" {
		t.Errorf("jobRunTitle = %q", got)
	}
	spec.Name = ""
	if got := jobRunTitle(spec, ts); got != "heartbeat - 06/12/2026 9:30 AM" {
		t.Errorf("jobRunTitle (no name) = %q", got)
	}
}

func TestIssuePlanTitle(t *testing.T) {
	spec := jobs.Spec{ID: "plan-github-issues-get-vix-vix", Name: "Plan GitHub issues (get-vix/vix)"}
	cases := []struct {
		name      string
		finalText string
		want      string
		ok        bool
	}{
		{
			name:      "issue header",
			finalText: "Hi, I investigated issue #29 — ANTHROPIC_BASE_URL not resolved from .env files — on GitHub. Here are my findings:\n\nhttps://github.com/get-vix/vix/issues/29",
			want:      "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files",
			ok:        true,
		},
		{
			name:      "header preceded by an H1 title line",
			finalText: "# [Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files\n\nHi, I investigated issue #29 — ANTHROPIC_BASE_URL not resolved from .env files — on GitHub. Here are my findings:",
			want:      "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files",
			ok:        true,
		},
		{
			name:      "pull request header, preceded by other streamed text",
			finalText: "Note: the GitHub CLI wasn't available.\nHi, I investigated pull request #7 — Fix the thing — on GitHub. Here are my findings:",
			want:      "[Plan GitHub issues (get-vix/vix)] Addressing pull request #7 — Fix the thing",
			ok:        true,
		},
		{
			name:      "item title containing dashes survives",
			finalText: "Hi, I investigated issue #3 — feat: add a — b — c support — on GitHub. Here are my findings:",
			want:      "[Plan GitHub issues (get-vix/vix)] Addressing issue #3 — feat: add a — b — c support",
			ok:        true,
		},
		{
			name:      "no header (nothing new to plan) falls back",
			finalText: "Every open item is already recorded — there is nothing new to plan.",
			ok:        false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := issuePlanTitle(spec, c.finalText)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Errorf("title = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGitHubItemTitle(t *testing.T) {
	cases := []struct {
		name      string
		spec      jobs.Spec
		finalText string
		want      string
		ok        bool
	}{
		{
			name:      "issue header, short title kept whole",
			spec:      jobs.Spec{ID: "triage-github-issues-get-vix-vix", Name: "Triage GitHub issues (get-vix/vix)"},
			finalText: "# [Triage GitHub issues (get-vix/vix)] Triaging issue #53: Fix the flaky retry\n\nHi, I looked at issue #53: Fix the flaky retry. Here is my triage:",
			want:      "[get-vix/vix] Triage GitHub issues #53 - Fix the flaky retry",
			ok:        true,
		},
		{
			name:      "issue header, long title trimmed to six words with ellipsis",
			spec:      jobs.Spec{ID: "triage-github-issues-get-vix-vix", Name: "Triage GitHub issues (get-vix/vix)"},
			finalText: "# [Triage GitHub issues (get-vix/vix)] Triaging issue #7: Add a configurable backoff to the retry loop please",
			want:      "[get-vix/vix] Triage GitHub issues #7 - Add a configurable backoff to the…",
			ok:        true,
		},
		{
			name:      "pull request header",
			spec:      jobs.Spec{ID: "review-github-prs-get-vix-vix", Name: "Review GitHub PRs (get-vix/vix)"},
			finalText: "# [Review GitHub PRs (get-vix/vix)] Reviewing pull request #42: Bump the mock server timeout",
			want:      "[get-vix/vix] Review GitHub PRs #42 - Bump the mock server timeout",
			ok:        true,
		},
		{
			name:      "job name without a repo suffix drops the bracket prefix",
			spec:      jobs.Spec{ID: "triage", Name: "Triage GitHub issues"},
			finalText: "# Triaging issue #9: Something broke",
			want:      "Triage GitHub issues #9 - Something broke",
			ok:        true,
		},
		{
			name:      "no header falls back",
			spec:      jobs.Spec{ID: "triage-github-issues-get-vix-vix", Name: "Triage GitHub issues (get-vix/vix)"},
			finalText: "Nothing new to triage right now.",
			ok:        false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := githubItemTitle(c.spec, c.finalText)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Errorf("title = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFirst6Words(t *testing.T) {
	cases := []struct{ in, want string }{
		{"one two three", "one two three"},
		{"one two three four five six", "one two three four five six"},
		{"one two three four five six seven", "one two three four five six…"},
		{"  spaced   out   words  here ", "spaced out words here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := first6Words(c.in); got != c.want {
			t.Errorf("first6Words(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTitleTranscriptSkipsToolBlocksAndCaps(t *testing.T) {
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("question")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "bash", map[string]any{"command": "ls"})),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "noise", false)),
		llm.NewAssistantMessage(llm.NewTextBlock("answer")),
	}
	got := titleTranscript(msgs)
	if !strings.Contains(got, "User: question") || !strings.Contains(got, "Assistant: answer") {
		t.Errorf("transcript missing text blocks: %q", got)
	}
	if strings.Contains(got, "noise") || strings.Contains(got, "bash") {
		t.Errorf("transcript leaked tool blocks: %q", got)
	}

	long := []llm.MessageParam{llm.NewUserMessage(llm.NewTextBlock(strings.Repeat("a", 2*titleTranscriptBudget)))}
	if n := len(titleTranscript(long)); n > titleTranscriptBudget+16 {
		t.Errorf("transcript not capped: %d bytes", n)
	}
}

// TestMaybeGenerateTitle exercises the async pass end to end against a fake
// LLM: qualifying thread → title set, persisted state updated, and
// event.title_updated emitted.
func TestMaybeGenerateTitle(t *testing.T) {
	fake := &fakeCompactionLLM{summary: `"Refactoring the thread store"`}
	s, events := newCompactionTestThread(t, fake)
	s.endTurnCount = titleEndTurnThreshold

	s.maybeGenerateTitle()

	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type != "event.title_updated" {
				continue
			}
			tu := ev.Data.(protocol.EventTitleUpdated)
			if tu.Title != "Refactoring the thread store" {
				t.Errorf("event title = %q", tu.Title)
			}
			s.mu.Lock()
			got := s.title
			s.mu.Unlock()
			if got != "Refactoring the thread store" {
				t.Errorf("thread title = %q", got)
			}
			// The one-shot call must be tool-free.
			if len(fake.gotMsgs) != 1 || fake.gotMsgs[0].Role != llm.RoleUser {
				t.Errorf("unexpected request shape: %+v", fake.gotMsgs)
			}
			if !strings.Contains(fake.gotMsgs[0].Content[0].Text, "User: u0") {
				t.Errorf("prompt missing transcript: %q", fake.gotMsgs[0].Content[0].Text)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for event.title_updated")
		}
	}
}

func TestMaybeGenerateTitleSkips(t *testing.T) {
	cases := []struct {
		name string
		prep func(s *Thread)
	}{
		{"below threshold", func(s *Thread) { s.endTurnCount = titleEndTurnThreshold - 1 }},
		{"vix origin", func(s *Thread) { s.endTurnCount = titleEndTurnThreshold; s.origin = "vix" }},
		{"already titled", func(s *Thread) { s.endTurnCount = titleEndTurnThreshold; s.title = "set" }},
		{"in flight", func(s *Thread) { s.endTurnCount = titleEndTurnThreshold; s.titleGenInFlight = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeCompactionLLM{summary: "nope"}
			s, _ := newCompactionTestThread(t, fake)
			c.prep(s)
			s.maybeGenerateTitle()
			time.Sleep(20 * time.Millisecond)
			if fake.callCount != 0 {
				t.Errorf("LLM called %d times, want 0", fake.callCount)
			}
		})
	}
}
