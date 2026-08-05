package scenarios

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestRetryableStatusesRecover proves vix retries transient API errors and the
// turn recovers. It tables the retryable status codes (complementing the 503
// covered by stream.retry). Each retryable error is followed by a success, and
// vix's backoff (~1s for the first retry) keeps it well within the deadline.
// stream.retry
func TestRetryableStatusesRecover(t *testing.T) {
	for _, status := range []int{429, 500, 529} {
		status := status
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			h := harness.Start(t, harness.Meta{
				Category:    "stream",
				Subcategory: "stream.retry",
				Description: fmt.Sprintf("a %d response is retried and the turn recovers", status),
				Wire:        harness.WireMessages,
				Variant:     fmt.Sprintf("status-%d", status),
			})

			h.UI.WaitStable(400 * time.Millisecond)
			h.Mock.Enqueue(
				harness.HTTPError(status, "transient error"),
				harness.Text("Recovered after the retry."),
			)
			h.UI.Type("do the thing")
			h.UI.Enter()
			h.UI.ResolveToolPrompts("Recovered after the retry.")
			h.UI.Shot(fmt.Sprintf("recovered-%d", status))

			if got := len(h.Mock.Requests()); got < 2 {
				t.Fatalf("status %d: expected >=2 requests (error + retry), got %d", status, got)
			}
			if !h.UI.Contains("Recovered after the retry.") {
				t.Fatalf("status %d: recovered text not rendered", status)
			}
		})
	}
}

// TestNonRetryableStatusesFailFast proves permanent API errors fail immediately
// (no retry storm) and are surfaced. Complements the 400 covered by stream.error.
// stream.error
func TestNonRetryableStatusesFailFast(t *testing.T) {
	cases := []struct {
		status int
		logKey string // a substring expected in the daemon log for this status
	}{
		{401, "401"},
		{404, "404"},
	}
	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("status-%d", c.status), func(t *testing.T) {
			h := harness.Start(t, harness.Meta{
				Category:    "stream",
				Subcategory: "stream.error",
				Description: fmt.Sprintf("a %d response fails fast and is surfaced (no retry)", c.status),
				Wire:        harness.WireMessages,
				Variant:     fmt.Sprintf("status-%d", c.status),
			})

			h.UI.WaitStable(400 * time.Millisecond)
			h.Mock.Enqueue(harness.HTTPError(c.status, "permanent error"))
			h.UI.Type("trigger a permanent error")
			h.UI.Enter()
			h.WaitForLLMRequests(1)
			h.UI.WaitStable(500 * time.Millisecond)
			h.UI.Shot(fmt.Sprintf("failed-%d", c.status))

			if got := len(h.Mock.Requests()); got != 1 {
				t.Fatalf("status %d should not be retried; requests=%d", c.status, got)
			}
			if log := h.Daemon.LogTail(200); !strings.Contains(log, c.logKey) {
				t.Fatalf("status %d not recorded in daemon log:\n%s", c.status, log)
			}
		})
	}
}
