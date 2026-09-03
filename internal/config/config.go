package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Config holds client configuration.
type Config struct {
	Model      string
	CWD        string
	Workdir    string
	ConfigDir  string // absolute path, or "" for default ~/.vix + ./.vix behavior
	Paths      VixPaths
	ForceInit  bool
	SocketPath string
}

// Load reads configuration from environment variables.
// The API key is no longer needed on the client side — the daemon handles it.
// If workdir is non-empty, it is resolved to an absolute path and used as the
// thread working directory instead of os.Getwd().
// If configDir is non-empty, it is resolved to an absolute path and used as
// the sole .vix config root (ignoring ~/.vix and ./.vix).
// If socketPath is empty, /tmp/vixd.sock is used.
func Load(forceInit bool, workdir, configDir, socketPath string) (*Config, error) {
	// Model selection now lives in the active chat agent's `model:` YAML
	// frontmatter (resolved per-thread in the daemon). The Config.Model
	// field is left as a final fallback only — see thread.go for the
	// resolution chain.
	const model = "anthropic/claude-sonnet-4-6"

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot determine working directory: %w", err)
	}

	if workdir != "" {
		abs, err := filepath.Abs(workdir)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve workdir %q: %w", workdir, err)
		}
		cwd = abs
	}

	if configDir != "" {
		abs, err := filepath.Abs(configDir)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve config-dir %q: %w", configDir, err)
		}
		configDir = abs
	}

	if socketPath == "" {
		socketPath = "/tmp/vixd.sock"
	}

	return &Config{
		Model:      model,
		CWD:        cwd,
		Workdir:    workdir,
		ConfigDir:  configDir,
		Paths:      NewVixPaths(configDir, HomeVixDir(), cwd),
		ForceInit:  forceInit,
		SocketPath: socketPath,
	}, nil
}

// HomeVixDir returns the path to ~/.vix/.
func HomeVixDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".vix")
}

// DaemonConfig holds daemon-side configuration.
type DaemonConfig struct {
	HomeVixDir string
	// AuthToken is the shared-secret string the daemon will require on every
	// incoming socket message. Loaded from the file pointed at by vixd's
	// -auth-token-path flag (cmd/vixd/main.go). Empty means "no auth check"
	// — that mode exists for in-process tests and trusted-host embeddings;
	// production deployments always populate it.
	AuthToken string
}

// ToolsConfig holds tool backend configuration.
type ToolsConfig struct {
	Grep ToolBackendConfig `json:"grep"`
	Glob ToolBackendConfig `json:"glob"`
}

// ToolBackendConfig holds a single tool's backend selection.
type ToolBackendConfig struct {
	Backend string `json:"backend"`
}

// LoadDaemonConfig loads daemon configuration with defaults. version is the
// running binary's build version, used to refresh managed defaults in ~/.vix
// when it changes between runs.
func LoadDaemonConfig(version string) (*DaemonConfig, error) {
	homeDir := HomeVixDir()
	if homeDir != "" {
		os.MkdirAll(homeDir, 0o755)
		if err := BootstrapHomeVixDir(homeDir, version); err != nil {
			log.Printf("[config] bootstrap failed: %v", err)
		}
	}

	return &DaemonConfig{
		HomeVixDir: homeDir,
	}, nil
}

// feature reads a boolean feature flag from ~/.vix/settings.json, returning
// def when the file is missing, unparsable, or the flag is absent.
func feature(name string, def bool) bool {
	if v, ok := featureRaw(name); ok {
		return v
	}
	return def
}

// featureRaw reads a boolean feature flag from ~/.vix/settings.json, reporting
// whether the key was present. Used to distinguish "explicitly set" from
// "absent" (e.g. for legacy-key fallbacks).
func featureRaw(name string) (bool, bool) {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return false, false
	}
	var cfg struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, false
	}
	v, ok := cfg.Features[name]
	return v, ok
}

// setFeature writes a boolean feature flag to ~/.vix/settings.json, preserving
// other top-level keys (theme, other features, etc).
func setFeature(name string, v bool) error {
	home := HomeVixDir()
	if home == "" {
		return fmt.Errorf("no home directory")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	p := filepath.Join(home, "settings.json")

	raw := map[string]any{}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &raw)
	}

	features, _ := raw["features"].(map[string]any)
	if features == nil {
		features = map[string]any{}
	}
	features[name] = v
	raw["features"] = features

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

// TelemetryEnabled reads the telemetry feature flag from ~/.vix/settings.json.
// Returns true if the flag is absent (opt-out model).
func TelemetryEnabled() bool { return feature("telemetry", true) }

// SetTelemetryEnabled writes the telemetry feature flag to ~/.vix/settings.json.
func SetTelemetryEnabled(v bool) error { return setFeature("telemetry", v) }

// UpdateCheckEnabled reads the update_check feature flag from
// ~/.vix/settings.json. Returns true if the flag is absent (opt-out model): the
// daemon checks GitHub for newer releases at most once per day unless disabled.
func UpdateCheckEnabled() bool { return feature("update_check", true) }

// SetUpdateCheckEnabled writes the update_check feature flag to
// ~/.vix/settings.json.
func SetUpdateCheckEnabled(v bool) error { return setFeature("update_check", v) }

// ShowThinking reads the show_thinking feature flag from ~/.vix/settings.json.
// Returns false if the flag is absent (opt-in: thinking is hidden by default).
func ShowThinking() bool { return feature("show_thinking", false) }

// SetShowThinking writes the show_thinking feature flag to ~/.vix/settings.json.
func SetShowThinking(v bool) error { return setFeature("show_thinking", v) }

// CloseAllThreadsOnQuit reads the close_all_threads_on_quit feature flag.
// Defaults to false: quitting vix leaves all thread records open so they are
// restored on next launch. When true, quitting explicitly closes every thread.
// The pre-rename key close_all_sessions_on_quit is honored as a fallback so an
// existing settings.json keeps working.
func CloseAllThreadsOnQuit() bool {
	if v, ok := featureRaw("close_all_threads_on_quit"); ok {
		return v
	}
	return feature("close_all_sessions_on_quit", false)
}

// SetCloseAllThreadsOnQuit writes the close_all_threads_on_quit feature flag.
func SetCloseAllThreadsOnQuit(v bool) error { return setFeature("close_all_threads_on_quit", v) }

// ReadAgentsMD reads the read_agents_md feature flag. Defaults to false.
func ReadAgentsMD() bool { return feature("read_agents_md", false) }

// SetReadAgentsMD writes the read_agents_md feature flag.
func SetReadAgentsMD(v bool) error { return setFeature("read_agents_md", v) }

// ReadClaudeMD reads the read_claude_md feature flag. Defaults to false.
func ReadClaudeMD() bool { return feature("read_claude_md", false) }

// SetReadClaudeMD writes the read_claude_md feature flag.
func SetReadClaudeMD(v bool) error { return setFeature("read_claude_md", v) }

// ToolOrchestrator reads the tool_orchestrator feature flag. Defaults to false.
func ToolOrchestrator() bool { return feature("tool_orchestrator", false) }

// SetToolOrchestrator writes the tool_orchestrator feature flag.
func SetToolOrchestrator(v bool) error { return setFeature("tool_orchestrator", v) }

// JobsEnabled reads the jobs feature flag (the scheduled-jobs engine in vixd).
// Defaults to true; the VIX_DISABLE_JOBS environment variable overrides
// everything as an emergency kill switch.
func JobsEnabled() bool {
	if v := os.Getenv("VIX_DISABLE_JOBS"); v == "1" || v == "true" {
		return false
	}
	return feature("jobs", true)
}

// DefaultMaxAttachmentTextBytes bounds the size of a text or PDF file a user may
// attach to a prompt when attachments.max_text_bytes is unset in settings.json.
const DefaultMaxAttachmentTextBytes = 10 << 20 // 10 MiB

// MaxAttachmentTextBytes reads attachments.max_text_bytes from
// ~/.vix/settings.json: the maximum size of a single text or PDF file a user may
// attach to a prompt (the daemon reads and embeds the file's text). A missing,
// zero, or negative value falls back to DefaultMaxAttachmentTextBytes.
func MaxAttachmentTextBytes() int {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return DefaultMaxAttachmentTextBytes
	}
	var cfg struct {
		Attachments struct {
			MaxTextBytes int `json:"max_text_bytes"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultMaxAttachmentTextBytes
	}
	if cfg.Attachments.MaxTextBytes > 0 {
		return cfg.Attachments.MaxTextBytes
	}
	return DefaultMaxAttachmentTextBytes
}

// HooksEnabled reads the hooks feature flag (the lifecycle-hooks engine in
// vixd). Defaults to true; the VIX_DISABLE_HOOKS environment variable overrides
// everything as an emergency kill switch.
func HooksEnabled() bool {
	if v := os.Getenv("VIX_DISABLE_HOOKS"); v == "1" || v == "true" {
		return false
	}
	return feature("hooks", true)
}

// JobsMaxConcurrentRuns reads jobs.max_concurrent_runs from
// ~/.vix/settings.json. Returns 0 when absent/invalid, letting the scheduler
// apply its default.
func JobsMaxConcurrentRuns() int {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	var cfg struct {
		Jobs struct {
			MaxConcurrentRuns int `json:"max_concurrent_runs"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0
	}
	if cfg.Jobs.MaxConcurrentRuns < 0 {
		return 0
	}
	return cfg.Jobs.MaxConcurrentRuns
}

// DefaultLogRetentionDays is how long job/hook run logs are kept when
// logs.retention_days is absent or invalid in settings.json.
const DefaultLogRetentionDays = 10

// LogRetentionDays reads logs.retention_days from ~/.vix/settings.json: how many
// days of job/hook run logs (~/.vix/logs/{jobs,hooks}/<date>.jsonl) the daemon
// keeps before sweeping older daily files. Returns DefaultLogRetentionDays when
// the key is absent or the file is missing/unparsable. A value of 0 (or
// negative) disables the sweep entirely — logs are kept forever.
func LogRetentionDays() int {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return DefaultLogRetentionDays
	}
	var cfg struct {
		Logs struct {
			RetentionDays *int `json:"retention_days"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultLogRetentionDays
	}
	if cfg.Logs.RetentionDays == nil {
		return DefaultLogRetentionDays
	}
	return *cfg.Logs.RetentionDays
}

// Compaction defaults mirror the daemon-side defaults in internal/daemon.
const (
	defaultCompactionAuto      = true
	defaultCompactionThreshold = 0.8
)

// CompactionAuto reads compaction.auto from ~/.vix/settings.json. Defaults to
// true when absent.
func CompactionAuto() bool {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return defaultCompactionAuto
	}
	var cfg struct {
		Compaction struct {
			Auto *bool `json:"auto"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Compaction.Auto == nil {
		return defaultCompactionAuto
	}
	return *cfg.Compaction.Auto
}

// CompactionThreshold reads compaction.threshold from ~/.vix/settings.json.
// Defaults to 0.8 when absent.
func CompactionThreshold() float64 {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return defaultCompactionThreshold
	}
	var cfg struct {
		Compaction struct {
			Threshold *float64 `json:"threshold"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Compaction.Threshold == nil {
		return defaultCompactionThreshold
	}
	return *cfg.Compaction.Threshold
}

// setCompactionField writes a single key inside the top-level `compaction`
// object in ~/.vix/settings.json, preserving other keys.
func setCompactionField(key string, v any) error {
	home := HomeVixDir()
	if home == "" {
		return fmt.Errorf("no home directory")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	p := filepath.Join(home, "settings.json")

	raw := map[string]any{}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &raw)
	}

	comp, _ := raw["compaction"].(map[string]any)
	if comp == nil {
		comp = map[string]any{}
	}
	comp[key] = v
	raw["compaction"] = comp

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

// SetCompactionAuto writes compaction.auto to ~/.vix/settings.json.
func SetCompactionAuto(v bool) error { return setCompactionField("auto", v) }

// SetCompactionThreshold writes compaction.threshold to ~/.vix/settings.json.
func SetCompactionThreshold(v float64) error { return setCompactionField("threshold", v) }

// DefaultClosedThreadRetentionMinutes is the default retention for closed
// thread records: one week.
const DefaultClosedThreadRetentionMinutes = 7 * 24 * 60

// ClosedThreadRetentionMinutes reads threads.closed_retention_minutes from
// ~/.vix/settings.json. Closed thread records older than this are deleted by
// the daemon on startup. Defaults to one week when absent. 0 means never trim
// (settable only by editing settings.json — the TUI does not offer it). The
// pre-rename block sessions.closed_retention_minutes is honored as a fallback.
func ClosedThreadRetentionMinutes() int {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return DefaultClosedThreadRetentionMinutes
	}
	var cfg struct {
		Threads struct {
			ClosedRetentionMinutes *int `json:"closed_retention_minutes"`
		} `json:"threads"`
		Sessions struct {
			ClosedRetentionMinutes *int `json:"closed_retention_minutes"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultClosedThreadRetentionMinutes
	}
	v := cfg.Threads.ClosedRetentionMinutes
	if v == nil {
		v = cfg.Sessions.ClosedRetentionMinutes // legacy fallback
	}
	if v == nil {
		return DefaultClosedThreadRetentionMinutes
	}
	if *v < 0 {
		return DefaultClosedThreadRetentionMinutes
	}
	return *v
}

// SetClosedThreadRetentionMinutes writes threads.closed_retention_minutes to
// ~/.vix/settings.json, preserving other keys.
func SetClosedThreadRetentionMinutes(v int) error {
	home := HomeVixDir()
	if home == "" {
		return fmt.Errorf("no home directory")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	p := filepath.Join(home, "settings.json")

	raw := map[string]any{}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &raw)
	}

	threads, _ := raw["threads"].(map[string]any)
	if threads == nil {
		threads = map[string]any{}
	}
	threads["closed_retention_minutes"] = v
	raw["threads"] = threads

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

// notifSound reads a notification group (e.g. "turn_end" or "needs_you") from
// the notifications block in ~/.vix/settings.json, returning whether its sound
// is enabled and the chosen sound name. Missing/unparsable yields (false, "").
func notifSound(group string) (enabled bool, name string) {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return false, ""
	}
	var cfg struct {
		Notifications map[string]struct {
			Sound     *bool  `json:"sound"`
			SoundName string `json:"sound_name"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, ""
	}
	g, ok := cfg.Notifications[group]
	if !ok {
		return false, ""
	}
	if g.Sound != nil {
		enabled = *g.Sound
	}
	return enabled, g.SoundName
}

// setNotificationField writes a single key inside notifications.<group> in
// ~/.vix/settings.json, preserving other keys.
func setNotificationField(group, key string, v any) error {
	home := HomeVixDir()
	if home == "" {
		return fmt.Errorf("no home directory")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	p := filepath.Join(home, "settings.json")

	raw := map[string]any{}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &raw)
	}

	notifs, _ := raw["notifications"].(map[string]any)
	if notifs == nil {
		notifs = map[string]any{}
	}
	grp, _ := notifs[group].(map[string]any)
	if grp == nil {
		grp = map[string]any{}
	}
	grp[key] = v
	notifs[group] = grp
	raw["notifications"] = notifs

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

// TurnEndSoundEnabled reports whether a sound plays when a turn ends. Opt-in:
// defaults to false when unset.
func TurnEndSoundEnabled() bool { e, _ := notifSound("turn_end"); return e }

// TurnEndSoundName is the chosen turn-end sound name ("" when unset — callers
// substitute the default).
func TurnEndSoundName() string { _, n := notifSound("turn_end"); return n }

// NeedsYouSoundEnabled reports whether a sound plays when the agent needs the
// user (a question or a permission prompt). Opt-in: defaults to false.
func NeedsYouSoundEnabled() bool { e, _ := notifSound("needs_you"); return e }

// NeedsYouSoundName is the chosen needs-you sound name ("" when unset).
func NeedsYouSoundName() string { _, n := notifSound("needs_you"); return n }

// SetTurnEndSoundEnabled writes notifications.turn_end.sound.
func SetTurnEndSoundEnabled(v bool) error { return setNotificationField("turn_end", "sound", v) }

// SetTurnEndSoundName writes notifications.turn_end.sound_name.
func SetTurnEndSoundName(v string) error { return setNotificationField("turn_end", "sound_name", v) }

// SetNeedsYouSoundEnabled writes notifications.needs_you.sound.
func SetNeedsYouSoundEnabled(v bool) error { return setNotificationField("needs_you", "sound", v) }

// SetNeedsYouSoundName writes notifications.needs_you.sound_name.
func SetNeedsYouSoundName(v string) error { return setNotificationField("needs_you", "sound_name", v) }

// ThemeConfig holds user-configurable brand colors.
type ThemeConfig struct {
	Primary    string `json:"primary"`    // hex color like "#BC63FC"
	Secondary  string `json:"secondary"`  // hex color like "#A3FC63"
	Tertiary   string `json:"tertiary"`   // hex color like "#FC6F63"
	Quaternary string `json:"quaternary"` // hex color like "#63F0FC"
}

// ElevenLabsAgentID reads the elevenlabs.agent_id from the layered settings
// files (home then project, last non-empty wins). Falls back to the built-in
// default if no value is configured.
func ElevenLabsAgentID(paths VixPaths) string {
	const defaultID = "agent_7501kqrztj1te17ssqz5wqpnvkf3"
	result := defaultID
	for _, p := range paths.Settings() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg struct {
			ElevenLabs struct {
				AgentID string `json:"agent_id"`
			} `json:"elevenlabs"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.ElevenLabs.AgentID != "" {
			result = cfg.ElevenLabs.AgentID
		}
	}
	return result
}

// ElevenLabsAuthMode reads the elevenlabs.auth_mode from the layered settings
// files (home then project, last non-empty wins). Returns "public" by default.
// Set to "signed_url" to require a server-side ELEVENLABS_API_KEY instead.
func ElevenLabsAuthMode(paths VixPaths) string {
	result := "public"
	for _, p := range paths.Settings() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg struct {
			ElevenLabs struct {
				AuthMode string `json:"auth_mode"`
			} `json:"elevenlabs"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.ElevenLabs.AuthMode != "" {
			result = cfg.ElevenLabs.AuthMode
		}
	}
	return result
}

// LoadThemeConfig reads theme colors from settings.json files in the order
// returned by paths.Settings() — home then project in normal mode, or just
// the override in config-dir mode.
func LoadThemeConfig(paths VixPaths) ThemeConfig {
	var tc ThemeConfig

	for _, p := range paths.Settings() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var wrapper struct {
			Theme ThemeConfig `json:"theme"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			log.Printf("[config] failed to parse theme from %s: %v", p, err)
			continue
		}
		if wrapper.Theme.Primary != "" {
			tc.Primary = wrapper.Theme.Primary
		}
		if wrapper.Theme.Secondary != "" {
			tc.Secondary = wrapper.Theme.Secondary
		}
		if wrapper.Theme.Tertiary != "" {
			tc.Tertiary = wrapper.Theme.Tertiary
		}
		if wrapper.Theme.Quaternary != "" {
			tc.Quaternary = wrapper.Theme.Quaternary
		}
	}

	return tc
}
