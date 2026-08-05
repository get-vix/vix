package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// drainFor collects events until an event.agent_done arrives (or a timeout),
// returning every event seen so the caller can assert on them.
func drainFor(t *testing.T, events chan protocol.ThreadEvent) []protocol.ThreadEvent {
	t.Helper()
	var got []protocol.ThreadEvent
	for {
		select {
		case ev := <-events:
			got = append(got, ev)
			if ev.Type == "event.agent_done" {
				return got
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event.agent_done")
		}
	}
}

// TestHandleInputUnconfigured_NoCredential asserts that an unconfigured thread
// (no credential for the selected model) refuses to stream and instead emits
// the friendly, display-name-keyed error pointing the user to set credentials.
// s.llm is nil, so any attempt to stream would panic — its absence proves the
// gate short-circuits before any LLM call.
func TestHandleInputUnconfigured_NoCredential(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan protocol.ThreadEvent, 16)

	s := &Thread{
		ctx:       ctx,
		eventChan: events,
		model:     "anthropic/claude-sonnet-4-6",
		configErr: fmt.Errorf("no credential for anthropic (set ANTHROPIC_API_KEY): %w", llm.ErrNoCredential),
	}

	s.handleInput("hello", nil)

	got := drainFor(t, events)

	var errMsg string
	var sawDone bool
	for _, ev := range got {
		switch ev.Type {
		case "event.error":
			errMsg = ev.Data.(protocol.EventError).Message
		case "event.agent_done":
			sawDone = true
		case "event.stream_chunk", "event.stream_done", "event.thinking_chunk":
			t.Fatalf("unconfigured thread must not stream, got %s", ev.Type)
		}
	}
	if !sawDone {
		t.Fatal("expected event.agent_done")
	}
	if !strings.Contains(errMsg, "Claude Sonnet 4 6") {
		t.Errorf("error should name the model's display name, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "Models (F3)") {
		t.Errorf("error should point the user to Models (F3), got %q", errMsg)
	}
}

// TestHandleInputUnconfigured_OtherError shows non-credential construction
// failures surface their raw error verbatim (no friendly substitution).
func TestHandleInputUnconfigured_OtherError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan protocol.ThreadEvent, 16)

	s := &Thread{
		ctx:       ctx,
		eventChan: events,
		model:     "bogus/model",
		configErr: fmt.Errorf("unknown provider prefix %q", "bogus/"),
	}

	s.handleInput("hello", nil)

	got := drainFor(t, events)
	var errMsg string
	for _, ev := range got {
		if ev.Type == "event.error" {
			errMsg = ev.Data.(protocol.EventError).Message
		}
	}
	if !strings.Contains(errMsg, "unknown provider prefix") {
		t.Errorf("non-credential error should pass through verbatim, got %q", errMsg)
	}
	if strings.Contains(errMsg, "Models (F3)") {
		t.Errorf("non-credential error must not use the credential message, got %q", errMsg)
	}
}
