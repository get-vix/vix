package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
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
		final, hadErr := s.runHookThread(ctx, spec, hookThreadText(spec, base), cwd, false, "")
		if hadErr {
			errMsg = "hook thread errored"
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
// workflow/prompt hooks the run executes in an isolated thread whose id is
// runID (empty = mint a fresh one); command hooks have no thread and ignore
// it. Returns the thread id (empty for command hooks) and the fire id, so an
// on-demand trigger can surface them immediately.
func (s *Server) fireHookAsync(spec hooks.Spec, base map[string]any, runID string) (string, string) {
	parent := s.serverCtx
	if parent == nil {
		parent = context.Background()
	}
	fireID := newFireID()
	isCommand := spec.Command != ""
	if !isCommand && runID == "" {
		runID = generateThreadID()
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, spec.TimeoutDuration())
		defer cancel()
		s.logHookFired(fireID, spec, true, base)
		start := time.Now()
		cwd := hookCWD(spec, base)
		status := "done"
		var errMsg string
		threadID := ""
		if isCommand {
			stdin, _ := json.Marshal(base)
			code, _, errOut := runHookCommand(ctx, spec, cwd, stdin)
			if code < 0 {
				errMsg = strings.TrimSpace(errOut)
				s.logHookError(fireID, spec, "command_exec", errMsg, code)
				status = "error"
			}
		} else {
			threadID = runID
			_, hadErr := s.runHookThread(ctx, spec, hookThreadText(spec, base), cwd, true, runID)
			if hadErr {
				errMsg = "hook thread errored"
				s.logHookError(fireID, spec, "agent", errMsg, 0)
				status = "error"
			}
		}
		dur := time.Since(start)
		s.logHookFinished(fireID, spec, status, dur)
		s.recordHookRun(spec, hooks.RunRecord{
			At:       start,
			Status:   status,
			Async:    true,
			Event:    spec.Trigger.Event,
			Error:    errMsg,
			ThreadID: threadID,
			Duration: dur.String(),
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
// envelope and returns the run's thread id (empty for command hooks) and the
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
	threadID, fireID := s.fireHookAsync(spec, base, "")
	return threadID, fireID, nil
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

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

// runHookThread runs a workflow- or prompt-form hook in an isolated headless
// thread (origin "vix" so it can't itself re-trigger hooks). It returns the
// concatenated assistant text and whether an error occurred. When persist is
// true the run is registered/persisted so it appears in the Threads tab under
// "Vix-initiated"; sync veto runs pass false to avoid a record per tool call.
// runID seeds the thread id (empty = mint a fresh one) so callers that need it
// up front can pre-generate it.
func (s *Server) runHookThread(ctx context.Context, spec hooks.Spec, text, cwd string, persist bool, runID string) (string, bool) {
	if runID == "" {
		runID = generateThreadID()
	}
	thread := NewThread(runID, s, nil, s.model, cwd, "", false,
		spec.AutoWrite(), spec.AutoDirs(), true /*headless*/, ctx)
	thread.origin = "vix"
	thread.trigger = &protocol.TriggerInfo{Type: "hook", Ref: spec.ID}
	thread.title = "Hook: " + hookName(spec)

	if persist {
		s.threadMu.Lock()
		s.threads[runID] = thread
		s.threadMu.Unlock()
		s.broadcastThreadsChanged()
		defer func() {
			s.threadMu.Lock()
			delete(s.threads, runID)
			s.threadMu.Unlock()
			thread.cancel()
			s.broadcastThreadsChanged()
		}()
	} else {
		defer thread.cancel()
	}

	go thread.Run()

	var startCmd protocol.ThreadCommand
	switch {
	case spec.Workflow != nil:
		raw, _ := json.Marshal(spec.Workflow)
		data, _ := json.Marshal(protocol.ThreadWorkflowData{Name: spec.Workflow.Name, Text: text, Workflow: raw})
		startCmd = protocol.ThreadCommand{Type: "thread.workflow", Data: data}
	case spec.WorkflowID != "":
		data, _ := json.Marshal(protocol.ThreadWorkflowData{Name: spec.WorkflowID, Text: text})
		startCmd = protocol.ThreadCommand{Type: "thread.workflow", Data: data}
	default:
		data, _ := json.Marshal(protocol.ThreadInputData{Text: text})
		startCmd = protocol.ThreadCommand{Type: "thread.input", Data: data}
	}
	if !thread.pushCommand(ctx, startCmd) {
		return "", true
	}

	var (
		finalText strings.Builder
		hadError  bool
	)
consume:
	for {
		select {
		case ev := <-thread.eventChan:
			switch ev.Type {
			case "event.stream_chunk":
				finalText.WriteString(decodeJobEvent[protocol.EventStreamChunk](ev.Data).Text)
			case "event.confirm_request":
				data, _ := json.Marshal(protocol.ThreadConfirmData{Approved: false})
				thread.pushCommand(ctx, protocol.ThreadCommand{Type: "thread.confirm", Data: data})
			case "event.user_question":
				uq := decodeJobEvent[protocol.EventUserQuestion](ev.Data)
				answer := ""
				if len(uq.RichOptions) > 0 {
					answer = uq.RichOptions[0].Title
				} else if len(uq.Options) > 0 {
					answer = uq.Options[0]
				}
				data, _ := json.Marshal(protocol.ThreadUserAnswerData{Answer: answer})
				thread.pushCommand(ctx, protocol.ThreadCommand{Type: "thread.user_answer", Data: data})
			case "event.plan_proposed":
				data, _ := json.Marshal(protocol.ThreadPlanActionData{Action: "approve"})
				thread.pushCommand(ctx, protocol.ThreadCommand{Type: "thread.plan_action", Data: data})
			case "event.error":
				hadError = true
			case "event.agent_done":
				break consume
			}
		case <-ctx.Done():
			return finalText.String(), true
		case <-thread.ctx.Done():
			break consume
		}
	}
	if persist && !hadError {
		thread.persist()
	}
	return finalText.String(), hadError
}

// hookCWD resolves the working directory for a hook run: the spec's explicit
// cwd, else the triggering thread's cwd from the context envelope.
func hookCWD(spec hooks.Spec, base map[string]any) string {
	if spec.CWD != "" {
		return spec.CWD
	}
	if v, ok := base["cwd"].(string); ok {
		return v
	}
	return ""
}

// hookThreadText builds the message text for a workflow/prompt hook. The
// context envelope is appended as JSON so the workflow/prompt can inspect it.
func hookThreadText(spec hooks.Spec, base map[string]any) string {
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
