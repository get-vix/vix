package telemetry

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
)

// wireClient installs c as the active telemetry client (as if Init had
// succeeded) and returns a restore func.
func wireClient(c posthog.Client) func() {
	prevClient, prevEnabled, prevDevice, prevVersion, prevMode := client, enabled, deviceID, version, mode
	client, enabled, deviceID, version, mode = c, true, "test-device", "v9.9.9-test", "tui"
	return func() {
		client, enabled, deviceID, version, mode = prevClient, prevEnabled, prevDevice, prevVersion, prevMode
	}
}

// newProdClient builds a client from the exact production config
// (posthogClientConfig) pointed at endpoint, so these tests exercise the real
// retry/backpressure settings shipped to users.
func newProdClient(t *testing.T, endpoint string) posthog.Client {
	t.Helper()
	c, err := posthog.NewWithConfig("test-key", posthogClientConfig(endpoint))
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	return c
}

// TestTelemetry_TrackDoesNotBlockWhenBlackholed simulates PostHog being blocked
// such that TCP connects hang forever (packets dropped — the worst case for a
// blocked endpoint, e.g. a DNS blackhole). It fires a burst of Track calls and
// asserts each returns effectively instantly, i.e. the telemetry path never
// blocks the app. Regression guard for issue #47.
func TestTelemetry_TrackDoesNotBlockWhenBlackholed(t *testing.T) {
	// A listener that accepts connections but never responds: this is a
	// network blackhole. Any HTTP request against it hangs until timeout.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn // hold it open, never write
		}
	}()

	c := newProdClient(t, "http://"+ln.Addr().String())
	restore := wireClient(c)
	defer restore()

	// Fire many events; each Track must return promptly.
	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		Track("repro_event", map[string]interface{}{"i": i})
	}
	elapsed := time.Since(start)
	t.Logf("%d Track calls against a blackholed endpoint took %v", n, elapsed)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Track blocked: %d calls took %v (expected near-instant)", n, elapsed)
	}

	// Shutdown must be bounded by ShutdownTimeout, not hang forever.
	done := make(chan struct{})
	shutStart := time.Now()
	go func() {
		c.Close()
		close(done)
	}()
	select {
	case <-done:
		t.Logf("Close() returned in %v against a blackholed endpoint", time.Since(shutStart))
	case <-time.After(6 * time.Second):
		t.Fatalf("Close() hung past ShutdownTimeout against a blackholed endpoint")
	}
}

// TestTelemetry_TrackDoesNotBlockWhenRefused simulates DNS-level blocking that
// fails fast (connection refused / NXDOMAIN): the endpoint points at a closed
// port. Track and Close must still never block the app. Regression guard for
// issue #47.
func TestTelemetry_TrackDoesNotBlockWhenRefused(t *testing.T) {
	// Bind then immediately close to obtain a port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c := newProdClient(t, "http://"+addr)
	restore := wireClient(c)
	defer restore()

	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		Track("repro_event", map[string]interface{}{"i": i})
	}
	elapsed := time.Since(start)
	t.Logf("%d Track calls against a refused endpoint took %v", n, elapsed)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Track blocked: %d calls took %v (expected near-instant)", n, elapsed)
	}

	done := make(chan struct{})
	shutStart := time.Now()
	go func() {
		c.Close()
		close(done)
	}()
	select {
	case <-done:
		t.Logf("Close() returned in %v against a refused endpoint", time.Since(shutStart))
	case <-time.After(6 * time.Second):
		t.Fatalf("Close() hung past ShutdownTimeout against a refused endpoint")
	}
}

// TestTelemetry_BoundedRetriesWhenBlocked verifies the hardened production
// config (posthogClientConfig) does NOT hammer a blocked endpoint: a single
// event whose uploads always fail with a retryable status must produce a small,
// bounded number of HTTP attempts (MaxRetries=1 → 2 attempts), not the SDK
// default of 10. This is the fix for the "shit-ton of posthog endpoints"
// reported in issue #47.
func TestTelemetry_BoundedRetriesWhenBlocked(t *testing.T) {
	var attempts atomic.Int64
	// 503 is retryable, so an unhardened client would keep re-uploading.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Start from the shipped config so MaxRetries is exactly what users get, then
	// override only the batching cadence to force a *live* flush within the test
	// window (retries are short-circuited during Close, so they only manifest
	// while the app runs — the reporter's scenario: a long-lived session with
	// PostHog blocked).
	cfg := posthogClientConfig(srv.URL)
	cfg.BatchSize = 1                                                    // flush on first event
	cfg.Interval = 50 * time.Millisecond                                 // flush promptly, live
	cfg.RetryAfter = func(int) time.Duration { return time.Millisecond } // no real backoff wait
	c, err := posthog.NewWithConfig("test-key", cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	restore := wireClient(c)
	defer restore()

	Track("repro_event", map[string]interface{}{"i": 1})
	time.Sleep(500 * time.Millisecond) // let the live flush exhaust its retries
	c.Close()

	got := attempts.Load()
	t.Logf("single event caused %d HTTP upload attempts (hardened config)", got)
	// MaxRetries=1 means exactly 2 attempts (1 initial + 1 retry), far below the
	// SDK default of 10. Allow a hair of slack for a late interval re-flush.
	if got < 2 || got > 3 {
		t.Fatalf("expected bounded retries (2), got %d attempts", got)
	}
}

// TestPosthogClientConfig_Hardened asserts the shipped config carries the
// anti-hammering settings, documenting them as intentional (issue #47).
func TestPosthogClientConfig_Hardened(t *testing.T) {
	cfg := posthogClientConfig("https://example.test")

	if cfg.MaxRetries == nil || *cfg.MaxRetries != 1 {
		t.Errorf("MaxRetries = %v, want 1", cfg.MaxRetries)
	}
	if cfg.BatchSubmitTimeout >= 0 {
		t.Errorf("BatchSubmitTimeout = %v, want negative (non-blocking)", cfg.BatchSubmitTimeout)
	}
	if cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 3s", cfg.ShutdownTimeout)
	}
	if cfg.BatchUploadTimeout != 5*time.Second {
		t.Errorf("BatchUploadTimeout = %v, want 5s", cfg.BatchUploadTimeout)
	}
}
