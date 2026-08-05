package daemon

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// MessageThreadSpec is the public, stable schema for creating a Vix-initiated
// message thread — a one-message conversation that lands in the Threads tab
// under "Vix-initiated". It is the payload of the message.create RPC and the
// `vix thread create` CLI. Deliberately a small surface (not the internal
// threadRecord) so the on-disk format can evolve independently.
type MessageThreadSpec struct {
	// Message is the assistant text shown to the user. Required unless
	// MessageFile is set (exactly one of the two).
	Message string `json:"message"`
	// MessageFile is an absolute path whose contents become the message text.
	// Set this instead of Message to avoid encoding multi-line content in JSON
	// (e.g. a hook delivering markdown). The file must exist and be non-empty.
	MessageFile string `json:"message_file,omitempty"`
	// CWD is the project the conversation is scoped to. Required; must be an
	// existing directory. The thread surfaces in any TUI launched there.
	CWD string `json:"cwd"`
	// Title is the Threads-tab display title. Optional; empty falls back to
	// the first message.
	Title string `json:"title,omitempty"`
	// Unread controls the unread dot. Optional; defaults to true.
	Unread *bool `json:"unread,omitempty"`
	// Trigger records provenance (e.g. the hook that created it). Optional.
	Trigger *protocol.TriggerInfo `json:"trigger,omitempty"`
}

// createMessageThread materialises a Vix-initiated message thread from spec
// and persists it to open/, returning the new thread id. Origin is always
// "vix" (so it groups under Vix-initiated and never re-triggers hooks). This is
// the single implementation behind writeJobAlertThread, the message.create
// RPC, and `vix thread create`.
func (s *Server) createMessageThread(spec MessageThreadSpec) (string, error) {
	message := spec.Message
	if spec.MessageFile != "" {
		if strings.TrimSpace(spec.Message) != "" {
			return "", fmt.Errorf("set only one of message or message_file")
		}
		b, err := os.ReadFile(spec.MessageFile)
		if err != nil {
			return "", fmt.Errorf("read message_file: %w", err)
		}
		message = string(b)
	}
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("missing message")
	}
	if strings.TrimSpace(spec.CWD) == "" {
		return "", fmt.Errorf("missing cwd")
	}
	if fi, err := os.Stat(spec.CWD); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("cwd is not an existing directory: %s", spec.CWD)
	}

	unread := true
	if spec.Unread != nil {
		unread = *spec.Unread
	}

	rec := threadRecord{
		ID:      generateThreadID(),
		CWD:     spec.CWD,
		Title:   spec.Title,
		Origin:  "vix",
		Trigger: spec.Trigger,
		Unread:  unread,
		Messages: []llm.MessageParam{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockText, Text: message}},
		}},
		ThreadMode: "chat",
		StartedAt:  time.Now(),
	}

	paths := config.NewVixPaths("", s.homeVixDir, spec.CWD)
	if err := saveThreadRecord(paths, rec); err != nil {
		return "", err
	}
	s.broadcastThreadsChanged()
	return rec.ID, nil
}
