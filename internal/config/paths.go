package config

import "path/filepath"

// VixPaths resolves all .vix-relative filesystem paths for a thread.
//
// When Override is set, every path resolves under the override directory and
// neither ~/.vix nor cwd/.vix is consulted. This enables fully isolated runs
// that ignore the user's and project's real configuration.
//
// When Override is empty (normal mode), Layers() returns [home, cwd/.vix] so
// callers can merge home-level defaults with project-level overrides.
type VixPaths struct {
	override string
	home     string
	cwd      string
}

// NewVixPaths constructs a resolver. override may be empty (normal mode).
// home should be the result of HomeVixDir() (may be empty if UserHomeDir fails).
// cwd is the thread working directory.
func NewVixPaths(override, home, cwd string) VixPaths {
	return VixPaths{override: override, home: home, cwd: cwd}
}

// Override returns the override directory, or "" if not set.
func (p VixPaths) Override() string { return p.override }

// IsOverride reports whether the thread is running in config-dir override mode.
func (p VixPaths) IsOverride() bool { return p.override != "" }

// Home returns the home .vix directory. Empty in override mode or if unavailable.
func (p VixPaths) Home() string {
	if p.override != "" {
		return ""
	}
	return p.home
}

// Project returns the project-level .vix directory. Empty in override mode.
func (p VixPaths) Project() string {
	if p.override != "" {
		return ""
	}
	return filepath.Join(p.cwd, ".vix")
}

// Layers returns the ordered list of .vix root directories to read from.
// Override mode: [override]
// Normal mode:   [home, cwd/.vix] (home first, later entries override earlier)
// Empty entries (e.g. unavailable home) are filtered out.
func (p VixPaths) Layers() []string {
	if p.override != "" {
		return []string{p.override}
	}
	var out []string
	if p.home != "" {
		out = append(out, p.home)
	}
	out = append(out, filepath.Join(p.cwd, ".vix"))
	return out
}

// ConfigDir returns the directory holding the split config files
// (workflow.json, languages.json). Override mode: override/config. Normal
// mode: home/config (home-only by design — these files are not layered with
// the project directory). Empty when home is unavailable in normal mode.
func (p VixPaths) ConfigDir() string {
	if p.override != "" {
		return filepath.Join(p.override, "config")
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "config")
}

// WorkflowsFile returns the path to workflow.json, or "" if unavailable.
func (p VixPaths) WorkflowsFile() string {
	dir := p.ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "workflow.json")
}

// LanguagesFile returns the path to languages.json, or "" if unavailable.
func (p VixPaths) LanguagesFile() string {
	dir := p.ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "languages.json")
}

// Settings returns the settings.json paths to merge, in load order.
func (p VixPaths) Settings() []string {
	layers := p.Layers()
	out := make([]string, len(layers))
	for i, d := range layers {
		out[i] = filepath.Join(d, "settings.json")
	}
	return out
}

// Providers returns the providers.json paths to merge, in load order (later
// overrides earlier). These overlay the binary's embedded providers.json.
func (p VixPaths) Providers() []string {
	layers := p.Layers()
	out := make([]string, len(layers))
	for i, d := range layers {
		out[i] = filepath.Join(d, "providers.json")
	}
	return out
}

// Agents returns the agents/ directories to scan, in load order (later wins).
func (p VixPaths) Agents() []string {
	return p.subdirs("agents")
}

// Skills returns the skills/ directories to scan, in load order.
func (p VixPaths) Skills() []string {
	return p.subdirs("skills")
}

// Plugins returns the plugins/ directories to scan, in load order.
func (p VixPaths) Plugins() []string { return p.subdirs("plugins") }

// ClaudeMD returns the CLAUDE.md paths to load, in order.
// Normal mode also includes the project root CLAUDE.md (outside .vix).
func (p VixPaths) ClaudeMD() []string {
	if p.override != "" {
		return []string{filepath.Join(p.override, "CLAUDE.md")}
	}
	var out []string
	if p.home != "" {
		out = append(out, filepath.Join(p.home, "CLAUDE.md"))
	}
	out = append(out, filepath.Join(p.cwd, "CLAUDE.md"))
	return out
}

// AgentsMD returns the AGENTS.md paths to load, in order.
// Normal mode also includes the project root AGENTS.md (outside .vix).
func (p VixPaths) AgentsMD() []string {
	if p.override != "" {
		return []string{filepath.Join(p.override, "AGENTS.md")}
	}
	var out []string
	if p.home != "" {
		out = append(out, filepath.Join(p.home, "AGENTS.md"))
	}
	out = append(out, filepath.Join(p.cwd, "AGENTS.md"))
	return out
}

// Primary returns the write target for thread-scoped state (history, plans,
// access stats when override is set, etc.). Override mode: override.
// Normal mode: cwd/.vix.
func (p VixPaths) Primary() string {
	if p.override != "" {
		return p.override
	}
	return filepath.Join(p.cwd, ".vix")
}

// Logs returns where LLM logs should be written for this thread.
// Override mode: override/logs. Normal mode: home/logs (or "" if home empty).
func (p VixPaths) Logs() string {
	if p.override != "" {
		return filepath.Join(p.override, "logs")
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "logs")
}

// JobsLog returns the directory holding append-only job-run logs
// (<date>.jsonl files), a subdirectory of Logs(). Empty when Logs() is empty
// (home unavailable in normal mode).
func (p VixPaths) JobsLog() string {
	base := p.Logs()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "jobs")
}

// HooksLog returns the directory holding append-only hook-run logs
// (<date>.jsonl files), a subdirectory of Logs(). Empty when Logs() is empty.
func (p VixPaths) HooksLog() string {
	base := p.Logs()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "hooks")
}

// Threads returns the directory where persisted thread records live.
// Threads are stored globally (not project-scoped): override mode uses
// override/threads; normal mode uses home/threads (empty if home is
// unavailable). Each record carries its own cwd so the daemon can filter the
// open list by the launching project.
func (p VixPaths) Threads() string {
	if p.override != "" {
		return filepath.Join(p.override, "threads")
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "threads")
}

// LegacyThreads returns the pre-rename directory ("sessions") that threads used
// to live in, mirroring Threads()'s override/home resolution. Empty when
// Threads() is empty. Used only by the one-time startup migration that renames
// the old sessions/ tree to threads/.
func (p VixPaths) LegacyThreads() string {
	if p.override != "" {
		return filepath.Join(p.override, "sessions")
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "sessions")
}

// ThreadsOpen returns the subdirectory holding open (TUI-visible) threads.
// Empty when Threads() is empty.
func (p VixPaths) ThreadsOpen() string {
	base := p.Threads()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "open")
}

// ThreadsClosed returns the subdirectory holding closed threads (retained on
// disk but not reopened on launch). Empty when Threads() is empty.
func (p VixPaths) ThreadsClosed() string {
	base := p.Threads()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "closed")
}

// AccessStatsDB returns the sqlite path for per-thread tool access stats.
// Override mode: override/access_stats.db.
// Normal mode:   cwd/.vix/access_stats.db.
func (p VixPaths) AccessStatsDB() string {
	return filepath.Join(p.Primary(), "access_stats.db")
}

// History returns the TUI input history file path.
func (p VixPaths) History() string {
	return filepath.Join(p.Primary(), "history.txt")
}

// Plans returns the plans/ directory path.
func (p VixPaths) Plans() string {
	return filepath.Join(p.Primary(), "plans")
}

// Brain returns the brain index directory.
// Override mode: override (brain lives directly in the override root).
// Normal mode:   cwd/.vix.
func (p VixPaths) Brain() string {
	return p.Primary()
}

// ProjectSettingsWrite returns the settings.json path to use for persisting
// project-level edits (e.g. appending allowed directories). Override mode
// writes to the override dir; normal mode writes to cwd/.vix.
func (p VixPaths) ProjectSettingsWrite() string {
	return filepath.Join(p.Primary(), "settings.json")
}

// StateFile returns the path to the global thread-state file (state.json),
// used for cross-thread bookkeeping that is not project-scoped — e.g. the
// once-per-day update-check record. Override mode: override/state.json. Normal
// mode: home/state.json (empty when home is unavailable).
func (p VixPaths) StateFile() string {
	if p.override != "" {
		return filepath.Join(p.override, "state.json")
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "state.json")
}

// Jobs returns the directory holding scheduled job specs (<id>.json files).
// Jobs are user-global like threads — each spec carries its own cwd — so the
// store lives next to threads/: override mode uses override/jobs; normal mode
// uses home/jobs (empty when home is unavailable, which disables the scheduler).
func (p VixPaths) Jobs() string {
	if p.override != "" {
		return filepath.Join(p.override, "jobs")
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "jobs")
}

// JobState returns the path of one job's machine-written runtime state file
// (next/last run times, statuses, error counters), kept separate from the
// user-authored spec (job.json) so spec files never churn. It lives inside the
// job's own subdirectory, a sibling of job.json: override mode:
// override/jobs/<id>/state.json; normal mode: home/jobs/<id>/state.json. Empty
// when the jobs directory is unavailable (no home directory).
func (p VixPaths) JobState(id string) string {
	dir := p.Jobs()
	if dir == "" || id == "" {
		return ""
	}
	return filepath.Join(dir, id, "state.json")
}

// Hooks returns the directory holding lifecycle-hook specs (<id>.json files).
// Hooks are user-global like jobs — each spec carries its own cwd — so the
// store lives next to jobs/: override mode uses override/hooks; normal mode
// uses home/hooks (empty when home is unavailable, which disables the engine).
func (p VixPaths) Hooks() string {
	if p.override != "" {
		return filepath.Join(p.override, "hooks")
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "hooks")
}

// HookState returns the path of one hook's machine-written runtime state file
// (recent-fire history and last-fire summary), kept separate from the
// user-authored spec (hook.json) so spec files never churn. It lives inside the
// hook's own subdirectory, a sibling of hook.json: override mode:
// override/hooks/<id>/state.json; normal mode: home/hooks/<id>/state.json.
// Empty when the hooks directory is unavailable (no home directory).
func (p VixPaths) HookState(id string) string {
	dir := p.Hooks()
	if dir == "" || id == "" {
		return ""
	}
	return filepath.Join(dir, id, "state.json")
}

// HeartbeatMD returns the path of the heartbeat whiteboard file read by the
// default heartbeat job's prompt. It lives inside the heartbeat job's own
// subdirectory, a sibling of its job.json: override mode:
// override/jobs/heartbeat/heartbeat.md; normal mode:
// home/jobs/heartbeat/heartbeat.md. Empty when the jobs directory is
// unavailable (no home directory).
func (p VixPaths) HeartbeatMD() string {
	dir := p.Jobs()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "heartbeat", "heartbeat.md")
}

func (p VixPaths) subdirs(name string) []string {
	layers := p.Layers()
	out := make([]string, len(layers))
	for i, d := range layers {
		out[i] = filepath.Join(d, name)
	}
	return out
}
