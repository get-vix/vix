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
	groups := []userDirGroupView{
		{dir: workDir, rows: []userRowView{{sum: protocol.ThreadSummary{ID: "s1abc", Title: "Alpha title", LastRequestAt: "2026-01-01T00:00:00Z"}}}},
		{dir: "/opt/proj", rows: []userRowView{{sum: protocol.ThreadSummary{ID: "s2xyz", Title: "Beta title", LastRequestAt: "2026-01-01T00:00:00Z"}}}},
	}
	out := renderThreadsView(groups, nil, 120, 40, NewStyles(true), 0, "")

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
