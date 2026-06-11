package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/jobs"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// heartbeatOKToken is the contract for "nothing needs attention": a job run
// whose final text is this token (give or take a short ack) is recorded as
// skipped — no session record, no notification.
const heartbeatOKToken = "HEARTBEAT_OK"

// heartbeatOKSlop is how much text may surround the token before the reply
// stops counting as a bare acknowledgement.
const heartbeatOKSlop = 300

// JobRunner returns the jobs.Runner executing runs in-process: an isolated
// headless session per run, mirroring `vix -p [-w workflow]` semantics.
func (s *Server) JobRunner() jobs.Runner {
	return s.runJob
}

// runJob drives one scheduled job run to completion. ctx carries the per-run
// timeout; cancelling it tears the session down.
func (s *Server) runJob(ctx context.Context, spec jobs.Spec, resolvedPrompt string) jobs.RunResult {
	runID := generateSessionID()
	session := NewSession(runID, s, nil, s.model, spec.CWD, "", false,
		spec.AutoWrite(), spec.AutoDirs(), true /*headless*/, ctx)
	session.origin = "vix"
	session.trigger = &protocol.TriggerInfo{Type: spec.Trigger.Type, Ref: spec.ID}

	// Register so the web UI and session.list see the run while it's live.
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

	go session.Run()

	// Dispatch exactly like headless: workflow set → session.workflow with the
	// resolved prompt as $(workflow.prompt); nil → plain chat turn.
	var startCmd protocol.SessionCommand
	if spec.Workflow != "" {
		data, _ := json.Marshal(protocol.SessionWorkflowData{Name: spec.Workflow, Text: resolvedPrompt})
		startCmd = protocol.SessionCommand{Type: "session.workflow", Data: data}
	} else {
		data, _ := json.Marshal(protocol.SessionInputData{Text: resolvedPrompt})
		startCmd = protocol.SessionCommand{Type: "session.input", Data: data}
	}
	if !session.pushCommand(ctx, startCmd) {
		return jobs.RunResult{Status: jobs.StatusError, Err: "session refused start command"}
	}

	// Consume the event stream (mandatory: emit blocks once eventChan fills
	// with no reader), answering interactive events with unattended policy:
	// confirmations are denied and recorded, questions take the first option,
	// plans are approved — mirroring headless except for the deny.
	var (
		finalText  strings.Builder
		agentTurns int
		hadError   bool
		errMsg     string
		denials    []string
	)

consume:
	for {
		select {
		case ev := <-session.eventChan:
			switch ev.Type {
			case "event.stream_chunk":
				finalText.WriteString(decodeJobEvent[protocol.EventStreamChunk](ev.Data).Text)
			case "event.stream_done":
				agentTurns++
			case "event.confirm_request":
				cr := decodeJobEvent[protocol.EventConfirmRequest](ev.Data)
				denials = append(denials, cr.ToolName)
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
				errMsg = decodeJobEvent[protocol.EventError](ev.Data).Message
			case "event.agent_done":
				break consume
			}
		case <-ctx.Done():
			// Timeout or daemon shutdown: the session ctx (derived from ctx)
			// is collapsing; persist what we have and report.
			session.persist()
			return jobs.RunResult{
				Status:    jobs.StatusTimeout,
				Err:       "run cancelled: " + ctx.Err().Error(),
				SessionID: runID,
			}
		case <-session.ctx.Done():
			break consume
		}
	}

	res := jobs.RunResult{Status: jobs.StatusOK, SessionID: runID}
	if hadError {
		res.Status = jobs.StatusError
		res.Err = errMsg
	}
	if len(denials) > 0 && res.Err == "" {
		res.Err = "needed approval for: " + strings.Join(denials, "; ")
	}

	// Skip rules — a skipped run leaves no trace:
	//   cheap-poll: no agent step executed (a poll workflow whose execute_if
	//   gate didn't pass — bash steps never call the LLM);
	//   heartbeat OK: the model said nothing needs attention.
	if res.Status == jobs.StatusOK && (agentTurns == 0 || isHeartbeatOK(finalText.String())) {
		deleteSessionRecord(session.paths, runID)
		return jobs.RunResult{Status: jobs.StatusSkipped, SessionID: runID}
	}

	// Every other finished run lands in open/: visible in the Vix-initiated
	// sessions group until the user dismisses it (or retention sweeps it).
	session.jobStatus = res.Status
	session.persist()
	sweepJobRunRecords(session.paths, spec.ID)

	// Failures nobody saw get a synthetic explainer session on top of the run
	// record, so the next TUI launch surfaces them.
	if res.Status != jobs.StatusOK && !s.hasAttachedInstances() {
		s.writeJobAlertSession(spec, res)
	}
	return res
}

// pushCommand feeds a command to the session loop, giving up when either
// context dies. Returns false when the command was not delivered.
func (s *Session) pushCommand(ctx context.Context, cmd protocol.SessionCommand) bool {
	select {
	case s.commandChan <- cmd:
		return true
	case <-ctx.Done():
		return false
	case <-s.ctx.Done():
		return false
	}
}

// hasAttachedInstances reports whether any vix process is currently attached.
func (s *Server) hasAttachedInstances() bool {
	s.instanceMu.Lock()
	defer s.instanceMu.Unlock()
	return s.instanceCount > 0
}

// broadcastSessionsChanged tells attached clients (and web UI subscribers) the
// persisted sessions list changed outside their own connection.
func (s *Server) broadcastSessionsChanged() {
	s.BroadcastEvent(protocol.SessionEvent{Type: "event.sessions_changed", Data: protocol.EventSessionsChanged{}})
	s.notifySubscribers()
}

// writeJobAlertSession persists a synthetic one-message session explaining a
// failed job run. Zero tokens: the text is canned. It lands in open/ so the
// next TUI launch lists it under Vix-initiated sessions.
func (s *Server) writeJobAlertSession(spec jobs.Spec, res jobs.RunResult) {
	name := spec.Name
	if name == "" {
		name = spec.ID
	}
	text := fmt.Sprintf(
		"Your job %q failed at %s (%s).",
		name, time.Now().Format("15:04"), res.Status)
	if res.Err != "" {
		text += "\n\nError: " + res.Err
	}
	if res.SessionID != "" {
		text += fmt.Sprintf("\n\nThe full run is in session %s.", res.SessionID)
	}
	rec := sessionRecord{
		ID:      generateSessionID(),
		CWD:     spec.CWD,
		Origin:  "vix",
		Trigger: &protocol.TriggerInfo{Type: spec.Trigger.Type, Ref: spec.ID},
		Unread:  true,
		Messages: []llm.MessageParam{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockText, Text: text}},
		}},
		SessionMode: "chat",
		StartedAt:   time.Now(),
	}
	paths := config.NewVixPaths("", s.homeVixDir, spec.CWD)
	if err := saveSessionRecord(paths, rec); err != nil {
		LogError("job alert session: %v", err)
	}
}

// isHeartbeatOK reports whether text is a bare "nothing needs attention"
// acknowledgement: the HEARTBEAT_OK token at the start or end with at most
// heartbeatOKSlop other characters around it.
func isHeartbeatOK(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if !strings.HasPrefix(t, heartbeatOKToken) && !strings.HasSuffix(t, heartbeatOKToken) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, heartbeatOKToken), heartbeatOKToken))
	return len(rest) <= heartbeatOKSlop
}

// decodeJobEvent unmarshals an event payload into the given type.
func decodeJobEvent[T any](data any) T {
	var out T
	raw, err := json.Marshal(data)
	if err != nil {
		return out
	}
	json.Unmarshal(raw, &out)
	return out
}
