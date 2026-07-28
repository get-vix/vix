package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/protocol"
)

// runSyncHook executes one synchronous hook and returns its (possibly
// downgraded) Decision. Command hooks decide via exit code / stdout JSON;
// workflow and prompt hooks decide via their final text (JSON or a BLOCK:
// sentinel). A non-blocking hook can never deny or modify — its verdict is
// downgraded to context so it stays observational.
func (s *Server) runSyncHook(ctx context.Context, spec hooks.Spec, base map[string]any) hooks.Decision {
	cwd := hookCWD(spec, base)
	fireID := newFireID()
	s.logHookFired(fireID, spec, false, base)
	start := time.Now()
	var dec hooks.Decision
	var errMsg string
	if spec.Command != "" {
		stdin, _ := json.Marshal(base)
		code, out, errOut := runHookCommand(ctx, spec, cwd, stdin)
		if code < 0 {
			errMsg = strings.TrimSpace(errOut)
			s.logHookError(fireID, spec, "command_exec", errMsg, code)
		}
		dec = hooks.ParseCommandDecision(code, out, errOut)
	} else {
		final, hadErr := s.runHookSession(ctx, spec, hookSessionText(spec, base), cwd, false, "")
		if hadErr {
			errMsg = "hook session errored"
			s.logHookError(fireID, spec, "agent", errMsg, 0)
		}
		dec = hooks.ParseTextDecision(final)
	}
	dec = downgradeIfNonBlocking(spec, dec)
	dur := time.Since(start)
	s.logHookFinished(fireID, spec, dec.Behavior, dur)
	s.recordHookRun(spec, hooks.RunRecord{
		At:       start,
		Status:   dec.Behavior,
		Async:    false,
		Event:    spec.Trigger.Event,
		Error:    errMsg,
		Duration: dur.String(),
	})
	return dec
}

// fireAsyncHook runs a hook fire-and-forget, decoupled from the triggering turn
// (it derives from the server context so it can outlive the tool call).
func (s *Server) fireAsyncHook(spec hooks.Spec, base map[string]any) {
	s.fireHookAsync(spec, base, "")
}

// fireHookAsync runs a hook fire-and-forget, decoupled from any triggering turn
// (it derives from the server context so it can outlive the caller). For
// workflow/prompt hooks the run executes in an isolated session whose id is
// runID (empty = mint a fresh one); command hooks have no session and ignore
// it. Returns the session id (empty for command hooks) and the fire id, so an
// on-demand trigger can surface them immediately.
func (s *Server) fireHookAsync(spec hooks.Spec, base map[string]any, runID string) (string, string) {
	parent := s.serverCtx
	if parent == nil {
		parent = context.Background()
	}
	fireID := newFireID()
	isCommand := spec.Command != ""
	if !isCommand && runID == "" {
		runID = generateSessionID()
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, spec.TimeoutDuration())
		defer cancel()
		s.logHookFired(fireID, spec, true, base)
		start := time.Now()
		cwd := hookCWD(spec, base)
		status := "done"
		var errMsg string
		sessionID := ""
		if isCommand {
			stdin, _ := json.Marshal(base)
			code, _, errOut := runHookCommand(ctx, spec, cwd, stdin)
			if code < 0 {
				errMsg = strings.TrimSpace(errOut)
				s.logHookError(fireID, spec, "command_exec", errMsg, code)
				status = "error"
			}
		} else {
			sessionID = runID
			_, hadErr := s.runHookSession(ctx, spec, hookSessionText(spec, base), cwd, true, runID)
			if hadErr {
				errMsg = "hook session errored"
				s.logHookError(fireID, spec, "agent", errMsg, 0)
				status = "error"
			}
		}
		dur := time.Since(start)
		s.logHookFinished(fireID, spec, status, dur)
		s.recordHookRun(spec, hooks.RunRecord{
			At:        start,
			Status:    status,
			Async:     true,
			Event:     spec.Trigger.Event,
			Error:     errMsg,
			SessionID: sessionID,
			Duration:  dur.String(),
		})
	}()
	if isCommand {
		return "", fireID
	}
	return runID, fireID
}

// TriggerHook fires the hook with the given id immediately, out of band from its
// event (backs `vix hook trigger <id>`). A manual trigger has no triggering
// action to veto, so it always runs fire-and-forget regardless of the hook's
// mode, even when the hook is disabled. It synthesizes a minimal context
// envelope and returns the run's session id (empty for command hooks) and the
// fire id. Errors surface synchronously for an unknown id or a disabled engine.
func (s *Server) TriggerHook(id string) (string, string, error) {
	if s.hookRegistry == nil {
		return "", "", fmt.Errorf("hooks engine is disabled")
	}
	spec, ok := s.hookRegistry.SpecByID(id)
	if !ok {
		return "", "", fmt.Errorf("hook %q not found", id)
	}
	base := map[string]any{"event": spec.Trigger.Event, "manual": true}
	if spec.CWD != "" {
		base["cwd"] = spec.CWD
	}
	sessionID, fireID := s.fireHookAsync(spec, base, "")
	return sessionID, fireID, nil
}

// recordHookRun appends a fire to the hook's recent-run history via the
// registry. Nil-safe: when the hooks engine is disabled there is no registry to
// record against.
func (s *Server) recordHookRun(spec hooks.Spec, rec hooks.RunRecord) {
	if s.hookRegistry == nil {
		return
	}
	s.hookRegistry.RecordRun(spec.ID, rec)
	// Surface the updated last-fired state to the Jobs & Triggers tab live.
	s.broadcastJobsChanged()
}

// downgradeIfNonBlocking strips a non-blocking hook's veto powers: a deny is
// surfaced to the model as context, a modify is dropped.
func downgradeIfNonBlocking(spec hooks.Spec, d hooks.Decision) hooks.Decision {
	if spec.Blocking {
		return d
	}
	switch d.Behavior {
	case hooks.BehaviorDeny:
		return hooks.Decision{Behavior: hooks.BehaviorContext, Context: d.Reason}
	case hooks.BehaviorModify:
		return hooks.Decision{Behavior: hooks.BehaviorAllow}
	}
	return d
}

// runHookCommand runs a command hook with the context JSON on stdin and returns
// (exitCode, stdout, stderr). A timeout (or non-ExitError failure) yields exit
// code -1, which ParseCommandDecision treats as fail-open allow so a broken
// hook never wedges the loop.
func runHookCommand(ctx context.Context, spec hooks.Spec, cwd string, stdin []byte) (int, string, string) {
	cctx, cancel := context.WithTimeout(ctx, spec.TimeoutDuration())
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", spec.Command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid) }

	err := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		LogError("hook %q: timed out after %s", spec.ID, spec.TimeoutDuration())
		return -1, out.String(), "hook timed out"
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), out.String(), errb.String()
		}
		LogError("hook %q: command failed to run: %v", spec.ID, err)
		return -1, out.String(), err.Error()
	}
	return 0, out.String(), errb.String()
}

// runHookSession runs a workflow- or prompt-form hook in an isolated headless
// session (origin "vix" so it can't itself re-trigger hooks). It returns the
// concatenated assistant text and whether an error occurred. When persist is
// true the run is registered/persisted so it appears in the Sessions tab under
// "Vix-initiated"; sync veto runs pass false to avoid a record per tool call.
// runID seeds the session id (empty = mint a fresh one) so callers that need it
// up front can pre-generate it.
func (s *Server) runHookSession(ctx context.Context, spec hooks.Spec, text, cwd string, persist bool, runID string) (string, bool) {
	if runID == "" {
		runID = generateSessionID()
	}
	session := NewSession(runID, s, nil, s.model, cwd, "", false,
		spec.AutoWrite(), spec.AutoDirs(), true /*headless*/, ctx)
	session.origin = "vix"
	session.trigger = &protocol.TriggerInfo{Type: "hook", Ref: spec.ID}
	session.title = "Hook: " + hookName(spec)

	if persist {
		s.sessionMu.Lock()
		s.sessions[runID] = session
		s.sessionMu.Unlock()
		s.broadcastSessionsChanged()
		defer func() {
			s.sessionMu.Lock()
			delete(s.sessions, runID)
			s.sessionMu.Unlock()
			session.cancel()
			s.broadcastSessionsChanged()
		}()
	} else {
		defer session.cancel()
	}

	go session.Run()

	var startCmd protocol.SessionCommand
	switch {
	case spec.Workflow != nil:
		raw, _ := json.Marshal(spec.Workflow)
		data, _ := json.Marshal(protocol.SessionWorkflowData{Name: spec.Workflow.Name, Text: text, Workflow: raw})
		startCmd = protocol.SessionCommand{Type: "session.workflow", Data: data}
	case spec.WorkflowID != "":
		data, _ := json.Marshal(protocol.SessionWorkflowData{Name: spec.WorkflowID, Text: text})
		startCmd = protocol.SessionCommand{Type: "session.workflow", Data: data}
	default:
		data, _ := json.Marshal(protocol.SessionInputData{Text: text})
		startCmd = protocol.SessionCommand{Type: "session.input", Data: data}
	}
	if !session.pushCommand(ctx, startCmd) {
		return "", true
	}

	var (
		finalText strings.Builder
		hadError  bool
	)
consume:
	for {
		select {
		case ev := <-session.eventChan:
			switch ev.Type {
			case "event.stream_chunk":
				finalText.WriteString(decodeJobEvent[protocol.EventStreamChunk](ev.Data).Text)
			case "event.confirm_request":
				data, _ := json.Marshal(protocol.SessionConfirmData{Approved: false})
				session.pushCommand(ctx, protocol.SessionCommand{Type: "session.confirm", Data: data})
			case "event.user_question":
				uq := decodeJobEvent[protocol.EventUserQuestion](ev.Data)
				answer := ""
				if len(uq.RichOptions) > 0 {
					answer = uq.RichOptions[0].Title
				} else if len(uq.Options) > 0 {
					answer = uq.Options[0]
				}
				data, _ := json.Marshal(protocol.SessionUserAnswerData{Answer: answer})
				session.pushCommand(ctx, protocol.SessionCommand{Type: "session.user_answer", Data: data})
			case "event.plan_proposed":
				data, _ := json.Marshal(protocol.SessionPlanActionData{Action: "approve"})
				session.pushCommand(ctx, protocol.SessionCommand{Type: "session.plan_action", Data: data})
			case "event.error":
				hadError = true
			case "event.agent_done":
				break consume
			}
		case <-ctx.Done():
			return finalText.String(), true
		case <-session.ctx.Done():
			break consume
		}
	}
	if persist && !hadError {
		session.persist()
	}
	return finalText.String(), hadError
}

// hookCWD resolves the working directory for a hook run: the spec's explicit
// cwd, else the triggering session's cwd from the context envelope.
func hookCWD(spec hooks.Spec, base map[string]any) string {
	if spec.CWD != "" {
		return spec.CWD
	}
	if v, ok := base["cwd"].(string); ok {
		return v
	}
	return ""
}

// hookSessionText builds the message text for a workflow/prompt hook. The
// context envelope is appended as JSON so the workflow/prompt can inspect it.
func hookSessionText(spec hooks.Spec, base map[string]any) string {
	b, _ := json.Marshal(base)
	if spec.Prompt != "" {
		return spec.Prompt + "\n\nHook context:\n" + string(b)
	}
	return string(b)
}

func hookName(spec hooks.Spec) string {
	if spec.Name != "" {
		return spec.Name
	}
	return spec.ID
}
