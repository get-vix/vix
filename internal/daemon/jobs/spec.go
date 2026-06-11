// Package jobs implements vixd's scheduled-jobs engine: user-authored job
// specs (~/.vix/jobs/<id>.json) fired by a single timer loop, each run
// executing a prompt — optionally through a workflow — in an isolated session.
//
// The package owns scheduling, persistence, and policy (catch-up, backoff,
// auto-disable); actual execution is delegated to a Runner installed by the
// daemon package, keeping the dependency direction daemon → jobs.
package jobs

import (
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
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

// Permissions maps onto the session's automatic-permission flags. Pointers so
// "absent" defaults to true (scheduled runs are unattended; the deny list and
// workflow deny_tools remain the brakes).
type Permissions struct {
	AutoWrite *bool `json:"auto_write,omitempty"`
	AutoDirs  *bool `json:"auto_dirs,omitempty"`
}

// Spec is a user-authored job definition, one JSON file per job under
// ~/.vix/jobs/. Mutable runtime state lives separately (State) so these files
// never churn.
type Spec struct {
	ID          string      `json:"id"`
	Name        string      `json:"name,omitempty"`
	Enabled     bool        `json:"enabled"`
	Trigger     Trigger     `json:"trigger"`
	Prompt      string      `json:"prompt"`             // required; supports $(file:path)
	Workflow    string      `json:"workflow,omitempty"` // empty = plain chat turn with the general agent
	CWD         string      `json:"cwd,omitempty"`
	Permissions Permissions `json:"permissions,omitempty"`
	SkipIfEmpty bool        `json:"skip_if_empty,omitempty"`
	Timeout     string      `json:"timeout,omitempty"` // Go duration, default 10m
	CreatedBy   string      `json:"created_by,omitempty"`
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
	return nil
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
