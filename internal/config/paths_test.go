package config

import (
	"path/filepath"
	"testing"
)

func TestVixPaths_NormalMode(t *testing.T) {
	p := NewVixPaths("", "/home/.vix", "/project")

	if p.IsOverride() {
		t.Error("expected IsOverride false in normal mode")
	}

	layers := p.Layers()
	want := []string{"/home/.vix", filepath.Join("/project", ".vix")}
	if len(layers) != len(want) {
		t.Fatalf("Layers length = %d, want %d", len(layers), len(want))
	}
	for i, w := range want {
		if layers[i] != w {
			t.Errorf("Layers[%d] = %q, want %q", i, layers[i], w)
		}
	}

	if got := p.Primary(); got != filepath.Join("/project", ".vix") {
		t.Errorf("Primary = %q, want %q", got, filepath.Join("/project", ".vix"))
	}

	if got := p.Logs(); got != filepath.Join("/home/.vix", "logs") {
		t.Errorf("Logs = %q, want %q", got, filepath.Join("/home/.vix", "logs"))
	}

	if got := p.JobsLog(); got != filepath.Join("/home/.vix", "logs", "jobs") {
		t.Errorf("JobsLog = %q", got)
	}
	if got := p.HooksLog(); got != filepath.Join("/home/.vix", "logs", "hooks") {
		t.Errorf("HooksLog = %q", got)
	}

	if got := p.HeartbeatMD(); got != filepath.Join("/home/.vix", "jobs", "heartbeat", "heartbeat.md") {
		t.Errorf("HeartbeatMD = %q", got)
	}

	if got := p.AccessStatsDB(); got != filepath.Join("/project", ".vix", "access_stats.db") {
		t.Errorf("AccessStatsDB = %q", got)
	}

	if got := p.History(); got != filepath.Join("/project", ".vix", "history.txt") {
		t.Errorf("History = %q", got)
	}

	settings := p.Settings()
	if len(settings) != 2 {
		t.Fatalf("Settings length = %d, want 2", len(settings))
	}
	if settings[1] != filepath.Join("/project", ".vix", "settings.json") {
		t.Errorf("project settings = %q", settings[1])
	}

	claudeMD := p.ClaudeMD()
	// Normal mode: [home/CLAUDE.md, cwd/CLAUDE.md]
	if len(claudeMD) != 2 {
		t.Fatalf("ClaudeMD length = %d, want 2", len(claudeMD))
	}
	if claudeMD[1] != filepath.Join("/project", "CLAUDE.md") {
		t.Errorf("ClaudeMD[1] = %q, want cwd CLAUDE.md", claudeMD[1])
	}

	agentsMD := p.AgentsMD()
	// Normal mode: [home/AGENTS.md, cwd/AGENTS.md]
	if len(agentsMD) != 2 {
		t.Fatalf("AgentsMD length = %d, want 2", len(agentsMD))
	}
	if agentsMD[0] != filepath.Join("/home/.vix", "AGENTS.md") {
		t.Errorf("AgentsMD[0] = %q, want home AGENTS.md", agentsMD[0])
	}
	if agentsMD[1] != filepath.Join("/project", "AGENTS.md") {
		t.Errorf("AgentsMD[1] = %q, want cwd AGENTS.md", agentsMD[1])
	}
}

func TestVixPaths_OverrideMode(t *testing.T) {
	p := NewVixPaths("/custom", "/home/.vix", "/project")

	if !p.IsOverride() {
		t.Error("expected IsOverride true")
	}

	if p.Home() != "" {
		t.Errorf("Home should be empty in override mode, got %q", p.Home())
	}
	if p.Project() != "" {
		t.Errorf("Project should be empty in override mode, got %q", p.Project())
	}

	layers := p.Layers()
	if len(layers) != 1 || layers[0] != "/custom" {
		t.Errorf("Layers = %v, want [/custom]", layers)
	}

	settings := p.Settings()
	if len(settings) != 1 || settings[0] != filepath.Join("/custom", "settings.json") {
		t.Errorf("Settings = %v", settings)
	}

	agents := p.Agents()
	if len(agents) != 1 || agents[0] != filepath.Join("/custom", "agents") {
		t.Errorf("Agents = %v", agents)
	}

	if got := p.Primary(); got != "/custom" {
		t.Errorf("Primary = %q, want /custom", got)
	}

	if got := p.Logs(); got != filepath.Join("/custom", "logs") {
		t.Errorf("Logs = %q", got)
	}

	if got := p.JobsLog(); got != filepath.Join("/custom", "logs", "jobs") {
		t.Errorf("JobsLog = %q", got)
	}
	if got := p.HooksLog(); got != filepath.Join("/custom", "logs", "hooks") {
		t.Errorf("HooksLog = %q", got)
	}

	if got := p.HeartbeatMD(); got != filepath.Join("/custom", "jobs", "heartbeat", "heartbeat.md") {
		t.Errorf("HeartbeatMD = %q", got)
	}

	if got := p.AccessStatsDB(); got != filepath.Join("/custom", "access_stats.db") {
		t.Errorf("AccessStatsDB = %q", got)
	}

	if got := p.History(); got != filepath.Join("/custom", "history.txt") {
		t.Errorf("History = %q", got)
	}

	if got := p.Brain(); got != "/custom" {
		t.Errorf("Brain = %q, want /custom", got)
	}

	if got := p.ProjectSettingsWrite(); got != filepath.Join("/custom", "settings.json") {
		t.Errorf("ProjectSettingsWrite = %q", got)
	}

	claudeMD := p.ClaudeMD()
	if len(claudeMD) != 1 || claudeMD[0] != filepath.Join("/custom", "CLAUDE.md") {
		t.Errorf("ClaudeMD = %v", claudeMD)
	}

	agentsMD := p.AgentsMD()
	if len(agentsMD) != 1 || agentsMD[0] != filepath.Join("/custom", "AGENTS.md") {
		t.Errorf("AgentsMD = %v", agentsMD)
	}
}

func TestVixPaths_NormalModeWithoutHome(t *testing.T) {
	p := NewVixPaths("", "", "/project")

	layers := p.Layers()
	// Without home, layers is just [project]
	if len(layers) != 1 || layers[0] != filepath.Join("/project", ".vix") {
		t.Errorf("Layers without home = %v", layers)
	}

	if got := p.Logs(); got != "" {
		t.Errorf("Logs should be empty without home, got %q", got)
	}
	if got := p.JobsLog(); got != "" {
		t.Errorf("JobsLog should be empty without home, got %q", got)
	}
	if got := p.HooksLog(); got != "" {
		t.Errorf("HooksLog should be empty without home, got %q", got)
	}
	if got := p.HeartbeatMD(); got != "" {
		t.Errorf("HeartbeatMD should be empty without home, got %q", got)
	}
}
