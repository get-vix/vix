package harness

import "time"

// WaitForLLMRequests blocks until the mock has received at least n requests,
// i.e. the daemon has driven that many turns. Use it for daemon/wire/disk
// assertions that don't depend on the TUI rendering anything — it decouples
// those scenarios from the screen entirely. Bounded by the per-test deadline.
func (h *Harness) WaitForLLMRequests(n int) {
	h.t.Helper()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if len(h.Mock.Requests()) >= n {
			return
		}
		select {
		case <-h.ctx.Done():
			h.t.Fatalf("e2e: timed out waiting for %d LLM requests (got %d)", n, len(h.Mock.Requests()))
		case <-tick.C:
		}
	}
}
