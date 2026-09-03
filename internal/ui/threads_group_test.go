package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/protocol"
)

// userRec builds a not-attached, user-initiated thread record for the given
// directory with an activity timestamp used for recency ordering.
func userRec(id, cwd, lastReq string) protocol.ThreadSummary {
	return protocol.ThreadSummary{ID: id, CWD: cwd, Title: "title-" + id, LastRequestAt: lastReq}
}

// liveAt builds a live (attached) user thread in the given directory whose
// daemon creation time is fixed, so tests can assert creation-time ordering.
// The client is nil (a real one can't be constructed cross-package), so the
// cached startedAt supplies the sort key via ThreadState.createdAt.
func liveAt(dir, rfc3339 string) *ThreadState {
	s := newThreadState(testCfg(dir), nil)
	s.phase = phaseLive
	t, _ := time.Parse(time.RFC3339, rfc3339)
	s.startedAt = t
	return s
}

// TestAbbreviatePath replaces the home prefix with "~" and labels empties.
func TestAbbreviatePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	cases := []struct{ in, want string }{
		{"", "(unknown)"},
		{"   ", "(unknown)"},
		{home, "~"},
		{filepath.Join(home, "Developer", "vix"), "~" + string(os.PathSeparator) + filepath.Join("Developer", "vix")},
		{"/opt/elsewhere", "/opt/elsewhere"},
	}
	for _, c := range cases {
		if got := abbreviatePath(c.in); got != c.want {
			t.Errorf("abbreviatePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestUserDirBlocksGrouping: user threads are grouped by working directory with
// the current cwd first and other directories by most-recent activity (desc).
func TestUserDirBlocksGrouping(t *testing.T) {
	liveWork := liveAt("/work", "2026-01-04T00:00:00Z") // current cwd, newest in /work
	liveBeta := liveAt("/beta", "2026-01-01T00:00:00Z") // attached other-dir thread, oldest in /beta
	m := &Model{
		cwd:     "/work",
		threads: []*ThreadState{liveWork, liveBeta},
		userThreadRecords: []protocol.ThreadSummary{
			{ID: "rWork", CWD: "/work", Title: "rWork", StartedAt: "2026-01-01T00:00:00Z", LastRequestAt: "2026-01-01T00:00:00Z"},
			{ID: "rAlpha", CWD: "/alpha", Title: "rAlpha", StartedAt: "2026-01-02T00:00:00Z", LastRequestAt: "2026-01-02T00:00:00Z"},
			{ID: "rBeta", CWD: "/beta", Title: "rBeta", StartedAt: "2026-01-03T00:00:00Z", LastRequestAt: "2026-01-03T00:00:00Z"},
		},
	}

	blocks := m.userDirBlocks()
	if len(blocks) != 3 {
		t.Fatalf("want 3 dir blocks, got %d", len(blocks))
	}
	// Current cwd first.
	if blocks[0].dir != "/work" {
		t.Errorf("block[0].dir = %q, want /work (current cwd first)", blocks[0].dir)
	}
	// /beta (activity 01-03) ranks above /alpha (01-02).
	if blocks[1].dir != "/beta" || blocks[2].dir != "/alpha" {
		t.Errorf("other-dir order = [%q, %q], want [/beta, /alpha]", blocks[1].dir, blocks[2].dir)
	}
	// Within /work: record rWork (01-01) precedes the newer live thread (01-04),
	// i.e. the live thread is NOT hoisted ahead of an older record.
	if len(blocks[0].rows) != 2 || blocks[0].rows[0].sum == nil || blocks[0].rows[0].sum.ID != "rWork" || blocks[0].rows[1].liveIdx != 0 {
		t.Errorf("/work rows = %+v, want [record rWork, live#0]", blocks[0].rows)
	}
	// Within /beta: the older live thread (01-01) precedes its newer record (01-03).
	if len(blocks[1].rows) != 2 || blocks[1].rows[0].liveIdx != 1 || blocks[1].rows[1].sum == nil || blocks[1].rows[1].sum.ID != "rBeta" {
		t.Errorf("/beta rows = %+v, want [live#1, record rBeta]", blocks[1].rows)
	}
}

// TestUserDirBlocksOrdersByCreatedAt: within one directory, live threads and
// not-attached records interleave strictly by creation time (asc), and a
// still-connecting thread (unknown start time) sorts last.
func TestUserDirBlocksOrdersByCreatedAt(t *testing.T) {
	oldRec := protocol.ThreadSummary{ID: "old", CWD: "/work", Title: "old", StartedAt: "2026-01-01T00:00:00Z", LastRequestAt: "2026-01-01T00:00:00Z"}
	midLive := liveAt("/work", "2026-01-02T00:00:00Z")
	newRec := protocol.ThreadSummary{ID: "new", CWD: "/work", Title: "new", StartedAt: "2026-01-03T00:00:00Z", LastRequestAt: "2026-01-03T00:00:00Z"}
	connecting := newThreadState(testCfg("/work"), nil) // no start time yet
	connecting.phase = phaseLive

	m := &Model{
		cwd:               "/work",
		threads:           []*ThreadState{midLive, connecting},
		userThreadRecords: []protocol.ThreadSummary{newRec, oldRec}, // deliberately out of order
	}

	blocks := m.userDirBlocks()
	if len(blocks) != 1 {
		t.Fatalf("want 1 dir block, got %d", len(blocks))
	}
	got := make([]string, 0, len(blocks[0].rows))
	for _, r := range blocks[0].rows {
		if r.sum != nil {
			got = append(got, r.sum.ID)
		} else {
			got = append(got, "live")
		}
	}
	// old (01-01), midLive (01-02), new (01-03), then the connecting thread last.
	want := []string{"old", "live", "new", "live"}
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row order = %v, want %v", got, want)
		}
	}
	// The last row must be the still-connecting thread (no client, zero start).
	if last := blocks[0].rows[len(blocks[0].rows)-1]; last.liveIdx != 1 {
		t.Errorf("connecting thread should sort last, got liveIdx=%d", last.liveIdx)
	}
}

// TestUserDirBlocksLiveThreadCountsForRecency: a directory whose thread is
// currently open (live, no saved record) keeps its recency spot instead of
// sinking to the bottom. This guards the bug where a restored thread that
// re-attached on launch flipped its group's position (its block's recency key
// dropped to zero because only saved records fed it).
func TestUserDirBlocksLiveThreadCountsForRecency(t *testing.T) {
	liveWork := liveAt("/work", "2026-01-04T00:00:00Z")     // current cwd
	liveWarren := liveAt("/warren", "2026-01-05T00:00:00Z") // open thread, most recent activity
	m := &Model{
		cwd:     "/work",
		threads: []*ThreadState{liveWork, liveWarren},
		userThreadRecords: []protocol.ThreadSummary{
			{ID: "rBeta", CWD: "/beta", Title: "rBeta", StartedAt: "2026-01-03T00:00:00Z", LastRequestAt: "2026-01-03T00:00:00Z"},
		},
	}

	blocks := m.userDirBlocks()
	if len(blocks) != 3 {
		t.Fatalf("want 3 dir blocks, got %d", len(blocks))
	}
	if blocks[0].dir != "/work" {
		t.Errorf("block[0].dir = %q, want /work (current cwd first)", blocks[0].dir)
	}
	// /warren (live activity 01-05) must rank above /beta (record 01-03), even
	// though /warren has no saved record — its open thread feeds the recency key.
	if blocks[1].dir != "/warren" || blocks[2].dir != "/beta" {
		t.Errorf("other-dir order = [%q, %q], want [/warren, /beta]", blocks[1].dir, blocks[2].dir)
	}
}

// TestThreadRowTargetsIncludesUserRecords: the flat selection order lists the
// User-initiated rows (grouped by dir) before the Vix-initiated rows, so the
// selection index space covers cross-directory user records.
func TestThreadRowTargetsIncludesUserRecords(t *testing.T) {
	liveWork := newThreadState(testCfg("/work"), nil)
	vixRec := protocol.ThreadSummary{ID: "vixRun", CWD: "/job", Origin: "vix", StartedAt: "2026-01-05T00:00:00Z"}
	m := &Model{
		cwd:     "/work",
		threads: []*ThreadState{liveWork},
		userThreadRecords: []protocol.ThreadSummary{
			userRec("rWork", "/work", "2026-01-01T00:00:00Z"),
			userRec("rAlpha", "/alpha", "2026-01-02T00:00:00Z"),
		},
		vixThreads: []protocol.ThreadSummary{vixRec},
	}

	rows := m.threadRowTargets()
	if len(rows) != 4 {
		t.Fatalf("want 4 rows (1 live + 2 user records + 1 vix), got %d", len(rows))
	}
	// User section: live /work, record rWork, record rAlpha.
	if rows[0].sum != nil || rows[0].liveIdx != 0 {
		t.Errorf("row[0] should be the live /work thread, got %+v", rows[0])
	}
	if rows[1].sum == nil || rows[1].sum.ID != "rWork" {
		t.Errorf("row[1] should be record rWork, got %+v", rows[1])
	}
	if rows[2].sum == nil || rows[2].sum.ID != "rAlpha" {
		t.Errorf("row[2] should be record rAlpha, got %+v", rows[2])
	}
	// Vix section last.
	if rows[3].sum == nil || rows[3].sum.ID != "vixRun" {
		t.Errorf("row[3] should be the vix record, got %+v", rows[3])
	}
}

// TestUserDirBlocksDedupsLiveRecords: a record that is already live in this
// window (attached but the list hasn't refreshed) is not shown twice.
func TestUserDirBlocksDedupsLiveRecords(t *testing.T) {
	live := newThreadState(testCfg("/work"), nil)
	live.daemonThreadID = "dup-id"
	m := &Model{
		cwd:     "/work",
		threads: []*ThreadState{live},
		userThreadRecords: []protocol.ThreadSummary{
			{ID: "dup-id", CWD: "/work", Title: "dup", LastRequestAt: "2026-01-01T00:00:00Z"},
			userRec("keep", "/work", "2026-01-02T00:00:00Z"),
		},
	}
	blocks := m.userDirBlocks()
	if len(blocks) != 1 {
		t.Fatalf("want 1 dir block, got %d", len(blocks))
	}
	// Live row + the non-duplicate record only (the "dup-id" record is dropped).
	if len(blocks[0].rows) != 2 {
		t.Fatalf("want 2 rows (live + keep), got %d: %+v", len(blocks[0].rows), blocks[0].rows)
	}
	for _, r := range blocks[0].rows {
		if r.sum != nil && r.sum.ID == "dup-id" {
			t.Error("record duplicating a live thread should be dropped")
		}
	}
}

// TestRenderThreadsViewGroupsByDir: the User-initiated group renders a path
// subtitle for every directory (always shown) and the rows under each.
func TestRenderThreadsViewGroupsByDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	workDir := filepath.Join(home, "work")
	rows := []threadListRow{
		{kind: rowUserHeader},
		{kind: rowDirHeader, dir: workDir, count: 1},
		{kind: rowUserThread, liveIdx: -1, sum: protocol.ThreadSummary{ID: "s1abc", Title: "Alpha title", LastRequestAt: "2026-01-01T00:00:00Z"}},
		{kind: rowDirHeader, dir: "/opt/proj", count: 1},
		{kind: rowUserThread, liveIdx: -1, sum: protocol.ThreadSummary{ID: "s2xyz", Title: "Beta title", LastRequestAt: "2026-01-01T00:00:00Z"}},
	}
	out := renderThreadsView(rows, 120, 40, NewStyles(true), 0, "")

	for _, want := range []string{
		"User-initiated",
		"~" + string(os.PathSeparator) + "work", // home-abbreviated subtitle, always shown
		"/opt/proj",                             // second directory subtitle
		"Alpha title", "Beta title",
		"s1abc", "s2xyz",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderThreadsView output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderThreadsViewFoldedDirHidesRows: a collapsed directory header renders
// (with the ▸ glyph and a hidden count) but its thread rows are omitted.
func TestRenderThreadsViewFoldedDirHidesRows(t *testing.T) {
	rows := []threadListRow{
		{kind: rowUserHeader},
		{kind: rowDirHeader, dir: "/opt/proj", collapsed: true, count: 2},
	}
	out := renderThreadsView(rows, 120, 40, NewStyles(true), 0, "")

	if !strings.Contains(out, "▸") {
		t.Errorf("collapsed dir header should show the ▸ glyph\n---\n%s", out)
	}
	if !strings.Contains(out, "(2)") {
		t.Errorf("collapsed dir header should show the hidden count (2)\n---\n%s", out)
	}
	if !strings.Contains(out, "/opt/proj") {
		t.Errorf("collapsed dir header should still show the path\n---\n%s", out)
	}
}

// TestThreadListRowsFolding: folding a directory drops its thread rows from the
// selection space (the header stays) and shifts the Vix rows up accordingly.
func TestThreadListRowsFolding(t *testing.T) {
	vixRec := protocol.ThreadSummary{ID: "vixRun", CWD: "/job", Origin: "vix", StartedAt: "2026-01-05T00:00:00Z"}
	m := &Model{
		cwd:     "/work",
		threads: []*ThreadState{},
		userThreadRecords: []protocol.ThreadSummary{
			userRec("rWork", "/work", "2026-01-01T00:00:00Z"),
			userRec("rAlpha", "/alpha", "2026-01-02T00:00:00Z"),
		},
		vixThreads: []protocol.ThreadSummary{vixRec},
	}

	// Expanded: one dir header + one thread row per directory, then the vix row.
	sel := m.selectableThreadRows()
	if len(sel) != 5 {
		t.Fatalf("expanded selectable rows = %d, want 5 (2 dir headers + 2 threads + 1 vix)", len(sel))
	}
	if sel[0].kind != rowDirHeader || sel[0].dir != "/work" {
		t.Fatalf("sel[0] = %+v, want /work dir header", sel[0])
	}
	if sel[1].kind != rowUserThread || sel[1].sum.ID != "rWork" {
		t.Fatalf("sel[1] = %+v, want rWork thread", sel[1])
	}

	// Fold /work: its thread row disappears, header remains, vix row shifts up.
	m.collapsedDirs = map[string]bool{"/work": true}
	sel = m.selectableThreadRows()
	if len(sel) != 4 {
		t.Fatalf("folded selectable rows = %d, want 4", len(sel))
	}
	for _, r := range sel {
		if r.kind == rowUserThread && r.sum.ID == "rWork" {
			t.Fatal("folded /work should hide the rWork thread row")
		}
	}
	if sel[0].kind != rowDirHeader || sel[0].dir != "/work" || !sel[0].collapsed {
		t.Fatalf("sel[0] = %+v, want collapsed /work dir header", sel[0])
	}
	last := sel[len(sel)-1]
	if last.kind != rowVixThread || last.sum.ID != "vixRun" {
		t.Fatalf("last selectable row = %+v, want the vixRun vix thread", last)
	}
}

// twoBlockModel builds a Model with two user directory blocks (/work and
// /alpha), each holding one persisted thread record, plus one Vix-initiated
// record. Selectable rows: [0]=/alpha hdr [1]=rAlpha [2]=/work hdr [3]=rWork
// [4]=vixRun (dir blocks are ordered by most-recent activity).
func twoBlockModel() *Model {
	return &Model{
		cwd:     "/work",
		threads: []*ThreadState{},
		userThreadRecords: []protocol.ThreadSummary{
			userRec("rWork", "/work", "2026-01-01T00:00:00Z"),
			userRec("rAlpha", "/alpha", "2026-01-02T00:00:00Z"),
		},
		vixThreads: []protocol.ThreadSummary{
			{ID: "vixRun", CWD: "/job", Origin: "vix", StartedAt: "2026-01-05T00:00:00Z"},
		},
	}
}

// dirHeaderIndex returns the selectable-row index of the given directory header.
func dirHeaderIndex(m *Model, dir string) int {
	for i, r := range m.selectableThreadRows() {
		if r.kind == rowDirHeader && r.dir == dir {
			return i
		}
	}
	return -1
}

// TestFoldSelectedDirOnHeader: space/enter on a directory header toggles its
// fold state and leaves the cursor on the header.
func TestFoldSelectedDirOnHeader(t *testing.T) {
	m := twoBlockModel()
	hdr := dirHeaderIndex(m, "/work")
	m.threadsSelected = hdr

	if !m.foldSelectedDir() || !m.collapsedDirs["/work"] {
		t.Fatalf("first fold: acted=%v collapsed=%v, want true/true", true, m.collapsedDirs["/work"])
	}
	if got := m.selectableThreadRows()[m.threadsSelected]; got.kind != rowDirHeader || got.dir != "/work" {
		t.Fatalf("cursor after fold = %+v, want /work header", got)
	}
	if !m.foldSelectedDir() || m.collapsedDirs["/work"] {
		t.Fatalf("second fold should unfold /work, collapsed=%v", m.collapsedDirs["/work"])
	}
}

// TestFoldSelectedDirFromThreadRow: space on a thread row folds its enclosing
// directory and re-anchors the cursor on that directory's header.
func TestFoldSelectedDirFromThreadRow(t *testing.T) {
	m := twoBlockModel()
	// Cursor on the rWork thread row (under /work).
	sel := m.selectableThreadRows()
	row := -1
	for i, r := range sel {
		if r.kind == rowUserThread && r.sum.ID == "rWork" {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("could not find rWork thread row")
	}
	m.threadsSelected = row

	if !m.foldSelectedDir() || !m.collapsedDirs["/work"] {
		t.Fatalf("folding from thread row should collapse /work, collapsed=%v", m.collapsedDirs["/work"])
	}
	got := m.selectableThreadRows()[m.threadsSelected]
	if got.kind != rowDirHeader || got.dir != "/work" {
		t.Fatalf("cursor after fold from thread row = %+v, want /work header", got)
	}
}

// TestFoldSelectedDirOnVixRow: space on a Vix-initiated row is a no-op (those
// rows have no enclosing directory header).
func TestFoldSelectedDirOnVixRow(t *testing.T) {
	m := twoBlockModel()
	sel := m.selectableThreadRows()
	m.threadsSelected = len(sel) - 1 // the vixRun row
	if m.selectableThreadRows()[m.threadsSelected].kind != rowVixThread {
		t.Fatalf("last row = %+v, want vix thread", m.selectableThreadRows()[m.threadsSelected])
	}
	before := m.threadsSelected
	if m.foldSelectedDir() {
		t.Error("foldSelectedDir on a vix row should report false")
	}
	if m.threadsSelected != before || len(m.collapsedDirs) != 0 {
		t.Errorf("vix-row fold changed state: cursor %d->%d, collapsed=%v", before, m.threadsSelected, m.collapsedDirs)
	}
}

// TestSelectEnclosingDirFromThreadRow: left arrow moves the cursor from a
// thread row to its nearest preceding directory header.
func TestSelectEnclosingDirFromThreadRow(t *testing.T) {
	m := twoBlockModel()
	// rWork lives in the second block; its header is /work.
	sel := m.selectableThreadRows()
	for i, r := range sel {
		if r.kind == rowUserThread && r.sum.ID == "rWork" {
			m.threadsSelected = i
		}
	}
	if !m.selectEnclosingDir() {
		t.Fatal("selectEnclosingDir should move from a thread row")
	}
	got := m.selectableThreadRows()[m.threadsSelected]
	if got.kind != rowDirHeader || got.dir != "/work" {
		t.Fatalf("cursor after left = %+v, want /work header", got)
	}
}

// TestSelectEnclosingDirNoOp: left arrow does nothing on a directory header or
// on a Vix-initiated row.
func TestSelectEnclosingDirNoOp(t *testing.T) {
	m := twoBlockModel()

	m.threadsSelected = dirHeaderIndex(m, "/alpha")
	if m.selectEnclosingDir() {
		t.Error("selectEnclosingDir on a dir header should report false")
	}

	sel := m.selectableThreadRows()
	m.threadsSelected = len(sel) - 1 // vix row
	before := m.threadsSelected
	if m.selectEnclosingDir() || m.threadsSelected != before {
		t.Errorf("selectEnclosingDir on a vix row should be a no-op, cursor %d->%d", before, m.threadsSelected)
	}
}

// TestThreadsSelectionOnDirHeader: with the cursor on a directory header,
// threadsSelectedIdx and vixSelectedSummary both report false (Enter folds
// instead of opening a thread).
func TestThreadsSelectionOnDirHeader(t *testing.T) {
	m := &Model{
		cwd:     "/work",
		threads: []*ThreadState{},
		userThreadRecords: []protocol.ThreadSummary{
			userRec("rWork", "/work", "2026-01-01T00:00:00Z"),
		},
	}
	// sel[0] is the /work dir header.
	m.threadsSelected = 0
	if _, ok := m.threadsSelectedIdx(); ok {
		t.Error("threadsSelectedIdx should be false on a directory header")
	}
	if _, ok := m.vixSelectedSummary(); ok {
		t.Error("vixSelectedSummary should be false on a directory header")
	}
	// sel[1] is the rWork record row: vixSelectedSummary resolves it.
	m.threadsSelected = 1
	if sum, ok := m.vixSelectedSummary(); !ok || sum.ID != "rWork" {
		t.Errorf("vixSelectedSummary on record row = (%+v, %v), want rWork,true", sum, ok)
	}
}
