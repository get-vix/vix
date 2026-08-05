package daemon

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/daemon/prompt"
	"github.com/get-vix/vix/internal/protocol"
)

// titleEndTurnThreshold is how many completed chat turns (end_turn stops) a
// thread needs before the auto-titling pass runs.
const titleEndTurnThreshold = 3

// titleMaxLen caps the generated title length, in runes.
const titleMaxLen = 100

// titleTranscriptBudget caps how much conversation text is sent to the
// summarization call.
const titleTranscriptBudget = 4000

// fallbackTitlePrompt is used when prompts/summarization.md is missing from
// every config layer (e.g. a stripped-down config dir).
const fallbackTitlePrompt = "Summarize the following conversation into a short descriptive title. " +
	"Reply with the title only — no quotes, no explanations, at most 100 characters.\n\n$(transcript)"

// countEndTurns derives the number of completed chat turns from history: each
// assistant message without tool_use blocks corresponds to one end_turn stop.
func countEndTurns(msgs []llm.MessageParam) int {
	n := 0
	for _, m := range msgs {
		if m.Role != llm.RoleAssistant {
			continue
		}
		hasToolUse := false
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				hasToolUse = true
				break
			}
		}
		if !hasToolUse {
			n++
		}
	}
	return n
}

// maybeGenerateTitle kicks off the async auto-titling pass when the thread
// qualifies: user-started, still untitled, at least titleEndTurnThreshold
// completed turns, a usable LLM client, and no pass already running. Called by
// the turn loop after persist; never blocks the turn.
func (s *Thread) maybeGenerateTitle() {
	if s.origin == "vix" || s.llm == nil || s.endTurnCount < titleEndTurnThreshold {
		return
	}
	s.mu.Lock()
	if s.title != "" || s.titleGenInFlight {
		s.mu.Unlock()
		return
	}
	s.titleGenInFlight = true
	transcript := titleTranscript(s.messages)
	s.mu.Unlock()

	go s.generateTitle(transcript)
}

// generateTitle runs the one-shot, tool-free summarization call and publishes
// the result (record, thread event, threads-list broadcast). Failures are
// logged and leave the title empty — the next completed turn retries.
func (s *Thread) generateTitle(transcript string) {
	defer func() {
		s.mu.Lock()
		s.titleGenInFlight = false
		s.mu.Unlock()
	}()

	promptText := s.loadTitlePrompt(transcript)
	msgs := []llm.MessageParam{llm.NewUserMessage(llm.NewTextBlock(promptText))}
	msg, _, err := s.llm.StreamMessage(s.ctx, nil, msgs, nil, func(string) {}, func(string) {})
	if err != nil {
		LogError("title generation for thread %s: %v", s.id, err)
		return
	}
	title := sanitizeTitle(msg.TextContent)
	if title == "" {
		LogError("title generation for thread %s: empty result", s.id)
		return
	}

	s.mu.Lock()
	if s.title != "" { // lost a race with another setter; keep the first
		s.mu.Unlock()
		return
	}
	s.title = title
	s.mu.Unlock()

	s.persist()
	s.emit("event.title_updated", protocol.EventTitleUpdated{Title: title})
	if s.server != nil {
		s.server.broadcastThreadsChanged()
	}
}

// loadTitlePrompt resolves prompts/summarization.md across the config layers
// (highest precedence first) and substitutes $(transcript). Falls back to a
// built-in prompt when the file is missing everywhere.
func (s *Thread) loadTitlePrompt(transcript string) string {
	vars := map[string]string{"transcript": transcript}
	for _, dir := range s.searchDirsSlice() {
		path := filepath.Join(dir, "prompts", "summarization.md")
		if _, err := os.Stat(path); err == nil {
			return prompt.GetLoader().Load(path, vars, s.searchDirs(), nil)
		}
	}
	return prompt.GetLoader().Resolve(fallbackTitlePrompt, vars, s.searchDirs(), nil)
}

// titleTranscript renders the conversation's text blocks into a compact
// "User:/Assistant:" transcript capped at titleTranscriptBudget bytes.
// Tool blocks and thinking are skipped. Caller holds s.mu.
func titleTranscript(msgs []llm.MessageParam) string {
	var sb strings.Builder
	for _, m := range msgs {
		role := "User"
		if m.Role == llm.RoleAssistant {
			role = "Assistant"
		}
		for _, b := range m.Content {
			if b.Type != llm.BlockText || strings.TrimSpace(b.Text) == "" {
				continue
			}
			remaining := titleTranscriptBudget - sb.Len()
			if remaining <= 0 {
				return sb.String()
			}
			text := strings.TrimSpace(b.Text)
			if len(text) > remaining {
				text = strings.ToValidUTF8(text[:remaining], "")
			}
			sb.WriteString(role)
			sb.WriteString(": ")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		}
	}
	return sb.String()
}

// sanitizeTitle reduces a model reply to a single clean title line: first
// non-empty line, stripped of wrapping quotes, capped at titleMaxLen runes.
func sanitizeTitle(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	t = strings.Trim(t, `"'`)
	t = strings.TrimSpace(t)
	if r := []rune(t); len(r) > titleMaxLen {
		t = string(r[:titleMaxLen])
	}
	return t
}
