package daemon

import (
	"testing"
	"time"

	"github.com/get-vix/vix/internal/protocol"
)

func TestAggregateThreadDirs_RanksByCountThenRecency(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := func(cwd string, last time.Time, origin string) threadRecord {
		return threadRecord{CWD: cwd, StartedAt: base, LastRequestAt: last, Origin: origin}
	}
	recs := []threadRecord{
		rec("/a", base.Add(1*time.Hour), ""),
		rec("/a", base.Add(2*time.Hour), ""),
		rec("/b", base.Add(5*time.Hour), ""),
		rec("/c", base.Add(9*time.Hour), ""), // most recent single-thread dir
		// vix-initiated records are excluded from the ranking.
		rec("/vixdir", base.Add(10*time.Hour), "vix"),
		// blank cwd is skipped.
		rec("", base.Add(11*time.Hour), ""),
	}

	got := aggregateThreadDirs(recs)

	if len(got) != 3 {
		t.Fatalf("want 3 dirs (vix + blank excluded), got %d: %+v", len(got), got)
	}
	// /a has the highest count -> first.
	if got[0].Path != "/a" || got[0].Count != 2 {
		t.Errorf("first should be /a x2, got %+v", got[0])
	}
	// /b and /c both count 1; /c is more recent -> before /b.
	if got[1].Path != "/c" || got[2].Path != "/b" {
		t.Errorf("recency tiebreak wrong: got %q then %q", got[1].Path, got[2].Path)
	}
	// /a's LastRequestAt reflects the newest of its two threads.
	if got[0].LastRequestAt != base.Add(2*time.Hour).Format(time.RFC3339) {
		t.Errorf("/a LastRequestAt = %q, want newest thread time", got[0].LastRequestAt)
	}
	for _, d := range got {
		if d.Path == "/vixdir" {
			t.Error("vix-initiated directory should not appear in ranking")
		}
	}
}

func TestAggregateThreadDirs_Empty(t *testing.T) {
	if got := aggregateThreadDirs(nil); len(got) != 0 {
		t.Errorf("empty input should yield no dirs, got %+v", got)
	}
}

func TestAggregateThreadDirs_FallsBackToStartedAt(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := []threadRecord{{CWD: "/only", StartedAt: base}}
	got := aggregateThreadDirs(recs)
	if len(got) != 1 || got[0].LastRequestAt != base.Format(time.RFC3339) {
		t.Fatalf("should fall back to StartedAt when LastRequestAt is zero: %+v", got)
	}
	_ = protocol.DirUsage{}
}
