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
// session working directory instead of os.Getwd().
// If configDir is non-empty, it is resolved to an absolute path and used as
// the sole .vix config root (ignoring ~/.vix and ./.vix).
// If socketPath is empty, /tmp/vixd.sock is used.
func Load(forceInit bool, workdir, configDir, socketPath string) (*Config, error) {
	// Model selection now lives in the active chat agent's `model:` YAML
	// frontmatter (resolved per-session in the daemon). The Config.Model
	// field is left as a final fallback only — see session.go for the
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
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return def
	}
	var cfg struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return def
	}
	if v, ok := cfg.Features[name]; ok {
		return v
	}
	return def
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

// CloseAllSessionsOnQuit reads the close_all_sessions_on_quit feature flag.
// Defaults to false: quitting vix leaves all session records open so they are
// restored on next launch. When true, quitting explicitly closes every session.
func CloseAllSessionsOnQuit() bool { return feature("close_all_sessions_on_quit", false) }

// SetCloseAllSessionsOnQuit writes the close_all_sessions_on_quit feature flag.
func SetCloseAllSessionsOnQuit(v bool) error { return setFeature("close_all_sessions_on_quit", v) }

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

// DefaultClosedSessionRetentionMinutes is the default retention for closed
// session records: one week.
const DefaultClosedSessionRetentionMinutes = 7 * 24 * 60

// ClosedSessionRetentionMinutes reads sessions.closed_retention_minutes from
// ~/.vix/settings.json. Closed session records older than this are deleted by
// the daemon on startup. Defaults to one week when absent. 0 means never trim
// (settable only by editing settings.json — the TUI does not offer it).
func ClosedSessionRetentionMinutes() int {
	p := filepath.Join(HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return DefaultClosedSessionRetentionMinutes
	}
	var cfg struct {
		Sessions struct {
			ClosedRetentionMinutes *int `json:"closed_retention_minutes"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Sessions.ClosedRetentionMinutes == nil {
		return DefaultClosedSessionRetentionMinutes
	}
	if *cfg.Sessions.ClosedRetentionMinutes < 0 {
		return DefaultClosedSessionRetentionMinutes
	}
	return *cfg.Sessions.ClosedRetentionMinutes
}

// SetClosedSessionRetentionMinutes writes sessions.closed_retention_minutes to
// ~/.vix/settings.json, preserving other keys.
func SetClosedSessionRetentionMinutes(v int) error {
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

	sessions, _ := raw["sessions"].(map[string]any)
	if sessions == nil {
		sessions = map[string]any{}
	}
	sessions["closed_retention_minutes"] = v
	raw["sessions"] = sessions

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

// ThemeConfig holds user-configurable brand colors.
type ThemeConfig struct {
	Primary   string `json:"primary"`   // hex color like "#BC63FC"
	Secondary string `json:"secondary"` // hex color like "#A3FC63"
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
	}

	return tc
}
