package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

func TestThreadsIncludesPersistedOpenRecords(t *testing.T) {
	home := t.TempDir()
	paths := config.NewVixPaths("", home, "")
	started := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	last := time.Date(2026, 6, 17, 12, 5, 0, 0, time.UTC)

	if err := saveThreadRecord(paths, threadRecord{
		ID:                "persisted",
		CWD:               "/tmp/project",
		Model:             "openai/gpt-5.5",
		Title:             "Persisted thread",
		StartedAt:         started,
		LastRequestAt:     last,
		TotalInputTokens:  12,
		TotalOutputTokens: 34,
		Unread:            true,
	}); err != nil {
		t.Fatalf("saveThreadRecord: %v", err)
	}

	srv := &Server{
		homeVixDir: home,
		threads:    map[string]*Thread{},
	}

	infos := srv.Threads()
	if len(infos) != 1 {
		t.Fatalf("len(Threads()) = %d, want 1 (%+v)", len(infos), infos)
	}
	info := infos[0]
	if info.ID != "persisted" || info.CWD != "/tmp/project" {
		t.Fatalf("thread info = %+v", info)
	}
	if info.Model != "openai/gpt-5.5" || info.Title != "Persisted thread" {
		t.Fatalf("thread metadata = %+v", info)
	}
	if info.InputTokens != 12 || info.OutputTokens != 34 {
		t.Fatalf("token counts = (%d,%d), want (12,34)", info.InputTokens, info.OutputTokens)
	}
	if info.LastRequestAt == nil || *info.LastRequestAt != last.Format(time.RFC3339) {
		t.Fatalf("LastRequestAt = %v, want %s", info.LastRequestAt, last.Format(time.RFC3339))
	}
	if info.Attached {
		t.Fatal("Attached = true, want false for persisted-only record")
	}
	if !info.Unread {
		t.Fatal("Unread = false, want true")
	}
}

func TestThreadsPrefersLiveThreadOverPersistedRecord(t *testing.T) {
	home := t.TempDir()
	paths := config.NewVixPaths("", home, "")
	started := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

	if err := saveThreadRecord(paths, threadRecord{
		ID:                "same",
		CWD:               "/tmp/persisted",
		Model:             "openai/gpt-5.5",
		StartedAt:         started,
		TotalInputTokens:  1,
		TotalOutputTokens: 2,
	}); err != nil {
		t.Fatalf("saveThreadRecord: %v", err)
	}

	srv := &Server{
		homeVixDir: home,
		threads: map[string]*Thread{
			"same": {
				id:                "same",
				cwd:               "/tmp/live",
				model:             "anthropic/claude-sonnet-4-6",
				startTime:         started.Add(time.Minute),
				totalInputTokens:  10,
				totalOutputTokens: 20,
			},
		},
	}

	infos := srv.Threads()
	if len(infos) != 1 {
		t.Fatalf("len(Threads()) = %d, want 1 (%+v)", len(infos), infos)
	}
	info := infos[0]
	if !info.Attached {
		t.Fatal("Attached = false, want true for live thread")
	}
	if info.CWD != "/tmp/live" || info.Model != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("live thread did not override persisted record: %+v", info)
	}
	if info.InputTokens != 10 || info.OutputTokens != 20 {
		t.Fatalf("token counts = (%d,%d), want live counts (10,20)", info.InputTokens, info.OutputTokens)
	}
}

func TestThreadForWebCallRestoresPersistedOpenRecord(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	agentsDir := filepath.Join(home, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "general.md"), []byte("---\nname: general\n---\nExplore the project files.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile agent: %v", err)
	}

	paths := config.NewVixPaths("", home, "")
	if err := saveThreadRecord(paths, threadRecord{
		ID:        "persisted-web",
		CWD:       cwd,
		Model:     "openai/gpt-5.5",
		StartedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("saveThreadRecord: %v", err)
	}

	srv := NewServer("", config.Credential{}, "test-thread", "openai/gpt-5.5", &config.DaemonConfig{HomeVixDir: home}, nil)
	sess, cleanup, err := srv.threadForWebCall("persisted-web")
	if err != nil {
		t.Fatalf("threadForWebCall: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want cleanup for restored web-only thread")
	}
	defer cleanup()
	if sess == nil {
		t.Fatal("threadForWebCall returned nil thread")
	}
	if sess.id != "persisted-web" || sess.cwd != cwd {
		t.Fatalf("restored thread = id %q cwd %q, want persisted-web %q", sess.id, sess.cwd, cwd)
	}
	if !sess.headless {
		t.Fatal("restored web thread should be headless")
	}
	if _, ok := sess.customAgents["general"]; !ok {
		t.Fatalf("restored web thread did not load general agent: %#v", sess.customAgents)
	}
	if live := srv.getThread("persisted-web"); live != nil {
		t.Fatalf("web-only restore registered a live thread: %#v", live)
	}
}

func TestThreadForWebCallPrefersLiveThread(t *testing.T) {
	home := t.TempDir()
	live := &Thread{id: "same", cwd: "/tmp/live"}
	srv := &Server{
		homeVixDir: home,
		threads: map[string]*Thread{
			"same": live,
		},
	}

	sess, cleanup, err := srv.threadForWebCall("same")
	if err != nil {
		t.Fatalf("threadForWebCall: %v", err)
	}
	if sess != live {
		t.Fatalf("threadForWebCall returned %#v, want live thread %#v", sess, live)
	}
	if cleanup != nil {
		t.Fatal("cleanup should be nil for live threads")
	}
}

func TestRunExplorationReturnsConfigErrorWithoutLLM(t *testing.T) {
	sess := &Thread{
		model:     "openai/gpt-5.5",
		configErr: errors.New("missing model credentials"),
		customAgents: map[string]SubagentConfig{
			"general": {Name: "general"},
		},
	}

	_, err := sess.RunExploration(context.Background(), "general", "read the project")
	if err == nil {
		t.Fatal("RunExploration error = nil, want config error")
	}
	if !strings.Contains(err.Error(), "missing model credentials") {
		t.Fatalf("RunExploration error = %q, want config error", err.Error())
	}
}
