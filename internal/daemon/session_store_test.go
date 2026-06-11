package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// testPaths returns a VixPaths whose Sessions() resolves under a fresh temp dir
// (via config-dir override mode, which routes all session state into one dir).
func testPaths(t *testing.T) config.VixPaths {
	t.Helper()
	return config.NewVixPaths(t.TempDir(), "", "/work")
}

func sampleRecord() sessionRecord {
	return sessionRecord{
		ID:    "sess-abc",
		CWD:   "/work",
		Model: "anthropic/claude-x",
		Messages: []llm.MessageParam{
			llm.NewUserMessage(llm.NewTextBlock("first question")),
			llm.NewAssistantMessage(
				llm.NewTextBlock("an answer"),
				llm.NewToolUseBlock("t1", "read_file", map[string]any{"path": "main.go"}),
			),
			llm.NewUserMessage(llm.NewToolResultBlock("t1", "file contents", false)),
		},
		TodoList: []protocol.TodoItem{
			{ID: "a", Content: "do it", Status: protocol.TodoPending},
		},
		SessionMode:   "chat",
		StartedAt:     time.Now().Add(-time.Hour).Truncate(time.Second),
		LastRequestAt: time.Now().Truncate(time.Second),
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	paths := testPaths(t)
	rec := sampleRecord()

	if err := saveSessionRecord(paths, rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, found, err := loadOpenSessionRecord(paths, rec.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !found {
		t.Fatal("expected record to be found")
	}
	if got.ID != rec.ID || got.CWD != rec.CWD || got.Model != rec.Model {
		t.Errorf("metadata mismatch: %+v", got)
	}
	if got.SchemaVersion != sessionRecordSchemaVersion {
		t.Errorf("schema version = %d, want %d", got.SchemaVersion, sessionRecordSchemaVersion)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(got.Messages))
	}
	// Tool-use input round-trips through JSON.
	tu := got.Messages[1].Content[1]
	if tu.Type != llm.BlockToolUse || tu.Name != "read_file" || tu.Input["path"] != "main.go" {
		t.Errorf("tool_use block not preserved: %+v", tu)
	}
	if len(got.TodoList) != 1 || got.TodoList[0].ID != "a" {
		t.Errorf("todo list not preserved: %+v", got.TodoList)
	}
}

func TestSaveAtomicNoTempLeftover(t *testing.T) {
	paths := testPaths(t)
	if err := saveSessionRecord(paths, sampleRecord()); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(paths.SessionsOpen())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || filepath.Ext(e.Name()) != ".json" {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
}

func TestMoveToClosed(t *testing.T) {
	paths := testPaths(t)
	rec := sampleRecord()
	if err := saveSessionRecord(paths, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := moveSessionToClosed(paths, rec.ID); err != nil {
		t.Fatalf("move: %v", err)
	}

	// No longer in open/.
	if _, err := os.Stat(sessionRecordPath(paths.SessionsOpen(), rec.ID)); !os.IsNotExist(err) {
		t.Error("record still present in open/ after move")
	}
	// Present in closed/.
	if _, err := os.Stat(sessionRecordPath(paths.SessionsClosed(), rec.ID)); err != nil {
		t.Errorf("record not in closed/: %v", err)
	}
	// No longer attachable: a closed record must not be resurrected.
	_, found, err := loadOpenSessionRecord(paths, rec.ID)
	if err != nil {
		t.Fatalf("load after move: %v", err)
	}
	if found {
		t.Error("closed record still loadable for attach")
	}
}

func TestListOpenExcludesClosed(t *testing.T) {
	paths := testPaths(t)

	open1 := sampleRecord()
	open1.ID = "open-1"
	open1.StartedAt = time.Now().Add(-time.Hour)
	open2 := sampleRecord()
	open2.ID = "open-2"
	open2.StartedAt = time.Now().Add(-2 * time.Hour)
	closed := sampleRecord()
	closed.ID = "closed-1"

	for _, r := range []sessionRecord{open1, open2, closed} {
		if err := saveSessionRecord(paths, r); err != nil {
			t.Fatalf("save %s: %v", r.ID, err)
		}
	}
	if err := moveSessionToClosed(paths, closed.ID); err != nil {
		t.Fatalf("move: %v", err)
	}

	recs := listOpenSessionRecords(paths)
	if len(recs) != 2 {
		t.Fatalf("open count = %d, want 2", len(recs))
	}
	// Sorted by creation time, oldest first.
	if recs[0].ID != "open-2" || recs[1].ID != "open-1" {
		t.Errorf("unexpected order: %s, %s", recs[0].ID, recs[1].ID)
	}
}

func TestPersistenceDisabledNoHome(t *testing.T) {
	// Normal mode with empty home => Sessions() empty => save is a no-op.
	paths := config.NewVixPaths("", "", "/work")
	if paths.SessionsOpen() != "" {
		t.Fatalf("expected empty SessionsOpen with no home, got %q", paths.SessionsOpen())
	}
	if err := saveSessionRecord(paths, sampleRecord()); err != nil {
		t.Errorf("save should be a no-op (nil), got %v", err)
	}
	_, found, err := loadOpenSessionRecord(paths, "sess-abc")
	if err != nil || found {
		t.Errorf("load on disabled store: found=%v err=%v", found, err)
	}
}

func TestFirstUserMessageAndSummary(t *testing.T) {
	rec := sampleRecord()
	if got := rec.firstUserMessage(); got != "first question" {
		t.Errorf("firstUserMessage = %q", got)
	}
	s := rec.summary()
	if s.ID != rec.ID || s.FirstMessage != "first question" || s.Model != rec.Model {
		t.Errorf("summary mismatch: %+v", s)
	}
	if s.StartedAt == "" || s.LastRequestAt == "" {
		t.Errorf("summary timestamps not set: %+v", s)
	}
}

func TestBuildReplayMessages(t *testing.T) {
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("hi")),
		llm.NewAssistantMessage(
			llm.NewTextBlock(""), // empty -> skipped
			llm.NewTextBlock("answer"),
			llm.NewToolUseBlock("t1", "bash", map[string]any{"command": "ls"}),
		),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "out", false)),
		llm.NewAssistantMessage(), // no blocks -> whole message skipped
	}
	out := buildReplayMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("replay messages = %d, want 3", len(out))
	}
	if out[0].Role != "user" || len(out[0].Blocks) != 1 || out[0].Blocks[0].Text != "hi" {
		t.Errorf("user msg wrong: %+v", out[0])
	}
	// Assistant message: empty text skipped, so 2 blocks (text + tool_use).
	if out[1].Role != "assistant" || len(out[1].Blocks) != 2 {
		t.Fatalf("assistant blocks = %d, want 2: %+v", len(out[1].Blocks), out[1])
	}
	if out[1].Blocks[1].Kind != "tool_use" || out[1].Blocks[1].ToolName != "bash" {
		t.Errorf("tool_use not projected: %+v", out[1].Blocks[1])
	}
	if out[2].Blocks[0].Kind != "tool_result" || out[2].Blocks[0].Output != "out" {
		t.Errorf("tool_result not projected: %+v", out[2].Blocks[0])
	}
}

// newReplaySession builds a minimal Session wired for emitReplay (eventChan +
// ctx). Persistence is disabled (empty paths) so persist() is a no-op.
func newReplaySession(t *testing.T, rec *sessionRecord) *Session {
	t.Helper()
	s := &Session{
		id:          rec.ID,
		model:       "anthropic/new-default",
		eventChan:   make(chan protocol.SessionEvent, 4),
		sessionMode: rec.SessionMode,
	}
	if s.sessionMode == "" {
		s.sessionMode = "chat"
	}
	s.activeWorkflow = rec.ActiveWorkflow
	s.messages = append([]llm.MessageParam(nil), rec.Messages...)
	s.attachRecord = rec
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

func captureReplay(t *testing.T, s *Session) protocol.EventReplay {
	t.Helper()
	s.emitReplay()
	select {
	case ev := <-s.eventChan:
		if ev.Type != "event.replay" {
			t.Fatalf("event type = %q, want event.replay", ev.Type)
		}
		rep, ok := ev.Data.(protocol.EventReplay)
		if !ok {
			t.Fatalf("event data type = %T, want EventReplay", ev.Data)
		}
		return rep
	default:
		t.Fatal("no event emitted")
		return protocol.EventReplay{}
	}
}

func TestEmitReplayModelChangedWarning(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/old-saved"
	s := newReplaySession(t, &rec) // s.model = anthropic/new-default

	rep := captureReplay(t, s)
	if len(rep.Warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 (model change)", rep.Warnings)
	}
	if rep.Model != "anthropic/new-default" {
		t.Errorf("replay model = %q, want current default", rep.Model)
	}
	if s.attachRecord != nil {
		t.Error("attachRecord should be cleared after replay")
	}
}

func TestEmitReplayNoWarningWhenModelSame(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/new-default"
	s := newReplaySession(t, &rec)

	rep := captureReplay(t, s)
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", rep.Warnings)
	}
}

func TestEmitReplayWorkflowMissingFallsBackToChat(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/new-default" // avoid model warning
	rec.SessionMode = "workflow"
	rec.ActiveWorkflow = "ghost-workflow"
	s := newReplaySession(t, &rec)
	// s.workflows is empty -> workflow no longer exists.

	rep := captureReplay(t, s)
	if rep.SessionMode != "chat" || rep.ActiveWorkflow != "" {
		t.Errorf("expected fallback to chat: mode=%q wf=%q", rep.SessionMode, rep.ActiveWorkflow)
	}
	if s.sessionMode != "chat" || s.activeWorkflow != "" {
		t.Errorf("session state not updated: mode=%q wf=%q", s.sessionMode, s.activeWorkflow)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 (workflow missing)", rep.Warnings)
	}
}

func TestEmitReplayWorkflowPresentKept(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/new-default"
	rec.SessionMode = "workflow"
	rec.ActiveWorkflow = "build"
	s := newReplaySession(t, &rec)
	s.workflows = []*WorkflowDef{{Name: "build"}}

	rep := captureReplay(t, s)
	if rep.SessionMode != "workflow" || rep.ActiveWorkflow != "build" {
		t.Errorf("workflow should be kept: mode=%q wf=%q", rep.SessionMode, rep.ActiveWorkflow)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", rep.Warnings)
	}
}

func TestDeleteSessionRecord(t *testing.T) {
	paths := testPaths(t)

	// Record in open/ is removed.
	open := sampleRecord()
	open.ID = "del-open"
	if err := saveSessionRecord(paths, open); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := deleteSessionRecord(paths, open.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(sessionRecordPath(paths.SessionsOpen(), open.ID)); !os.IsNotExist(err) {
		t.Error("record still present in open/ after delete")
	}

	// Record in closed/ is removed too.
	closed := sampleRecord()
	closed.ID = "del-closed"
	if err := saveSessionRecord(paths, closed); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := moveSessionToClosed(paths, closed.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := deleteSessionRecord(paths, closed.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(sessionRecordPath(paths.SessionsClosed(), closed.ID)); !os.IsNotExist(err) {
		t.Error("record still present in closed/ after delete")
	}

	// Missing record and disabled persistence are no-ops.
	if err := deleteSessionRecord(paths, "no-such-id"); err != nil {
		t.Errorf("delete missing: %v", err)
	}
	if err := deleteSessionRecord(config.NewVixPaths("", "", "/work"), "x"); err != nil {
		t.Errorf("delete with persistence disabled: %v", err)
	}
}

// writeClosedRecord saves rec and moves it to closed/, returning its path.
func writeClosedRecord(t *testing.T, paths config.VixPaths, rec sessionRecord) string {
	t.Helper()
	if err := saveSessionRecord(paths, rec); err != nil {
		t.Fatalf("save %s: %v", rec.ID, err)
	}
	if err := moveSessionToClosed(paths, rec.ID); err != nil {
		t.Fatalf("move %s: %v", rec.ID, err)
	}
	return sessionRecordPath(paths.SessionsClosed(), rec.ID)
}

func TestTrimStaleClosedSessions(t *testing.T) {
	paths := testPaths(t)
	week := 7 * 24 * time.Hour

	fresh := sampleRecord()
	fresh.ID = "fresh"
	fresh.LastRequestAt = time.Now().Add(-time.Hour)
	freshPath := writeClosedRecord(t, paths, fresh)

	stale := sampleRecord()
	stale.ID = "stale"
	stale.LastRequestAt = time.Now().Add(-2 * week)
	stalePath := writeClosedRecord(t, paths, stale)

	// Stale via StartedAt only (no LastRequestAt).
	staleStart := sampleRecord()
	staleStart.ID = "stale-start"
	staleStart.StartedAt = time.Now().Add(-2 * week)
	staleStart.LastRequestAt = time.Time{}
	staleStartPath := writeClosedRecord(t, paths, staleStart)

	// Corrupt file with an old mtime falls back to mtime and is trimmed.
	corruptOld := filepath.Join(paths.SessionsClosed(), "corrupt-old.json")
	if err := os.WriteFile(corruptOld, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * week)
	if err := os.Chtimes(corruptOld, old, old); err != nil {
		t.Fatal(err)
	}

	// Corrupt file with a fresh mtime is kept.
	corruptFresh := filepath.Join(paths.SessionsClosed(), "corrupt-fresh.json")
	if err := os.WriteFile(corruptFresh, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stale record still in open/ must not be touched.
	openStale := sampleRecord()
	openStale.ID = "open-stale"
	openStale.LastRequestAt = time.Now().Add(-2 * week)
	if err := saveSessionRecord(paths, openStale); err != nil {
		t.Fatalf("save: %v", err)
	}

	trimStaleClosedSessions(paths, week)

	for _, p := range []string{stalePath, staleStartPath, corruptOld} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been trimmed", filepath.Base(p))
		}
	}
	for _, p := range []string{freshPath, corruptFresh, sessionRecordPath(paths.SessionsOpen(), openStale.ID)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been kept: %v", filepath.Base(p), err)
		}
	}
}

func TestTrimStaleClosedSessionsNever(t *testing.T) {
	paths := testPaths(t)

	stale := sampleRecord()
	stale.ID = "stale"
	stale.LastRequestAt = time.Now().Add(-365 * 24 * time.Hour)
	p := writeClosedRecord(t, paths, stale)

	// maxAge <= 0 means retention disabled ("never"): nothing is removed.
	trimStaleClosedSessions(paths, 0)
	trimStaleClosedSessions(paths, -time.Hour)

	if _, err := os.Stat(p); err != nil {
		t.Errorf("record should have been kept with retention disabled: %v", err)
	}
}

// ── Unread flag ──

// TestUnreadRoundTrip: the session-global unread flag persists and surfaces in
// summaries; legacy records without the field read as seen.
func TestUnreadRoundTrip(t *testing.T) {
	paths := testPaths(t)

	rec := sampleRecord()
	rec.Unread = true
	if err := saveSessionRecord(paths, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, found, _ := loadOpenSessionRecord(paths, rec.ID)
	if !found || !got.Unread {
		t.Fatalf("unread flag lost: found=%v rec=%+v", found, got)
	}
	if !got.summary().Unread {
		t.Fatal("summary must carry Unread")
	}

	// Legacy record: no unread field on disk → read.
	legacy := sampleRecord()
	legacy.ID = "sess-legacy"
	if err := saveSessionRecord(paths, legacy); err != nil {
		t.Fatalf("save legacy: %v", err)
	}
	got, _, _ = loadOpenSessionRecord(paths, legacy.ID)
	if got.Unread || got.summary().Unread {
		t.Fatal("legacy record must read as seen")
	}
}

// TestMarkReadCommandClearsUnread: buildRecord reflects the session flag, and
// the mark_read transition persists.
func TestMarkReadCommandClearsUnread(t *testing.T) {
	paths := testPaths(t)
	srv := NewServer("/tmp/unused.sock", config.Credential{}, "t", "m", &config.DaemonConfig{}, PluginConfig{})
	sess := NewSession("sess-mr", srv, nil, "m", "/work", paths.Override(), false, true, true, true, context.Background())

	sess.unread = true
	sess.persist()
	got, found, _ := loadOpenSessionRecord(sess.paths, "sess-mr")
	if !found || !got.Unread {
		t.Fatalf("turn-end persist must carry unread, got %+v", got)
	}

	// What the session.mark_read command handler does:
	sess.unread = false
	sess.persist()
	got, _, _ = loadOpenSessionRecord(sess.paths, "sess-mr")
	if got.Unread {
		t.Fatal("mark_read must clear the persisted flag")
	}
}

// TestSweepExemptsUnreadRuns: the open/ retention sweep never auto-dismisses
// unread or failed job runs; read OK runs beyond the cap age out.
func TestSweepExemptsUnreadRuns(t *testing.T) {
	paths := testPaths(t)
	trig := &protocol.TriggerInfo{Type: "cron", Ref: "job-x"}
	mk := func(id string, age time.Duration, status string, unread bool) {
		rec := sessionRecord{
			ID: id, CWD: "/work", Origin: "vix", Trigger: trig,
			JobStatus: status, Unread: unread,
			SessionMode: "chat", StartedAt: time.Now().Add(-age),
		}
		if err := saveSessionRecord(paths, rec); err != nil {
			t.Fatal(err)
		}
	}
	// Newest three stay regardless; the four older ones exercise the rules.
	mk("r1", 1*time.Hour, "ok", false)
	mk("r2", 2*time.Hour, "ok", false)
	mk("r3", 3*time.Hour, "ok", false)
	mk("r4", 4*time.Hour, "ok", true)     // unread → kept
	mk("r5", 5*time.Hour, "error", false) // failure → kept
	mk("r6", 6*time.Hour, "ok", false)    // read ok → swept
	mk("r7", 7*time.Hour, "ok", false)    // read ok → swept

	sweepJobRunRecords(paths, "job-x")

	openIDs := map[string]bool{}
	for _, r := range listSessionRecordsIn(paths.SessionsOpen()) {
		openIDs[r.ID] = true
	}
	for _, want := range []string{"r1", "r2", "r3", "r4", "r5"} {
		if !openIDs[want] {
			t.Errorf("%s should have been kept in open/", want)
		}
	}
	for _, gone := range []string{"r6", "r7"} {
		if openIDs[gone] {
			t.Errorf("%s should have been swept to closed/", gone)
		}
	}
}
