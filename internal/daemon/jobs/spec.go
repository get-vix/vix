// Package jobs implements vixd's scheduled-jobs engine: user-authored job
// specs (~/.vix/jobs/<id>/job.json) fired by a single timer loop, each run
// executing a prompt — optionally through a workflow — in an isolated thread.
//
// The package owns scheduling, persistence, and policy (catch-up, backoff,
// auto-disable); actual execution is delegated to a Runner installed by the
// daemon package, keeping the dependency direction daemon → jobs.
package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"

	"github.com/get-vix/vix/internal/workflow"
)

// DefaultTimeout bounds a job run's wall-clock time when the spec does not set
// one.
const DefaultTimeout = 10 * time.Minute

// cronParser accepts standard 5-field cron expressions plus descriptors
// (@every 30m, @daily, @hourly). Matches robfig's cron.New default.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Trigger describes when a job fires. Exactly one shape is valid:
//   - {type:"cron", expr:"*/30 9-19 * * *" | "@every 1m", tz:"Europe/Paris"}
//   - {type:"at",   time:"2026-01-10T09:00:00Z"}
//
// There is no interval type and no active-hours field: the cron hour field
// expresses windows natively, and @every covers plain intervals.
type Trigger struct {
	Type string `json:"type"`
	Expr string `json:"expr,omitempty"`
	TZ   string `json:"tz,omitempty"`
	Time string `json:"time,omitempty"` // RFC3339, type "at" only
}

// Permissions maps onto the thread's automatic-permission flags. Pointers so
// "absent" defaults to true (scheduled runs are unattended; the deny list and
// workflow deny_tools remain the brakes).
type Permissions struct {
	AutoWrite *bool `json:"auto_write,omitempty"`
	AutoDirs  *bool `json:"auto_dirs,omitempty"`
}

// SpecFile is a helper file materialized into the job's own directory (a
// sibling of job.json) when the spec is saved or loaded. It lets a
// self-contained job ship, declaratively, the scripts/assets its workflow needs
// at runtime (referenced via $(workflow.dir)/<path>) without a separate install
// step. Writes are confined to the job directory (see Validate); the spec is the
// source of truth, so an on-disk copy is (re)written whenever the content
// differs.
type SpecFile struct {
	Path    string `json:"path"`           // relative to the job dir; no "..", no absolute
	Content string `json:"content"`        // exact bytes to write
	Mode    string `json:"mode,omitempty"` // octal (e.g. "0755"); default "0644"
}

// FileMode returns the parsed permission bits, defaulting to 0644 when unset or
// unparseable (Validate rejects an unparseable mode up front).
func (f SpecFile) FileMode() os.FileMode {
	if f.Mode == "" {
		return 0o644
	}
	m, err := strconv.ParseUint(f.Mode, 8, 32)
	if err != nil {
		return 0o644
	}
	return os.FileMode(m)
}

// Spec is a user-authored job definition, one job.json per job under
// ~/.vix/jobs/<id>/. Mutable runtime state lives separately (State) so these
// files never churn.
type Spec struct {
	ID      string  `json:"id"`
	Name    string  `json:"name,omitempty"`
	Enabled bool    `json:"enabled"`
	Trigger Trigger `json:"trigger"`
	Prompt  string  `json:"prompt"` // required; supports $(file:path)
	// At most one of WorkflowID / Workflow may be set:
	//   - WorkflowID names a workflow defined in config/workflow.json.
	//   - Workflow is a self-contained inline definition (no separate file).
	// Both empty = plain chat turn with the general agent.
	WorkflowID  string        `json:"workflow_id,omitempty"`
	Workflow    *workflow.Def `json:"workflow,omitempty"`
	CWD         string        `json:"cwd,omitempty"`
	Permissions Permissions   `json:"permissions,omitempty"`
	SkipIfEmpty bool          `json:"skip_if_empty,omitempty"`
	Timeout     string        `json:"timeout,omitempty"` // Go duration, default 10m
	CreatedBy   string        `json:"created_by,omitempty"`
	// Files are helper files materialized into the job's own directory on save
	// and load (see SpecFile).
	Files []SpecFile `json:"files,omitempty"`
}

// Validate reports the first problem with the spec, or nil.
func (s *Spec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("missing id")
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("missing prompt")
	}
	if s.CWD == "" {
		return fmt.Errorf("missing cwd")
	}
	// At most one of workflow_id / workflow; an inline workflow must itself be
	// structurally valid (named, with consistent steps) so problems surface at
	// load time rather than mid-run.
	if s.WorkflowID != "" && s.Workflow != nil {
		return fmt.Errorf("set only one of workflow_id or workflow, not both")
	}
	if s.Workflow != nil {
		if err := workflow.Validate(s.Workflow); err != nil {
			return fmt.Errorf("inline workflow: %w", err)
		}
	}
	switch s.Trigger.Type {
	case "cron":
		if s.Trigger.Expr == "" {
			return fmt.Errorf("cron trigger: missing expr")
		}
		if s.Trigger.Time != "" {
			return fmt.Errorf("cron trigger: unexpected time field")
		}
		if _, err := s.schedule(); err != nil {
			return fmt.Errorf("cron trigger: %w", err)
		}
	case "at":
		if s.Trigger.Time == "" {
			return fmt.Errorf("at trigger: missing time")
		}
		if s.Trigger.Expr != "" || s.Trigger.TZ != "" {
			return fmt.Errorf("at trigger: unexpected expr/tz field")
		}
		if _, err := time.Parse(time.RFC3339, s.Trigger.Time); err != nil {
			return fmt.Errorf("at trigger: invalid time (want RFC3339): %w", err)
		}
	default:
		return fmt.Errorf("unknown trigger type %q (want \"cron\" or \"at\")", s.Trigger.Type)
	}
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("invalid timeout: must be positive")
		}
	}
	seen := make(map[string]bool, len(s.Files))
	for _, f := range s.Files {
		rel, err := safeSpecFilePath(f.Path)
		if err != nil {
			return err
		}
		if seen[rel] {
			return fmt.Errorf("file %q: duplicate path", f.Path)
		}
		seen[rel] = true
		if f.Mode != "" {
			if _, err := strconv.ParseUint(f.Mode, 8, 32); err != nil {
				return fmt.Errorf("file %q: invalid mode %q (want octal like \"0755\")", f.Path, f.Mode)
			}
		}
	}
	return nil
}

// safeSpecFilePath cleans a declared file path and confines it to the job
// directory: it must be non-empty, relative, and must not escape via "..".
// Returns the cleaned relative path.
func safeSpecFilePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("file: missing path")
	}
	clean := filepath.Clean(p)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("file %q: path must be relative", p)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file %q: path escapes the job directory", p)
	}
	return clean, nil
}

// schedule parses the cron expression with its timezone. Only valid for
// trigger type "cron".
func (s *Spec) schedule() (cron.Schedule, error) {
	expr := s.Trigger.Expr
	if s.Trigger.TZ != "" && !strings.HasPrefix(expr, "@") {
		expr = "TZ=" + s.Trigger.TZ + " " + expr
	}
	return cronParser.Parse(expr)
}

// NextRun returns the first fire time strictly after t. ok is false when the
// job has no future fire (an "at" whose time is not after t).
func (s *Spec) NextRun(t time.Time) (next time.Time, ok bool) {
	switch s.Trigger.Type {
	case "cron":
		sched, err := s.schedule()
		if err != nil {
			return time.Time{}, false
		}
		return sched.Next(t), true
	case "at":
		at, err := time.Parse(time.RFC3339, s.Trigger.Time)
		if err != nil || !at.After(t) {
			return time.Time{}, false
		}
		return at, true
	}
	return time.Time{}, false
}

// AtTime returns the one-shot fire time for an "at" trigger (zero otherwise).
func (s *Spec) AtTime() time.Time {
	if s.Trigger.Type != "at" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, s.Trigger.Time)
	if err != nil {
		return time.Time{}
	}
	return at
}

// TimeoutDuration returns the per-run wall-clock budget.
func (s *Spec) TimeoutDuration() time.Duration {
	if s.Timeout == "" {
		return DefaultTimeout
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil || d <= 0 {
		return DefaultTimeout
	}
	return d
}

// AutoWrite reports the effective auto_write permission (default true).
func (s *Spec) AutoWrite() bool {
	if s.Permissions.AutoWrite == nil {
		return true
	}
	return *s.Permissions.AutoWrite
}

// AutoDirs reports the effective auto_dirs permission (default true).
func (s *Spec) AutoDirs() bool {
	if s.Permissions.AutoDirs == nil {
		return true
	}
	return *s.Permissions.AutoDirs
}
