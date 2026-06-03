package daemon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/protocol"
)

// newMinimalSession builds a Session wired just enough to exercise the
// emit/hook gating logic: an eventChan and a live ctx. It bypasses
// NewSession (which needs a full *Server) because the silent gating only
// reads eventChan and ctx.
func newMinimalSession() *Session {
	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel // test lifetime is short enough that a leak is harmless
	return &Session{
		eventChan: make(chan protocol.SessionEvent, 16),
		ctx:       ctx,
	}
}

// drainEventTypes collects the Types of events currently buffered on the
// session's eventChan, draining it without blocking.
func drainEventTypes(s *Session) []string {
	var types []string
	for {
		select {
		case ev := <-s.eventChan:
			types = append(types, ev.Type)
		default:
			return types
		}
	}
}

// ── isUserFacingEvent ──

func TestIsUserFacingEvent(t *testing.T) {
	userFacing := []string{
		"event.stream_chunk",
		"event.thinking_chunk",
		"event.tool_call",
		"event.tool_result",
		"event.workflow_step_start",
		"event.workflow_step_done",
	}
	for _, et := range userFacing {
		if !isUserFacingEvent(et) {
			t.Errorf("%s should be user-facing (and therefore suppressable by silent)", et)
		}
	}

	alwaysThrough := []string{
		"event.error",
		"event.retry",
		"event.stream_done",
		"event.agent_done",
		"event.workflow_complete",
		"event.confirm_request",
		"event.user_question",
		"event.plan_proposed",
		"event.init_state",
		"event.clear",
		"event.session_started",
		"", // empty/unknown
	}
	for _, et := range alwaysThrough {
		if isUserFacingEvent(et) {
			t.Errorf("%s should NOT be user-facing — silent must not swallow it", et)
		}
	}
}

// ── silentCtx round-trip ──

func TestSilentCtx_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if isSilentCtx(ctx) {
		t.Fatal("default context should not be silent")
	}

	silent := withSilentCtx(ctx)
	if !isSilentCtx(silent) {
		t.Error("withSilentCtx(ctx) should produce a silent context")
	}

	// Parent must remain unchanged — scope isolation for concurrent steps.
	if isSilentCtx(ctx) {
		t.Error("parent context should not have been mutated")
	}

	// A child of the silent ctx inherits silence.
	child, cancel := context.WithCancel(silent)
	defer cancel()
	if !isSilentCtx(child) {
		t.Error("children of a silent ctx should also be silent")
	}
}

// ── emitIfVisible ──

func TestEmitIfVisible_SilentDropsUserFacingEvents(t *testing.T) {
	cases := []struct {
		eventType string
		data      any
	}{
		{"event.stream_chunk", protocol.EventStreamChunk{Text: "hello"}},
		{"event.thinking_chunk", protocol.EventThinkingChunk{Text: "thinking"}},
		{"event.tool_call", protocol.EventToolCall{Name: "bash"}},
		{"event.tool_result", protocol.EventToolResult{Name: "bash", Output: "ok"}},
		{"event.workflow_step_start", protocol.EventWorkflowStepStart{StepID: "s1"}},
		{"event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: "s1", Success: true}},
	}
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			s := newMinimalSession()
			s.emitIfVisible(true, tc.eventType, tc.data)

			got := drainEventTypes(s)
			if len(got) != 0 {
				t.Errorf("silent=true should drop %s, got events: %v", tc.eventType, got)
			}
		})
	}
}

func TestEmitIfVisible_SilentPassesNonUserFacingEvents(t *testing.T) {
	cases := []struct {
		eventType string
		data      any
	}{
		{"event.error", protocol.EventError{Message: "boom"}},
		{"event.retry", protocol.EventRetry{Attempt: 1}},
		{"event.stream_done", protocol.EventStreamDone{InputTokens: 10}},
		{"event.workflow_complete", protocol.EventWorkflowComplete{Success: true}},
	}
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			s := newMinimalSession()
			s.emitIfVisible(true, tc.eventType, tc.data)

			got := drainEventTypes(s)
			if len(got) != 1 || got[0] != tc.eventType {
				t.Errorf("silent=true must NOT drop %s, got: %v", tc.eventType, got)
			}
		})
	}
}

func TestEmitIfVisible_NotSilentPassesEverything(t *testing.T) {
	s := newMinimalSession()

	s.emitIfVisible(false, "event.stream_chunk", protocol.EventStreamChunk{Text: "x"})
	s.emitIfVisible(false, "event.tool_call", protocol.EventToolCall{Name: "bash"})
	s.emitIfVisible(false, "event.error", protocol.EventError{Message: "e"})

	got := drainEventTypes(s)
	want := []string{"event.stream_chunk", "event.tool_call", "event.error"}
	if len(got) != len(want) {
		t.Fatalf("silent=false must pass all events; got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ── silentHooks / hooksForStep ──

// TestSilentHooks_DropsUserFacingHookInvocations verifies that the hooks
// returned by silentHooks() do not fan out to s.emit for stream/thinking/
// tool events, even when called directly by the LLM streaming path.
func TestSilentHooks_DropsUserFacingHookInvocations(t *testing.T) {
	s := newMinimalSession()
	hooks := s.silentHooks()

	hooks.OnStreamDelta("some text")
	hooks.OnThinkingDelta("some thinking")
	hooks.OnToolCall(protocol.EventToolCall{Name: "bash"})
	hooks.OnToolResult("id-1", "bash", nil, "ok", false) // isError=false

	got := drainEventTypes(s)
	if len(got) != 0 {
		t.Errorf("silentHooks must not emit any user-facing events, got: %v", got)
	}
}

// TestSilentHooks_PassesToolErrors verifies that tool *failures* still
// surface even in silent mode — silence should never mask errors.
func TestSilentHooks_PassesToolErrors(t *testing.T) {
	s := newMinimalSession()
	hooks := s.silentHooks()

	hooks.OnToolResult("id-1", "bash", nil, "error output", true) // isError=true

	got := drainEventTypes(s)
	if len(got) != 1 || got[0] != "event.tool_result" {
		t.Errorf("silentHooks should surface tool errors, got: %v", got)
	}
}

// TestSilentHooks_PassesStreamDone verifies token-accounting / completion
// hooks still reach the client so telemetry and turn-end logic work.
func TestSilentHooks_PassesStreamDone(t *testing.T) {
	s := newMinimalSession()
	hooks := s.silentHooks()

	hooks.OnStreamDone(100, 50, 10, 5, 2000)

	got := drainEventTypes(s)
	if len(got) != 1 || got[0] != "event.stream_done" {
		t.Errorf("silentHooks should pass stream_done for telemetry, got: %v", got)
	}
}

// TestHooksForStep_SelectsVariantBySilent verifies the dispatch helper
// returns the visible hooks when silent=false and the silent hooks when
// silent=true. We prove it by calling OnStreamDelta and checking whether
// the session channel saw the event.
func TestHooksForStep_SelectsVariantBySilent(t *testing.T) {
	t.Run("silent=false emits stream_chunk", func(t *testing.T) {
		s := newMinimalSession()
		hooks := s.hooksForStep(false)

		hooks.OnStreamDelta("hello")

		got := drainEventTypes(s)
		if len(got) != 1 || got[0] != "event.stream_chunk" {
			t.Errorf("visible hooks should emit stream_chunk, got: %v", got)
		}
	})

	t.Run("silent=true suppresses stream_chunk", func(t *testing.T) {
		s := newMinimalSession()
		hooks := s.hooksForStep(true)

		hooks.OnStreamDelta("hello")

		got := drainEventTypes(s)
		if len(got) != 0 {
			t.Errorf("silent hooks should drop stream_chunk, got: %v", got)
		}
	})
}

// ── [dispatch] log suppression via isSilentCtx ──

// TestDispatch_SilentCtxSuppressesLogs drives dispatchToolCalls end-to-end
// with a silent-wrapped context and asserts no `[dispatch]` log lines are
// written. The control case (non-silent ctx) is checked in the same test
// so regressions in either direction fail.
func TestDispatch_SilentCtxSuppressesLogs(t *testing.T) {
	// Capture log output; restore at the end.
	origOutput := log.Writer()
	origFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	})
	log.SetFlags(0) // no timestamp prefix

	runDispatch := func(ctx context.Context) string {
		var buf bytes.Buffer
		log.SetOutput(&buf)

		msg := buildToolUseMessage(t, "bash", "toolu_silent_1")
		opts := dispatchOptions{
			cwd: t.TempDir(),
			executeTool: func(name string, input map[string]any) *ToolResult {
				return &ToolResult{Output: "ran"}
			},
		}
		_ = dispatchToolCalls(ctx, msg, opts)
		return buf.String()
	}

	t.Run("silent ctx suppresses [dispatch] lines", func(t *testing.T) {
		out := runDispatch(withSilentCtx(context.Background()))
		if strings.Contains(out, "[dispatch]") {
			t.Errorf("expected no [dispatch] log lines under silent ctx; got:\n%s", out)
		}
	})

	t.Run("non-silent ctx still emits [dispatch] lines", func(t *testing.T) {
		out := runDispatch(context.Background())
		if !strings.Contains(out, "[dispatch] tool call:") {
			t.Errorf("expected [dispatch] tool call log on non-silent ctx; got:\n%s", out)
		}
		if !strings.Contains(out, "[dispatch] exec start:") {
			t.Errorf("expected [dispatch] exec start log on non-silent ctx; got:\n%s", out)
		}
	})
}

// TestWorkflowStepDef_SilentDefault verifies that when the field is absent
// from JSON (the common case), Silent defaults to false — steps keep their
// existing behaviour unless they opt in.
func TestWorkflowStepDef_SilentDefault(t *testing.T) {
	var step WorkflowStepDef
	if step.Silent {
		t.Error("zero-value WorkflowStepDef.Silent should be false")
	}

	// Guard against accidental goroutine leaks in emit tests: give the
	// dispatcher a moment to settle before the test exits. Not strictly
	// needed but keeps `go test -race` quiet on slow machines.
	time.Sleep(1 * time.Millisecond)
}
