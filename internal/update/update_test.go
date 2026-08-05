package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/config"
)

func TestNewerThan(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.4.0", "v1.2.0", true},
		{"v1.10.0", "v1.9.0", true}, // numeric, not lexical
		{"1.4.0", "v1.4.0", false},  // equal, leading v optional
		{"v1.4", "v1.4.0", false},   // missing field == 0
		{"v1.4.1", "v1.4", true},
		{"v1.4.0-rc1", "v1.4.0", false}, // pre-release tail ignored → equal
		{"dev", "v1.4.0", false},        // dev never newer
		{"v1.4.0", "dev", true},         // any real tag beats dev
		{"", "v1.4.0", false},
	}
	for _, c := range cases {
		if got := NewerThan(c.a, c.b); got != c.want {
			t.Errorf("NewerThan(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIsDev(t *testing.T) {
	for _, v := range []string{"", "dev", "  dev  "} {
		if !IsDev(v) {
			t.Errorf("IsDev(%q) = false, want true", v)
		}
	}
	if IsDev("v1.0.0") {
		t.Errorf("IsDev(v1.0.0) = true, want false")
	}
}

func TestRunDailyCheck(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	calls := 0
	net := func(context.Context) (Release, error) {
		calls++
		return Release{Tag: "v9.9.9", URL: "https://example/v9.9.9"}, nil
	}

	st := RunDailyCheck("v1.0.0", statePath, net)
	if st.Latest != "v9.9.9" || st.URL == "" {
		t.Fatalf("first check: got Latest=%q URL=%q, want v9.9.9", st.Latest, st.URL)
	}
	if calls != 1 {
		t.Fatalf("first check: network calls = %d, want 1", calls)
	}

	// Same-day re-run must use the cached state, not hit the network again.
	st = RunDailyCheck("v1.0.0", statePath, net)
	if calls != 1 {
		t.Fatalf("second check: network calls = %d, want 1 (cached)", calls)
	}
	if st.Latest != "v9.9.9" {
		t.Fatalf("second check: Latest=%q, want v9.9.9 from cache", st.Latest)
	}

	// When already on the latest, Latest must be empty (no update prompt).
	st = RunDailyCheck("v9.9.9", statePath, net)
	if st.Latest != "" {
		t.Fatalf("up-to-date: Latest=%q, want empty", st.Latest)
	}
}

func TestRunDailyCheckDevSkips(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	net := func(context.Context) (Release, error) {
		t.Fatal("network should not be called for a dev build")
		return Release{}, nil
	}
	st := RunDailyCheck("dev", statePath, net)
	if st.Latest != "" {
		t.Fatalf("dev build: Latest=%q, want empty", st.Latest)
	}
}

func TestRunDailyCheckNetworkErrorDoesNotStampDate(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	failing := func(context.Context) (Release, error) { return Release{}, errors.New("offline") }

	st := RunDailyCheck("v1.0.0", statePath, failing)
	if st.Latest != "" {
		t.Fatalf("network error: Latest=%q, want empty", st.Latest)
	}
	// The date must not be stamped, so a later (succeeding) run still retries.
	if saved := config.ReadState(statePath); saved.LastUpdateCheck != "" {
		t.Fatalf("network error stamped LastUpdateCheck=%q, want empty", saved.LastUpdateCheck)
	}
}
