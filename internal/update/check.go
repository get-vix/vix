package update

import (
	"context"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// Status is the outcome of a release check: the running version, the newest
// release tag (empty when up-to-date / unknown), its page URL, and the detected
// install method.
type Status struct {
	Current string
	Latest  string
	URL     string
	Method  string
}

// RunDailyCheck compares current against the newest GitHub release at most once
// per calendar day. The result of the most recent check is cached in state.json
// (statePath); on a same-day re-run the cached release is reused without hitting
// the network. Returns a Status whose Latest is non-empty only when a strictly
// newer release exists. The check is skipped entirely (Latest empty) for dev
// builds and when the user has disabled it.
//
// network is the function used to fetch the latest release; production callers
// pass LatestRelease. It is a parameter so tests can stub it.
func RunDailyCheck(current, statePath string, network func(context.Context) (Release, error)) Status {
	method := DetectMethod()
	st := Status{Current: current, Method: method}
	if IsDev(current) || !config.UpdateCheckEnabled() {
		return st
	}

	today := time.Now().Format("2006-01-02")
	saved := config.ReadState(statePath)

	latest, url := saved.LatestKnown, saved.LatestURL
	if saved.LastUpdateCheck != today {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		rel, err := network(ctx)
		if err == nil {
			latest, url = rel.Tag, rel.URL
			saved.LastUpdateCheck = today
			saved.LatestKnown = rel.Tag
			saved.LatestURL = rel.URL
			_ = config.WriteState(statePath, saved)
		}
		// On error we fall back to the cached values (which may be empty) and
		// deliberately do not stamp today's date, so the next run retries.
	}

	if NewerThan(latest, current) {
		st.Latest = latest
		st.URL = url
	}
	return st
}
