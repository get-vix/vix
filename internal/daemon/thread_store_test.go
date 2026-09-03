package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// testPaths returns a VixPaths whose Threads() resolves under a fresh temp dir
// (via config-dir override mode, which routes all thread state into one dir).
func testPaths(t *testing.T) config.VixPaths {
	t.Helper()
	return config.NewVixPaths(t.TempDir(), "", "/work")
}

func sampleRecord() threadRecord {
	return threadRecord{
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
		ThreadMode:    "chat",
		StartedAt:     time.Now().Add(-time.Hour).Truncate(time.Second),
		LastRequestAt: time.Now().Truncate(time.Second),
	}
}

// TestMigrateLegacyThreadsDir covers the one-time sessions/ -> threads/ move:
// a legacy record (written with the pre-rename "session_mode" key) is relocated
// intact and still decodes its mode, and the migration is a no-op once threads/
// exists.
func TestMigrateLegacyThreadsDir(t *testing.T) {
	paths := testPaths(t)
	legacyOpen := filepath.Join(paths.LegacyThreads(), "open")
	if err := os.MkdirAll(legacyOpen, 0o755); err != nil {
		t.Fatal(err)
	}
	// A record shaped like the old on-disk format (note "session_mode").
	legacyJSON := `{"schema_version":1,"id":"leg-1","cwd":"/work","session_mode":"workflow","messages":[]}`
	if err := os.WriteFile(filepath.Join(legacyOpen, "leg-1.json"), []byte(legacyJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	migrateLegacyThreadsDir(paths)

	// Legacy dir is gone; the record now lives under threads/open.
	if _, err := os.Stat(paths.LegacyThreads()); !os.IsNotExist(err) {
		t.Fatalf("legacy sessions/ dir still present after migration (err=%v)", err)
	}
	rec, found, err := loadOpenThreadRecord(paths, "leg-1")
	if err != nil || !found {
		t.Fatalf("record not found under threads/ after migration (found=%v err=%v)", found, err)
	}
	if rec.ThreadMode != "workflow" {
		t.Errorf("ThreadMode = %q, want %q (legacy session_mode tag must still decode)", rec.ThreadMode, "workflow")
	}

	// Idempotent: a second run with threads/ already present is a no-op and does
	// not recreate or touch a (re-seeded) legacy dir.
	if err := os.MkdirAll(legacyOpen, 0o755); err != nil {
		t.Fatal(err)
	}
	migrateLegacyThreadsDir(paths)
	if _, err := os.Stat(legacyOpen); err != nil {
		t.Errorf("second migration should be a no-op leaving the legacy dir untouched, got err=%v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	paths := testPaths(t)
	rec := sampleRecord()

	if err := saveThreadRecord(paths, rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, found, err := loadOpenThreadRecord(paths, rec.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !found {
		t.Fatal("expected record to be found")
	}
	if got.ID != rec.ID || got.CWD != rec.CWD || got.Model != rec.Model {
		t.Errorf("metadata mismatch: %+v", got)
	}
	if got.SchemaVersion != threadRecordSchemaVersion {
		t.Errorf("schema version = %d, want %d", got.SchemaVersion, threadRecordSchemaVersion)
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
	if err := saveThreadRecord(paths, sampleRecord()); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(paths.ThreadsOpen())
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
	if err := saveThreadRecord(paths, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := moveThreadToClosed(paths, rec.ID); err != nil {
		t.Fatalf("move: %v", err)
	}

	// No longer in open/.
	if _, err := os.Stat(threadRecordPath(paths.ThreadsOpen(), rec.ID)); !os.IsNotExist(err) {
		t.Error("record still present in open/ after move")
	}
	// Present in closed/.
	if _, err := os.Stat(threadRecordPath(paths.ThreadsClosed(), rec.ID)); err != nil {
		t.Errorf("record not in closed/: %v", err)
	}
	// No longer attachable: a closed record must not be resurrected.
	_, found, err := loadOpenThreadRecord(paths, rec.ID)
	if err != nil {
		t.Fatalf("load after move: %v", err)
	}
	if found {
		t.Error("closed record still loadable for attach")
	}
}

// TestThreadListReturnsAllDirs: thread.list returns every persisted open
// thread regardless of the requesting cwd, so the TUI can group threads by
// working directory. cwd scoping (which threads to auto-attach on launch) is
// applied by the client, not by this handler.
func TestThreadListReturnsAllDirs(t *testing.T) {
	dir := t.TempDir()
	paths := config.NewVixPaths(dir, "", "/work")

	userSame := sampleRecord()
	userSame.ID = "user-same-cwd"
	userSame.CWD = "/work"

	userOther := sampleRecord()
	userOther.ID = "user-other-cwd"
	userOther.CWD = "/elsewhere"

	vixRun := sampleRecord()
	vixRun.ID = "vix-run"
	vixRun.CWD = "/elsewhere" // the job's cwd, not the TUI's
	vixRun.Origin = "vix"
	vixRun.Trigger = &protocol.TriggerInfo{Type: "cron", Ref: "job-1"}

	for _, r := range []threadRecord{userSame, userOther, vixRun} {
		if err := saveThreadRecord(paths, r); err != nil {
			t.Fatalf("save %s: %v", r.ID, err)
		}
	}

	srv := newInstanceTestServer(t)
	RegisterBuiltinHandlers(srv)
	resp, err := srv.GetHandler("thread.list")(map[string]any{
		"cwd": "/work", "config_dir": dir,
	})
	if err != nil {
		t.Fatalf("thread.list: %v", err)
	}
	sums, ok := resp["threads"].([]protocol.ThreadSummary)
	if !ok {
		t.Fatalf("threads has unexpected type %T", resp["threads"])
	}

	got := map[string]bool{}
	for _, s := range sums {
		got[s.ID] = true
	}
	if !got["user-same-cwd"] {
		t.Error("user thread for the requesting cwd missing")
	}
	if !got["user-other-cwd"] {
		t.Error("user thread for another cwd missing: thread.list must return all directories")
	}
	if !got["vix-run"] {
		t.Error("vix-initiated thread missing")
	}
}

// TestThreadRenameHandler: the server-level thread.rename handler pins a manual
// title on a persisted, not-open record on disk (Title + TitleManual), refuses
// when the thread is live in a connection, and validates id/title.
func TestThreadRenameHandler(t *testing.T) {
	dir := t.TempDir()
	paths := config.NewVixPaths(dir, "", "/work")

	rec := sampleRecord()
	rec.ID = "rename-me"
	rec.CWD = "/work"
	rec.Title = "old auto title"
	if err := saveThreadRecord(paths, rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	srv := newInstanceTestServer(t)
	RegisterBuiltinHandlers(srv)

	// Rename the persisted record.
	resp, err := srv.GetHandler("thread.rename")(map[string]any{
		"cwd": "/work", "config_dir": dir, "id": "rename-me", "title": "  My chosen name  ",
	})
	if err != nil {
		t.Fatalf("thread.rename: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v (%v), want ok", resp["status"], resp["message"])
	}
	got, found, err := loadOpenThreadRecord(paths, "rename-me")
	if err != nil || !found {
		t.Fatalf("reload: found=%v err=%v", found, err)
	}
	if got.Title != "My chosen name" {
		t.Errorf("Title = %q, want %q (sanitized/trimmed)", got.Title, "My chosen name")
	}
	if !got.TitleManual {
		t.Error("TitleManual must be set on disk after rename")
	}

	// Empty title and missing id are rejected.
	if r, _ := srv.GetHandler("thread.rename")(map[string]any{"cwd": "/work", "config_dir": dir, "id": "rename-me", "title": "   "}); r["status"] != "error" {
		t.Error("empty title should be rejected")
	}
	if r, _ := srv.GetHandler("thread.rename")(map[string]any{"cwd": "/work", "config_dir": dir, "title": "x"}); r["status"] != "error" {
		t.Error("missing id should be rejected")
	}

	// A live thread is refused (renamed over its own connection instead).
	srv.threadMu.Lock()
	srv.threads["rename-me"] = &Thread{}
	srv.threadMu.Unlock()
	if r, _ := srv.GetHandler("thread.rename")(map[string]any{"cwd": "/work", "config_dir": dir, "id": "rename-me", "title": "y"}); r["status"] != "error" {
		t.Error("rename of a live thread should be refused")
	}
}

// TestThreadRecordTitleManualRoundTrip: buildRecord persists titleManual and
// seedFromRecord restores it, so a manual pin survives a daemon restart.
func TestThreadRecordTitleManualRoundTrip(t *testing.T) {
	src := &Thread{id: "t1", title: "pinned", titleManual: true}
	rec := src.buildRecord()
	if !rec.TitleManual {
		t.Fatal("buildRecord did not persist TitleManual")
	}
	dst := &Thread{}
	dst.seedFromRecord(&rec)
	if dst.title != "pinned" || !dst.titleManual {
		t.Errorf("seedFromRecord: title=%q manual=%v, want pinned/true", dst.title, dst.titleManual)
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

	for _, r := range []threadRecord{open1, open2, closed} {
		if err := saveThreadRecord(paths, r); err != nil {
			t.Fatalf("save %s: %v", r.ID, err)
		}
	}
	if err := moveThreadToClosed(paths, closed.ID); err != nil {
		t.Fatalf("move: %v", err)
	}

	recs := listOpenThreadRecords(paths)
	if len(recs) != 2 {
		t.Fatalf("open count = %d, want 2", len(recs))
	}
	// Sorted by creation time, oldest first.
	if recs[0].ID != "open-2" || recs[1].ID != "open-1" {
		t.Errorf("unexpected order: %s, %s", recs[0].ID, recs[1].ID)
	}
}

func TestPersistenceDisabledNoHome(t *testing.T) {
	// Normal mode with empty home => Threads() empty => save is a no-op.
	paths := config.NewVixPaths("", "", "/work")
	if paths.ThreadsOpen() != "" {
		t.Fatalf("expected empty ThreadsOpen with no home, got %q", paths.ThreadsOpen())
	}
	if err := saveThreadRecord(paths, sampleRecord()); err != nil {
		t.Errorf("save should be a no-op (nil), got %v", err)
	}
	_, found, err := loadOpenThreadRecord(paths, "sess-abc")
	if err != nil || found {
		t.Errorf("load on disabled store: found=%v err=%v", found, err)
	}
}

func TestFirstUserMessageAndSummary(t *testing.T) {
	rec := sampleRecord()
	if got := rec.firstUserMessage(); got != "first question" {
		t.Errorf("firstUserMessage = %q", got)
	}
	rec.Title = "A generated title"
	s := rec.summary()
	if s.ID != rec.ID || s.FirstMessage != "first question" || s.Model != rec.Model {
		t.Errorf("summary mismatch: %+v", s)
	}
	if s.Title != "A generated title" {
		t.Errorf("summary title = %q", s.Title)
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
	out := buildReplayMessages(msgs, nil, nil)
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

func TestBuildReplayMessagesInterleavesRetryNotices(t *testing.T) {
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("hi")),
		llm.NewAssistantMessage(llm.NewTextBlock("working")),
	}
	notices := []retryNoticeRecord{
		{AfterIdx: -1, Reason: "API overloaded", Attempt: 1, MaxRetries: 10, WaitSecs: 1},
		{AfterIdx: 1, Reason: "API overloaded", Attempt: 7, MaxRetries: 10, WaitSecs: 32},
	}
	out := buildReplayMessages(msgs, notices, nil)
	// -1 notice, user, assistant, idx-1 notice = 4 entries.
	if len(out) != 4 {
		t.Fatalf("replay messages = %d, want 4: %+v", len(out), out)
	}
	if out[0].Role != "system" || len(out[0].Blocks) != 1 || out[0].Blocks[0].Kind != "retry" {
		t.Fatalf("leading retry notice wrong: %+v", out[0])
	}
	if out[0].Blocks[0].Attempt != 1 || out[0].Blocks[0].Text != "API overloaded" {
		t.Errorf("leading retry fields wrong: %+v", out[0].Blocks[0])
	}
	if out[1].Role != "user" || out[2].Role != "assistant" {
		t.Errorf("message order wrong: %+v", out)
	}
	last := out[3]
	if last.Role != "system" || last.Blocks[0].Kind != "retry" || last.Blocks[0].Attempt != 7 || last.Blocks[0].WaitSecs != 32 {
		t.Errorf("trailing retry notice wrong: %+v", last)
	}
}

func TestBuildReplayMessagesInterleavesFailureNotices(t *testing.T) {
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("hi")),
		llm.NewAssistantMessage(llm.NewTextBlock("working")),
	}
	failures := []failureNoticeRecord{
		{AfterIdx: 1, StepID: "deny", Reason: "workflow failed: step 'deny' bash failed: exit status 1 (output: no GitHub access)"},
	}
	out := buildReplayMessages(msgs, nil, failures)
	// user, assistant, trailing failure notice = 3 entries.
	if len(out) != 3 {
		t.Fatalf("replay messages = %d, want 3: %+v", len(out), out)
	}
	last := out[2]
	if last.Role != "system" || len(last.Blocks) != 1 || last.Blocks[0].Kind != "error" {
		t.Fatalf("trailing failure notice wrong: %+v", last)
	}
	if last.Blocks[0].StepID != "deny" || !strings.Contains(last.Blocks[0].Text, "no GitHub access") {
		t.Errorf("failure notice fields wrong: %+v", last.Blocks[0])
	}
}

func TestBuildReplayMessagesFailureNoticeWithoutMessages(t *testing.T) {
	// A run that aborts before any message still surfaces its failure notice,
	// anchored before everything (-1), so an opened thread isn't blank.
	failures := []failureNoticeRecord{{AfterIdx: -1, StepID: "detect", Reason: "workflow failed: step 'detect' bash failed"}}
	out := buildReplayMessages(nil, nil, failures)
	if len(out) != 1 {
		t.Fatalf("replay messages = %d, want 1 (failure notice): %+v", len(out), out)
	}
	if out[0].Role != "system" || out[0].Blocks[0].Kind != "error" || out[0].Blocks[0].StepID != "detect" {
		t.Errorf("leading failure notice wrong: %+v", out[0])
	}
}

func TestBuildReplayMessagesNoticeAfterSkippedMessage(t *testing.T) {
	// An empty assistant message is skipped from output, but a notice anchored
	// to it must still be emitted (the failed agent produced no final text).
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("go")),
		llm.NewAssistantMessage(), // no blocks -> skipped
	}
	notices := []retryNoticeRecord{{AfterIdx: 1, Reason: "API overloaded", Attempt: 10, MaxRetries: 10}}
	out := buildReplayMessages(msgs, notices, nil)
	if len(out) != 2 {
		t.Fatalf("replay messages = %d, want 2 (user + notice): %+v", len(out), out)
	}
	if out[1].Blocks[0].Kind != "retry" || out[1].Blocks[0].Attempt != 10 {
		t.Errorf("notice anchored to skipped message lost: %+v", out)
	}
}

func TestBuildReplayMessagesTimestamp(t *testing.T) {
	ts := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	stamped := llm.NewUserMessage(llm.NewTextBlock("hi"))
	stamped.Timestamp = ts
	legacy := llm.NewAssistantMessage(llm.NewTextBlock("answer")) // zero timestamp

	out := buildReplayMessages([]llm.MessageParam{stamped, legacy}, nil, nil)
	if len(out) != 2 {
		t.Fatalf("replay messages = %d, want 2", len(out))
	}
	if want := ts.Format(time.RFC3339); out[0].Timestamp != want {
		t.Errorf("stamped Timestamp = %q, want %q", out[0].Timestamp, want)
	}
	if out[1].Timestamp != "" {
		t.Errorf("legacy (zero) Timestamp = %q, want empty", out[1].Timestamp)
	}
}

// newReplayThread builds a minimal Thread wired for emitReplay (eventChan +
// ctx). Persistence is disabled (empty paths) so persist() is a no-op.
func newReplayThread(t *testing.T, rec *threadRecord) *Thread {
	t.Helper()
	s := &Thread{
		id:         rec.ID,
		model:      "anthropic/new-default",
		eventChan:  make(chan protocol.ThreadEvent, 4),
		threadMode: rec.ThreadMode,
	}
	if s.threadMode == "" {
		s.threadMode = "chat"
	}
	s.activeWorkflow = rec.ActiveWorkflow
	s.messages = append([]llm.MessageParam(nil), rec.Messages...)
	s.attachRecord = rec
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

// captureReplay drives the early, content-only emitReplay and returns the
// event.replay it emits.
func captureReplay(t *testing.T, s *Thread) protocol.EventReplay {
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

// captureReplayReady drives finalizeReplay (the post-initBrain phase) and
// returns the event.replay_ready it emits.
func captureReplayReady(t *testing.T, s *Thread) protocol.EventReplayReady {
	t.Helper()
	s.finalizeReplay()
	select {
	case ev := <-s.eventChan:
		if ev.Type != "event.replay_ready" {
			t.Fatalf("event type = %q, want event.replay_ready", ev.Type)
		}
		rep, ok := ev.Data.(protocol.EventReplayReady)
		if !ok {
			t.Fatalf("event data type = %T, want EventReplayReady", ev.Data)
		}
		return rep
	default:
		t.Fatal("no event emitted")
		return protocol.EventReplayReady{}
	}
}

// The early emitReplay carries the transcript with Initializing=true, no
// warnings, the saved rec.Model (so restored turn separators stay forkable),
// and leaves attachRecord intact for finalizeReplay.
func TestEmitReplayContentIsInitializing(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/old-saved" // would warn — but not in the early phase
	s := newReplayThread(t, &rec)

	rep := captureReplay(t, s)
	if !rep.Initializing {
		t.Error("early replay should be marked Initializing")
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("early replay warnings = %v, want none", rep.Warnings)
	}
	if rep.Model != "anthropic/old-saved" {
		t.Errorf("early replay model = %q, want the saved rec.Model so restored turn separators stay forkable", rep.Model)
	}
	if s.attachRecord == nil {
		t.Error("attachRecord must survive the early replay for finalizeReplay")
	}
}

func TestFinalizeReplayModelChangedWarning(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/old-saved"
	s := newReplayThread(t, &rec) // s.model = anthropic/new-default

	rep := captureReplayReady(t, s)
	if len(rep.Warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 (model change)", rep.Warnings)
	}
	if rep.Model != "anthropic/new-default" {
		t.Errorf("replay model = %q, want current default", rep.Model)
	}
	if s.attachRecord != nil {
		t.Error("attachRecord should be cleared after finalizeReplay")
	}
}

func TestFinalizeReplayNoWarningWhenModelSame(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/new-default"
	s := newReplayThread(t, &rec)

	rep := captureReplayReady(t, s)
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", rep.Warnings)
	}
}

func TestFinalizeReplayWorkflowMissingFallsBackToChat(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/new-default" // avoid model warning
	rec.ThreadMode = "workflow"
	rec.ActiveWorkflow = "ghost-workflow"
	s := newReplayThread(t, &rec)
	// s.workflows is empty -> workflow no longer exists.

	rep := captureReplayReady(t, s)
	if rep.ThreadMode != "chat" || rep.ActiveWorkflow != "" {
		t.Errorf("expected fallback to chat: mode=%q wf=%q", rep.ThreadMode, rep.ActiveWorkflow)
	}
	if s.threadMode != "chat" || s.activeWorkflow != "" {
		t.Errorf("thread state not updated: mode=%q wf=%q", s.threadMode, s.activeWorkflow)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 (workflow missing)", rep.Warnings)
	}
}

func TestFinalizeReplayWorkflowPresentKept(t *testing.T) {
	rec := sampleRecord()
	rec.Model = "anthropic/new-default"
	rec.ThreadMode = "workflow"
	rec.ActiveWorkflow = "build"
	s := newReplayThread(t, &rec)
	s.workflows = []*WorkflowDef{{Name: "build"}}

	rep := captureReplayReady(t, s)
	if rep.ThreadMode != "workflow" || rep.ActiveWorkflow != "build" {
		t.Errorf("workflow should be kept: mode=%q wf=%q", rep.ThreadMode, rep.ActiveWorkflow)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", rep.Warnings)
	}
}

func TestDeleteThreadRecord(t *testing.T) {
	paths := testPaths(t)

	// Record in open/ is removed.
	open := sampleRecord()
	open.ID = "del-open"
	if err := saveThreadRecord(paths, open); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := deleteThreadRecord(paths, open.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(threadRecordPath(paths.ThreadsOpen(), open.ID)); !os.IsNotExist(err) {
		t.Error("record still present in open/ after delete")
	}

	// Record in closed/ is removed too.
	closed := sampleRecord()
	closed.ID = "del-closed"
	if err := saveThreadRecord(paths, closed); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := moveThreadToClosed(paths, closed.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := deleteThreadRecord(paths, closed.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(threadRecordPath(paths.ThreadsClosed(), closed.ID)); !os.IsNotExist(err) {
		t.Error("record still present in closed/ after delete")
	}

	// Missing record and disabled persistence are no-ops.
	if err := deleteThreadRecord(paths, "no-such-id"); err != nil {
		t.Errorf("delete missing: %v", err)
	}
	if err := deleteThreadRecord(config.NewVixPaths("", "", "/work"), "x"); err != nil {
		t.Errorf("delete with persistence disabled: %v", err)
	}
}

// writeClosedRecord saves rec and moves it to closed/, returning its path.
func writeClosedRecord(t *testing.T, paths config.VixPaths, rec threadRecord) string {
	t.Helper()
	if err := saveThreadRecord(paths, rec); err != nil {
		t.Fatalf("save %s: %v", rec.ID, err)
	}
	if err := moveThreadToClosed(paths, rec.ID); err != nil {
		t.Fatalf("move %s: %v", rec.ID, err)
	}
	return threadRecordPath(paths.ThreadsClosed(), rec.ID)
}

func TestTrimStaleClosedThreads(t *testing.T) {
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
	corruptOld := filepath.Join(paths.ThreadsClosed(), "corrupt-old.json")
	if err := os.WriteFile(corruptOld, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * week)
	if err := os.Chtimes(corruptOld, old, old); err != nil {
		t.Fatal(err)
	}

	// Corrupt file with a fresh mtime is kept.
	corruptFresh := filepath.Join(paths.ThreadsClosed(), "corrupt-fresh.json")
	if err := os.WriteFile(corruptFresh, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stale record still in open/ must not be touched.
	openStale := sampleRecord()
	openStale.ID = "open-stale"
	openStale.LastRequestAt = time.Now().Add(-2 * week)
	if err := saveThreadRecord(paths, openStale); err != nil {
		t.Fatalf("save: %v", err)
	}

	trimStaleClosedThreads(paths, week)

	for _, p := range []string{stalePath, staleStartPath, corruptOld} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been trimmed", filepath.Base(p))
		}
	}
	for _, p := range []string{freshPath, corruptFresh, threadRecordPath(paths.ThreadsOpen(), openStale.ID)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been kept: %v", filepath.Base(p), err)
		}
	}
}

func TestTrimStaleClosedThreadsNever(t *testing.T) {
	paths := testPaths(t)

	stale := sampleRecord()
	stale.ID = "stale"
	stale.LastRequestAt = time.Now().Add(-365 * 24 * time.Hour)
	p := writeClosedRecord(t, paths, stale)

	// maxAge <= 0 means retention disabled ("never"): nothing is removed.
	trimStaleClosedThreads(paths, 0)
	trimStaleClosedThreads(paths, -time.Hour)

	if _, err := os.Stat(p); err != nil {
		t.Errorf("record should have been kept with retention disabled: %v", err)
	}
}

// ── Unread flag ──

// TestUnreadRoundTrip: the thread-global unread flag persists and surfaces in
// summaries; legacy records without the field read as seen.
func TestUnreadRoundTrip(t *testing.T) {
	paths := testPaths(t)

	rec := sampleRecord()
	rec.Unread = true
	if err := saveThreadRecord(paths, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, found, _ := loadOpenThreadRecord(paths, rec.ID)
	if !found || !got.Unread {
		t.Fatalf("unread flag lost: found=%v rec=%+v", found, got)
	}
	if !got.summary().Unread {
		t.Fatal("summary must carry Unread")
	}

	// Legacy record: no unread field on disk → read.
	legacy := sampleRecord()
	legacy.ID = "sess-legacy"
	if err := saveThreadRecord(paths, legacy); err != nil {
		t.Fatalf("save legacy: %v", err)
	}
	got, _, _ = loadOpenThreadRecord(paths, legacy.ID)
	if got.Unread || got.summary().Unread {
		t.Fatal("legacy record must read as seen")
	}
}

// TestMarkReadCommandClearsUnread: buildRecord reflects the thread flag, and
// the mark_read transition persists.
func TestMarkReadCommandClearsUnread(t *testing.T) {
	paths := testPaths(t)
	srv := NewServer("/tmp/unused.sock", config.Credential{}, "t", "m", &config.DaemonConfig{}, nil)
	sess := NewThread("sess-mr", srv, nil, "m", "/work", paths.Override(), false, true, true, true, context.Background())

	sess.unread = true
	sess.persist()
	got, found, _ := loadOpenThreadRecord(sess.paths, "sess-mr")
	if !found || !got.Unread {
		t.Fatalf("turn-end persist must carry unread, got %+v", got)
	}

	// What the thread.mark_read command handler does:
	sess.unread = false
	sess.persist()
	got, _, _ = loadOpenThreadRecord(sess.paths, "sess-mr")
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
		rec := threadRecord{
			ID: id, CWD: "/work", Origin: "vix", Trigger: trig,
			JobStatus: status, Unread: unread,
			ThreadMode: "chat", StartedAt: time.Now().Add(-age),
		}
		if err := saveThreadRecord(paths, rec); err != nil {
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
	for _, r := range listThreadRecordsIn(paths.ThreadsOpen()) {
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
