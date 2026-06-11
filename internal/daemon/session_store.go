package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// sessionRecordSchemaVersion is the on-disk format version. Bump it when the
// shape of sessionRecord changes incompatibly so loaders can migrate or skip.
const sessionRecordSchemaVersion = 1

// sessionRecord is the persisted, serialization-stable representation of a
// session. It is written under ~/.vix/sessions/{open,closed}/<id>.json (or the
// override dir's sessions/ in config-dir mode). It carries enough to both
// continue the conversation (messages, model, cwd, mode) and redisplay it in a
// freshly launched TUI (via replay), plus UI/telemetry niceties.
type sessionRecord struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	CWD           string `json:"cwd"`
	Model         string `json:"model"`
	ParentID      string `json:"parent_id,omitempty"`
	ForkTurnIdx   int    `json:"fork_turn_idx,omitempty"`

	SessionMode    string `json:"session_mode"`
	ActiveWorkflow string `json:"active_workflow,omitempty"`

	// WorkflowRun is the resume snapshot of an interrupted workflow run
	// (cursor, step results, per-step agent conversations, budget). Nil when
	// no run is in flight; completed runs are cleared rather than archived.
	WorkflowRun *WorkflowRunState `json:"workflow_run,omitempty"`

	Messages   []llm.MessageParam  `json:"messages"`
	TodoList   []protocol.TodoItem `json:"todo_list,omitempty"`
	ActivePlan *protocol.Plan      `json:"active_plan,omitempty"`

	StartedAt     time.Time `json:"started_at"`
	LastRequestAt time.Time `json:"last_request_at,omitempty"`

	// Provenance: empty Origin = user-started; "vix" = daemon-initiated
	// (scheduled job run, synthetic alert). Trigger records what fired it.
	Origin  string                `json:"origin,omitempty"`
	Trigger *protocol.TriggerInfo `json:"trigger,omitempty"`
	// JobStatus is the finished run's status (ok | error | timeout) for
	// vix-initiated records; empty for user sessions and synthetic alerts.
	JobStatus string `json:"job_status,omitempty"`
	// Unread is the session-global "content the user hasn't seen" flag. Set
	// on turn/run completion, cleared by session.mark_read. Absent on legacy
	// records (= read), keeping upgrades quiet.
	Unread bool `json:"unread,omitempty"`

	TotalInputTokens  int64 `json:"total_input_tokens,omitempty"`
	TotalOutputTokens int64 `json:"total_output_tokens,omitempty"`
	TotalCacheRead    int64 `json:"total_cache_read,omitempty"`
	TotalCacheWrite   int64 `json:"total_cache_write,omitempty"`
}

// sessionRecordPath returns the path of a record within dir, or "" when dir is
// empty (persistence disabled because no home/override directory is available).
func sessionRecordPath(dir, id string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, id+".json")
}

// saveSessionRecord atomically writes rec to the open/ directory. A no-op (nil)
// when persistence is disabled (Sessions() empty). The write goes to a unique
// temp file in the same directory, then renames over the target so a crash mid
// write never leaves a truncated record.
func saveSessionRecord(paths config.VixPaths, rec sessionRecord) error {
	dir := paths.SessionsOpen()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rec.SchemaVersion = sessionRecordSchemaVersion
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, rec.ID+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, sessionRecordPath(dir, rec.ID))
}

// loadOpenSessionRecord reads the record for id from the open/ directory. The
// bool reports whether a record was found. Records in closed/ are deliberately
// not consulted: attach (the only caller) must never resurrect a session the
// user explicitly closed — e.g. a stale TUI reconnect racing a quit-time close
// would otherwise re-persist the just-archived record back into open/.
func loadOpenSessionRecord(paths config.VixPaths, id string) (sessionRecord, bool, error) {
	p := sessionRecordPath(paths.SessionsOpen(), id)
	if p == "" {
		return sessionRecord{}, false, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return sessionRecord{}, false, nil
		}
		return sessionRecord{}, false, err
	}
	var rec sessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return sessionRecord{}, false, err
	}
	return rec, true, nil
}

// listOpenSessionRecords returns every parseable record in the open/ directory.
// Unreadable/corrupt files are skipped rather than failing the whole listing.
func listOpenSessionRecords(paths config.VixPaths) []sessionRecord {
	out := listSessionRecordsIn(paths.SessionsOpen())
	// Creation order (oldest first) so the TUI displays sessions in the order
	// the user started them.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// listSessionRecordsIn reads every parseable record in dir. Unreadable or
// corrupt files are skipped.
func listSessionRecordsIn(dir string) []sessionRecord {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []sessionRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec sessionRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// Job-run retention defaults: open/ keeps the newest few runs per job (the
// "latest runs at a glance" view), closed/ keeps a deeper history.
const (
	jobRunsOpenKeep   = 3
	jobRunsClosedKeep = 10
)

// sweepJobRunRecords enforces per-job retention after a run persists.
// open/: the newest jobRunsOpenKeep vix-initiated records for jobRef stay;
// older successful ones are auto-dismissed to closed/. Failed runs (job_status
// error/timeout) and synthetic alerts (no job_status) are exempt — a failure
// never slides out of sight on its own; only the user dismisses it.
// closed/: the newest jobRunsClosedKeep records for jobRef stay; older ones
// are deleted. This doubles as the job's run history depth.
func sweepJobRunRecords(paths config.VixPaths, jobRef string) {
	forJob := func(recs []sessionRecord) []sessionRecord {
		var out []sessionRecord
		for _, r := range recs {
			if r.Origin == "vix" && r.Trigger != nil && r.Trigger.Ref == jobRef {
				out = append(out, r)
			}
		}
		// Newest first.
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].StartedAt.After(out[j].StartedAt)
		})
		return out
	}

	open := forJob(listSessionRecordsIn(paths.SessionsOpen()))
	if len(open) > jobRunsOpenKeep {
		for _, r := range open[jobRunsOpenKeep:] {
			// Failures, alerts, and anything the user hasn't seen yet wait
			// for an explicit dismissal; only read, successful runs age out.
			if r.JobStatus != StatusOKRecord || r.Unread {
				continue
			}
			if err := moveSessionToClosed(paths, r.ID); err != nil {
				LogError("job retention: move %s to closed: %v", r.ID, err)
			}
		}
	}

	closed := forJob(listSessionRecordsIn(paths.SessionsClosed()))
	if len(closed) > jobRunsClosedKeep {
		for _, r := range closed[jobRunsClosedKeep:] {
			p := sessionRecordPath(paths.SessionsClosed(), r.ID)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				LogError("job retention: delete %s: %v", r.ID, err)
			}
		}
	}
}

// StatusOKRecord is the job_status value marking a successful run record.
const StatusOKRecord = "ok"

// moveSessionToClosed moves a record from open/ to closed/. It is invoked on an
// explicit user close (the "x" action), never on a bare disconnect. A no-op
// when persistence is disabled or the open record does not exist.
func moveSessionToClosed(paths config.VixPaths, id string) error {
	src := sessionRecordPath(paths.SessionsOpen(), id)
	dst := sessionRecordPath(paths.SessionsClosed(), id)
	if src == "" || dst == "" {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// deleteSessionRecord removes the record for id from both open/ and closed/.
// Used when an empty conversation is closed: there is nothing worth archiving,
// so the record is deleted instead of moved to closed/. A no-op when
// persistence is disabled or the files do not exist.
func deleteSessionRecord(paths config.VixPaths, id string) error {
	for _, dir := range []string{paths.SessionsOpen(), paths.SessionsClosed()} {
		p := sessionRecordPath(dir, id)
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// trimStaleClosedSessions deletes closed session records whose last activity
// is older than maxAge. Last activity is the record's LastRequestAt, else
// StartedAt; unparsable/corrupt files fall back to their mtime so they don't
// accumulate forever. A no-op when maxAge <= 0 (retention disabled, the
// "never" setting) or persistence is unavailable. Run once at daemon startup.
func trimStaleClosedSessions(paths config.VixPaths, maxAge time.Duration) {
	if maxAge <= 0 {
		return
	}
	dir := paths.SessionsClosed()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	trimmed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		var last time.Time
		data, err := os.ReadFile(p)
		if err == nil {
			var rec sessionRecord
			if json.Unmarshal(data, &rec) == nil {
				last = rec.lastActivity()
			}
		}
		if last.IsZero() {
			info, err := e.Info()
			if err != nil {
				continue
			}
			last = info.ModTime()
		}
		if last.After(cutoff) {
			continue
		}
		if err := os.Remove(p); err != nil {
			LogError("trim closed session %s: %v", e.Name(), err)
			continue
		}
		trimmed++
	}
	if trimmed > 0 {
		LogInfo("trimmed %d stale closed session(s) older than %s", trimmed, maxAge)
	}
}

// lastActivity returns the timestamp used to order the open list: the last
// request time if present, else the start time.
func (r sessionRecord) lastActivity() time.Time {
	if !r.LastRequestAt.IsZero() {
		return r.LastRequestAt
	}
	return r.StartedAt
}

// firstUserMessage returns a short single-line preview of the first user text
// block, used for the Sessions list "first message" column.
func (r sessionRecord) firstUserMessage() string {
	for _, m := range r.Messages {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
				return firstLine(b.Text)
			}
		}
	}
	return ""
}

// firstLine trims s to its first non-empty line, capped at 120 runes.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// summary projects a record into the lightweight shape sent to the TUI for the
// session.list response.
func (r sessionRecord) summary() protocol.SessionSummary {
	s := protocol.SessionSummary{
		ID:           r.ID,
		CWD:          r.CWD,
		Model:        r.Model,
		FirstMessage: r.firstUserMessage(),
		Origin:       r.Origin,
		Trigger:      r.Trigger,
		JobStatus:    r.JobStatus,
		Unread:       r.Unread,
	}
	if !r.StartedAt.IsZero() {
		s.StartedAt = r.StartedAt.Format(time.RFC3339)
	}
	if !r.LastRequestAt.IsZero() {
		s.LastRequestAt = r.LastRequestAt.Format(time.RFC3339)
	}
	return s
}

// buildRecord snapshots the session's current state into a serializable record.
// Each piece is read under the lock that guards it.
func (s *Session) buildRecord() sessionRecord {
	s.mu.Lock()
	msgs := make([]llm.MessageParam, len(s.messages))
	copy(msgs, s.messages)
	plan := s.activePlan
	workflowRun := s.workflowRunState
	s.mu.Unlock()

	s.todoMu.RLock()
	todos := make([]protocol.TodoItem, len(s.todoList))
	copy(todos, s.todoList)
	s.todoMu.RUnlock()

	return sessionRecord{
		ID:                s.id,
		CWD:               s.cwd,
		Model:             s.model,
		ParentID:          s.parentID,
		ForkTurnIdx:       s.forkTurnIdx,
		SessionMode:       s.sessionMode,
		ActiveWorkflow:    s.activeWorkflow,
		WorkflowRun:       workflowRun,
		Messages:          msgs,
		TodoList:          todos,
		ActivePlan:        plan,
		StartedAt:         s.startTime,
		LastRequestAt:     s.lastRequestAt,
		Origin:            s.origin,
		Trigger:           s.trigger,
		JobStatus:         s.jobStatus,
		Unread:            s.unread,
		TotalInputTokens:  s.totalInputTokens,
		TotalOutputTokens: s.totalOutputTokens,
		TotalCacheRead:    s.totalCacheRead,
		TotalCacheWrite:   s.totalCacheWrite,
	}
}

// persist writes the session's current state to the open/ directory. Best
// effort: failures are logged, never surfaced to the user. No-op when
// persistence is disabled (no home/override dir) or the session was closed by
// the user (its record now lives in closed/ and must not be resurrected).
func (s *Session) persist() {
	if s.paths.SessionsOpen() == "" || s.closedByUser {
		return
	}
	if err := saveSessionRecord(s.paths, s.buildRecord()); err != nil {
		LogError("persist session %s: %v", s.id, err)
	}
}

// seedFromRecord restores conversation state from a persisted record onto a
// freshly constructed session (used by the attach path). It does NOT restore
// the model — attach deliberately resumes on the current default and warns on
// mismatch in emitReplay.
func (s *Session) seedFromRecord(rec *sessionRecord) {
	s.messages = append([]llm.MessageParam(nil), rec.Messages...)
	s.todoList = append([]protocol.TodoItem(nil), rec.TodoList...)
	s.activePlan = rec.ActivePlan
	s.parentID = rec.ParentID
	s.forkTurnIdx = rec.ForkTurnIdx
	s.origin = rec.Origin
	s.trigger = rec.Trigger
	s.unread = rec.Unread
	s.sessionMode = rec.SessionMode
	if s.sessionMode == "" {
		s.sessionMode = "chat"
	}
	s.activeWorkflow = rec.ActiveWorkflow
	// An interrupted run persisted as "running" means the daemon died
	// mid-workflow; park it as paused so it reads correctly and resumes the
	// same way as a user-cancelled run.
	if rec.WorkflowRun != nil && rec.WorkflowRun.Status == WorkflowStatusRunning {
		rec.WorkflowRun.Status = WorkflowStatusPaused
	}
	s.workflowRunState = rec.WorkflowRun
	if !rec.StartedAt.IsZero() {
		s.startTime = rec.StartedAt
	}
	s.lastRequestAt = rec.LastRequestAt
	s.totalInputTokens = rec.TotalInputTokens
	s.totalOutputTokens = rec.TotalOutputTokens
	s.totalCacheRead = rec.TotalCacheRead
	s.totalCacheWrite = rec.TotalCacheWrite
	s.attachRecord = rec
}

// emitReplay rebuilds the client's chat viewport for an attached session and
// applies restore-time validation (model changed, workflow missing). Called
// from Run() after initBrain, when s.model and s.workflows are resolved.
func (s *Session) emitReplay() {
	rec := s.attachRecord
	if rec == nil {
		return
	}
	s.attachRecord = nil

	var warnings []string

	// Model: attach resumes on the current default (s.model, resolved by
	// initBrain). Warn if the saved model differed.
	if rec.Model != "" && rec.Model != s.model {
		warnings = append(warnings, fmt.Sprintf("This conversation was saved with model %q; switched to your current default %q.", rec.Model, s.model))
	}

	// Workflow: if the saved workflow no longer exists, fall back to chat mode.
	if s.sessionMode == "workflow" && s.activeWorkflow != "" {
		found := false
		for _, w := range s.snapshotWorkflows() {
			if w.Name == s.activeWorkflow {
				found = true
				break
			}
		}
		if !found {
			warnings = append(warnings, fmt.Sprintf("Workflow %q no longer exists; this session has been switched to chat mode.", s.activeWorkflow))
			s.sessionMode = "chat"
			s.activeWorkflow = ""
			s.setWorkflowRunState(nil)
		}
	}

	// Interrupted workflow run: offer to pick it up from its cursor. Sending
	// any message while this workflow is active dispatches session.workflow,
	// which detects the saved state and resumes instead of restarting.
	if st := s.snapshotWorkflowRunState(); st != nil && st.Resumable() && s.activeWorkflow == st.Name {
		warnings = append(warnings, fmt.Sprintf(
			"Workflow %q was interrupted at step '%s' (iteration %d). Send a message to resume it, or /clear to discard the run.",
			st.Name, st.CurrentRef.ID, st.Iteration))
	}

	s.mu.Lock()
	msgs := make([]llm.MessageParam, len(s.messages))
	copy(msgs, s.messages)
	plan := s.activePlan
	s.mu.Unlock()

	s.todoMu.RLock()
	todos := make([]protocol.TodoItem, len(s.todoList))
	copy(todos, s.todoList)
	s.todoMu.RUnlock()

	s.emit("event.replay", protocol.EventReplay{
		Messages:       buildReplayMessages(msgs),
		Todos:          todos,
		ActivePlan:     plan,
		Model:          s.model,
		SessionMode:    s.sessionMode,
		ActiveWorkflow: s.activeWorkflow,
		Warnings:       warnings,
	})

	// Persist any fallback (mode/model) so the on-disk record reflects reality.
	s.persist()
}

// buildReplayMessages projects llm history into the wire-stable replay shape.
// Empty assistant/user messages (no renderable blocks) are skipped.
func buildReplayMessages(msgs []llm.MessageParam) []protocol.ReplayMessage {
	out := make([]protocol.ReplayMessage, 0, len(msgs))
	for _, m := range msgs {
		rm := protocol.ReplayMessage{Role: string(m.Role)}
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockText:
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				rm.Blocks = append(rm.Blocks, protocol.ReplayBlock{Kind: "text", Text: b.Text})
			case llm.BlockThinking:
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				rm.Blocks = append(rm.Blocks, protocol.ReplayBlock{Kind: "thinking", Text: b.Text})
			case llm.BlockToolUse:
				rm.Blocks = append(rm.Blocks, protocol.ReplayBlock{
					Kind:     "tool_use",
					ToolID:   b.ID,
					ToolName: b.Name,
					Input:    b.Input,
				})
			case llm.BlockToolResult:
				rm.Blocks = append(rm.Blocks, protocol.ReplayBlock{
					Kind:    "tool_result",
					ToolID:  b.ToolUseID,
					Output:  b.Output,
					IsError: b.IsError,
				})
			}
		}
		if len(rm.Blocks) > 0 {
			out = append(out, rm)
		}
	}
	return out
}
