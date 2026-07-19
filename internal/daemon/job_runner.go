package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/daemon/jobs"
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

// jobTitleTimeFormat renders job-run timestamps in titles (en_US style).
const jobTitleTimeFormat = "01/02/2006 3:04 PM"

// runJob drives one scheduled job run to completion. ctx carries the per-run
// timeout; cancelling it tears the session down.
func (s *Server) runJob(ctx context.Context, spec jobs.Spec, resolvedPrompt string) jobs.RunResult {
	runID := jobs.RunIDFromContext(ctx)
	if runID == "" {
		runID = generateSessionID()
	}

	req := unattendedRunRequest{
		RunID:                   runID,
		Model:                   s.model,
		CWD:                     spec.CWD,
		Title:                   jobRunTitle(spec, time.Now()),
		Trigger:                 &protocol.TriggerInfo{Type: spec.Trigger.Type, Ref: spec.ID},
		Prompt:                  resolvedPrompt,
		AutoWrite:               spec.AutoWrite(),
		AutoDirs:                spec.AutoDirs(),
		JobID:                   spec.ID,
		SuppressFinishBroadcast: true,
	}
	if spec.Workflow != nil {
		req.Workflow.Inline = spec.Workflow
	} else if spec.WorkflowID != "" {
		req.Workflow.Name = spec.WorkflowID
	}
	run := s.runUnattendedSession(ctx, req, jobUnattendedPolicy)
	res := jobRunResultFromUnattended(run)

	// Skip rules — a skipped run leaves no trace:
	//   cheap-poll: no agent step executed (a poll workflow whose execute_if
	//   gate didn't pass — bash steps never call the LLM);
	//   heartbeat OK: the model said nothing needs attention.
	if res.Status == jobs.StatusOK && (run.AgentTurns == 0 || isHeartbeatOK(run.FinalText)) {
		if run.session != nil {
			deleteSessionRecord(run.session.paths, run.SessionID)
			s.broadcastSessionsChanged()
		}
		return jobs.RunResult{Status: jobs.StatusSkipped, SessionID: run.SessionID, AgentTurns: run.AgentTurns}
	}

	// Every other finished run lands in open/: visible in the Vix-initiated
	// sessions group until the user dismisses it (or retention sweeps it).
	if run.session != nil {
		run.session.jobStatus = res.Status
		// Successful GitHub-plan runs open their findings with a deterministic
		// header line naming the item they picked; turn that into a per-item session
		// title (e.g. "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — …").
		// Other jobs (and the "nothing new"/error branches) keep the static title.
		if res.Status == jobs.StatusOK {
			if title, ok := issuePlanTitle(spec, run.FinalText); ok {
				run.session.mu.Lock()
				run.session.title = title
				run.session.mu.Unlock()
			}
		}
		run.session.persist()
		sweepJobRunRecords(run.session.paths, spec.ID)
		s.broadcastSessionsChanged()
	}

	// Failures nobody saw get a synthetic explainer session on top of the run
	// record, so the next TUI launch surfaces them.
	if res.Status != jobs.StatusOK && !s.hasAttachedInstances() {
		s.writeJobAlertSession(spec, res)
	}
	return res
}

func jobUnattendedPolicy(ctx context.Context, session *Session, ev protocol.SessionEvent) (bool, error) {
	switch ev.Type {
	case "event.confirm_request":
		data, _ := json.Marshal(protocol.SessionConfirmData{Approved: false})
		return session.pushCommand(ctx, protocol.SessionCommand{Type: "session.confirm", Data: data}), nil
	case "event.user_question":
		uq := decodeUnattendedEvent[protocol.EventUserQuestion](ev.Data)
		answer := ""
		if len(uq.RichOptions) > 0 {
			answer = uq.RichOptions[0].Title
		} else if len(uq.Options) > 0 {
			answer = uq.Options[0]
		}
		data, _ := json.Marshal(protocol.SessionUserAnswerData{Answer: answer})
		return session.pushCommand(ctx, protocol.SessionCommand{Type: "session.user_answer", Data: data}), nil
	case "event.plan_proposed":
		data, _ := json.Marshal(protocol.SessionPlanActionData{Action: "approve"})
		return session.pushCommand(ctx, protocol.SessionCommand{Type: "session.plan_action", Data: data}), nil
	default:
		return false, nil
	}
}

func jobRunResultFromUnattended(run unattendedRunResult) jobs.RunResult {
	if run.TimedOut {
		return jobs.RunResult{
			Status:     jobs.StatusTimeout,
			Err:        run.Err,
			SessionID:  run.SessionID,
			AgentTurns: run.AgentTurns,
			Errors:     []jobs.RunError{{Source: "timeout", Message: run.Err}},
		}
	}

	res := jobs.RunResult{
		Status:     jobs.StatusOK,
		SessionID:  run.SessionID,
		AgentTurns: run.AgentTurns,
		Denials:    run.ConfirmRequests,
	}
	if run.HadError {
		errMsg := run.Err
		if errMsg == "" {
			errMsg = "agent run failed"
		}
		errSource := run.ErrSource
		if errSource == "" {
			errSource = "agent"
		}
		res.Status = jobs.StatusError
		res.Err = errMsg
		res.Errors = append(res.Errors, jobs.RunError{Source: errSource, Message: errMsg})
	}
	if len(run.ConfirmRequests) > 0 && res.Err == "" {
		res.Err = "needed approval for: " + strings.Join(run.ConfirmRequests, "; ")
	}
	if len(run.ConfirmRequests) > 0 {
		res.Errors = append(res.Errors, jobs.RunError{Source: "denied", Message: "needed approval for: " + strings.Join(run.ConfirmRequests, "; ")})
	}
	return res
}

// jobRunTitle builds the display title of a job-run session, e.g.
// "Heartbeat - 06/12/2026 9:30 AM".
func jobRunTitle(spec jobs.Spec, t time.Time) string {
	name := spec.Name
	if name == "" {
		name = spec.ID
	}
	return name + " - " + t.Format(jobTitleTimeFormat)
}

// issuePlanHeaderRe matches the deterministic first line of a GitHub-plan run's
// findings (built by the plan step in githubIssuePlanWorkflow):
//
//	Hi, I investigated <issue|pull request> #<n> — <item title> — on GitHub. …
//
// The title is non-greedy and anchored on " — on GitHub." so item titles that
// themselves contain dashes survive. `.` never spans newlines, so the match
// stays on the header line.
var issuePlanHeaderRe = regexp.MustCompile(`Hi, I investigated (issue|pull request) #(\d+) — (.+?) — on GitHub\.`)

// issuePlanTitle derives a per-item session title from a GitHub-plan run's
// final text, e.g. "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — …".
// Returns ok=false when the deterministic header is absent (any non-plan job, or
// the "nothing new to plan"/error branches), so the caller keeps the static
// jobRunTitle.
func issuePlanTitle(spec jobs.Spec, finalText string) (string, bool) {
	m := issuePlanHeaderRe.FindStringSubmatch(finalText)
	if m == nil {
		return "", false
	}
	kind, number, itemTitle := m[1], m[2], strings.TrimSpace(m[3])
	if itemTitle == "" {
		return "", false
	}
	name := spec.Name
	if name == "" {
		name = spec.ID
	}
	return fmt.Sprintf("[%s] Addressing %s #%s — %s", name, kind, number, itemTitle), true
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

// broadcastJobsChanged tells attached clients (and web UI subscribers) the jobs
// or hooks list changed — a run started/finished, a spec was enabled/disabled,
// or the spec directory was reloaded — so the Jobs & Triggers tab re-fetches.
func (s *Server) broadcastJobsChanged() {
	s.BroadcastEvent(protocol.SessionEvent{Type: "event.jobs_changed", Data: protocol.EventJobsChanged{}})
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
	if _, err := s.createMessageSession(MessageSessionSpec{
		Message: text,
		CWD:     spec.CWD,
		Title:   jobRunTitle(spec, time.Now()),
		Trigger: &protocol.TriggerInfo{Type: spec.Trigger.Type, Ref: spec.ID},
	}); err != nil {
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
