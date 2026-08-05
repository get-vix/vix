package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/daemon/jobs"
)

// Append-only run logs for scheduled jobs and lifecycle hooks. Each subsystem
// writes one JSON object per line to a daily file under ~/.vix/logs/{jobs,hooks}/
// (<UTC-date>.jsonl). Three line shapes share a "phase" field:
//
//	jobs:  started | error | finished        (correlate by job_id / thread_id)
//	hooks: fired   | error | finished        (correlate by fire_id)
//
// Writes are best-effort and never propagate failures into a run: a broken or
// unavailable log directory must not wedge a job or hook. Whole daily files are
// pruned by sweepRunLogs once they age past the configured retention.

// runLogMu serializes appends so concurrent job runs / hook fires don't
// interleave partial lines in the same daily file.
var runLogMu sync.Mutex

// runLogFileRe matches a daily run-log filename, capturing its date.
var runLogFileRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)

// appendRunLog appends one JSON line to dir/<UTC-date>.jsonl, stamping "ts" when
// absent. Best-effort: a nil/empty dir or any I/O error is swallowed so logging
// never breaks the caller.
func appendRunLog(dir string, entry map[string]any) {
	defer func() { _ = recover() }()
	if dir == "" {
		return
	}
	if _, ok := entry["ts"]; !ok {
		entry["ts"] = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}

	runLogMu.Lock()
	defer runLogMu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// sweepRunLogs deletes <date>.jsonl files in dir whose filename date is older
// than retentionDays (UTC). retentionDays <= 0 disables the sweep (keep
// forever); a missing dir is a no-op. Best-effort.
func sweepRunLogs(dir string, retentionDays int) {
	if dir == "" || retentionDays <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -retentionDays)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := runLogFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", m[1], time.UTC)
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// runLogSweepLoop prunes job/hook run logs older than the configured retention
// at startup, then every 24h until ctx is cancelled. A retention of <= 0 (or an
// unavailable home dir) makes each sweep a no-op.
func (s *Server) runLogSweepLoop(ctx context.Context) {
	sweep := func() {
		days := config.LogRetentionDays()
		sweepRunLogs(s.jobsLogDir(), days)
		sweepRunLogs(s.hooksLogDir(), days)
	}
	sweep()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// jobsLogDir / hooksLogDir resolve the run-log directories from the daemon's
// home .vix dir, matching how the hook/job stores are resolved at startup.
func (s *Server) jobsLogDir() string {
	return config.NewVixPaths("", s.homeVixDir, "").JobsLog()
}

func (s *Server) hooksLogDir() string {
	return config.NewVixPaths("", s.homeVixDir, "").HooksLog()
}

// newFireID returns a short correlation id tying a hook's fired/error/finished
// run-log lines together.
func newFireID() string {
	id := generateThreadID()
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// --- hook run-log helpers --------------------------------------------------

// hookKind classifies a hook by its action form for the run log.
func hookKind(spec hooks.Spec) string {
	switch {
	case spec.Command != "":
		return "command"
	case spec.WorkflowID != "" || spec.Workflow != nil:
		return "workflow"
	default:
		return "prompt"
	}
}

// logHookFired records a hook firing (one per matched hook, sync or async).
func (s *Server) logHookFired(fireID string, spec hooks.Spec, async bool, base map[string]any) {
	entry := map[string]any{
		"phase":    "fired",
		"fire_id":  fireID,
		"hook_id":  spec.ID,
		"name":     hookName(spec),
		"event":    spec.Trigger.Event,
		"matcher":  spec.Trigger.Matcher,
		"blocking": spec.Blocking,
		"async":    async,
		"kind":     hookKind(spec),
	}
	if v, ok := base["thread_id"].(string); ok {
		entry["thread_id"] = v
	}
	if v, ok := base["tool_name"].(string); ok {
		entry["tool_name"] = v
	}
	appendRunLog(s.hooksLogDir(), entry)
}

// logHookError records an error encountered while executing a hook. source names
// where it came from (command_exec, agent, start_refused, timeout).
func (s *Server) logHookError(fireID string, spec hooks.Spec, source, msg string, exitCode int) {
	entry := map[string]any{
		"phase":   "error",
		"fire_id": fireID,
		"hook_id": spec.ID,
		"event":   spec.Trigger.Event,
		"source":  source,
		"error":   msg,
	}
	if source == "command_exec" {
		entry["exit_code"] = exitCode
	}
	appendRunLog(s.hooksLogDir(), entry)
	LogError("hook %q: %s: %s", spec.ID, source, msg)
}

// logHookFinished records a hook completing, with its resolved status.
func (s *Server) logHookFinished(fireID string, spec hooks.Spec, status string, dur time.Duration) {
	if status == "" {
		status = "allow"
	}
	appendRunLog(s.hooksLogDir(), map[string]any{
		"phase":       "finished",
		"fire_id":     fireID,
		"hook_id":     spec.ID,
		"event":       spec.Trigger.Event,
		"status":      status,
		"duration_ms": dur.Milliseconds(),
	})
}

// --- job run-log adapter ---------------------------------------------------

// jobRunLogger implements jobs.RunLogger by appending to the jobs run-log
// directory. It is injected into the scheduler so every job-run lifecycle line
// (started/finished) and scheduler-origin error (prompt_resolve, auto_disable)
// plus the runner's in-run errors (carried back via RunResult.Errors) land in
// one place.
type jobRunLogger struct {
	dir func() string
}

func (l jobRunLogger) Started(spec jobs.Spec) {
	appendRunLog(l.dir(), map[string]any{
		"phase":       "started",
		"job_id":      spec.ID,
		"name":        spec.Name,
		"trigger":     spec.Trigger.Type,
		"cwd":         spec.CWD,
		"workflow_id": spec.WorkflowID,
	})
}

func (l jobRunLogger) Error(spec jobs.Spec, threadID, source, msg string) {
	entry := map[string]any{
		"phase":  "error",
		"job_id": spec.ID,
		"source": source,
		"error":  msg,
	}
	if threadID != "" {
		entry["thread_id"] = threadID
	}
	appendRunLog(l.dir(), entry)
	LogError("job %q: %s: %s", spec.ID, source, msg)
}

func (l jobRunLogger) Finished(spec jobs.Spec, res jobs.RunResult, dur time.Duration) {
	entry := map[string]any{
		"phase":       "finished",
		"job_id":      spec.ID,
		"name":        spec.Name,
		"status":      res.Status,
		"error":       res.Err,
		"agent_turns": res.AgentTurns,
		"duration_ms": dur.Milliseconds(),
	}
	if res.ThreadID != "" {
		entry["thread_id"] = res.ThreadID
	}
	if len(res.Denials) > 0 {
		entry["denials"] = res.Denials
	}
	appendRunLog(l.dir(), entry)
}
