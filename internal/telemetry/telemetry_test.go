package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/zalando/go-keyring"
)

func init() {
	// Use in-memory mock keyring for all tests
	keyring.MockInit()
}

func TestGetOrCreateDeviceID_CreatesNew(t *testing.T) {
	// Clean slate
	keyring.MockInit()

	id, err := GetOrCreateDeviceID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty device ID")
	}
	if len(id) != 36 { // UUID format: 8-4-4-4-12
		t.Fatalf("expected UUID format, got %q", id)
	}
}

func TestGetOrCreateDeviceID_ReadsExisting(t *testing.T) {
	keyring.MockInit()

	// Create first
	id1, err := GetOrCreateDeviceID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read again
	id2, err := GetOrCreateDeviceID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected same device ID, got %q and %q", id1, id2)
	}
}

func TestOptOut_EnvVar(t *testing.T) {
	tests := []struct {
		value    string
		expected bool
	}{
		{"off", true},
		{"false", true},
		{"0", true},
		{"OFF", true},
		{"False", true},
		{"", false},
		{"on", false},
		{"1", false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			os.Setenv("VIX_TELEMETRY", tt.value)
			defer os.Unsetenv("VIX_TELEMETRY")

			got := isOptedOut()
			if got != tt.expected {
				t.Errorf("VIX_TELEMETRY=%q: isOptedOut()=%v, want %v", tt.value, got, tt.expected)
			}
		})
	}
}

func TestHashWorkflowName(t *testing.T) {
	h1 := HashWorkflowName("Plan Workflow")
	h2 := HashWorkflowName("Plan Workflow")
	h3 := HashWorkflowName("Review Workflow")

	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %q and %q", h1, h2)
	}
	if h1 == h3 {
		t.Error("expected different hashes for different names")
	}
	if len(h1) != 12 {
		t.Errorf("expected 12 hex chars, got %d: %q", len(h1), h1)
	}
}

func TestTrack_NoopWhenDisabled(t *testing.T) {
	// enabled is false by default (no Init called)
	enabled = false
	// Should not panic
	Track("test_event", map[string]interface{}{"key": "value"})
}

func TestCaptureException_NoopWhenDisabled(t *testing.T) {
	// enabled is false by default (no Init called); client is nil, so this
	// must short-circuit without dereferencing it.
	enabled = false
	// Should not panic
	CaptureException("panic", "boom", nil)
}

func TestTrackPanic_NoopWhenDisabled(t *testing.T) {
	enabled = false
	// Should not panic even with a nil client.
	TrackPanic("test.site", "boom", []byte("goroutine stack"))
}

// TestCaptureException_SendsStructuredPayload exercises the full enqueue→flush
// path against a local HTTP server, asserting the $exception event carries the
// structured $exception_list plus the build/runtime context and raw go_stack in
// its properties (the PostHog "Properties" tab).
func TestCaptureException_SendsStructuredPayload(t *testing.T) {
	keyring.MockInit()

	var mu sync.Mutex
	var batchBodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/batch") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			batchBodies = append(batchBodies, b)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":1}`))
	}))
	defer srv.Close()

	c, err := posthog.NewWithConfig("test-key", posthog.Config{
		Endpoint:        srv.URL,
		BatchSize:       1,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}

	// Wire the package globals as if Init() had succeeded, restoring afterwards.
	prevClient, prevEnabled, prevDevice, prevVersion, prevMode := client, enabled, deviceID, version, mode
	client, enabled, deviceID, version, mode = c, true, "test-device", "v9.9.9-test", "tui"
	defer func() {
		client, enabled, deviceID, version, mode = prevClient, prevEnabled, prevDevice, prevVersion, prevMode
	}()

	TrackPanic("test.site", "kaboom", []byte("goroutine 1 [running]:\nmain.boom()"))
	c.Close() // flush synchronously

	mu.Lock()
	bodies := batchBodies
	mu.Unlock()

	props := findExceptionProps(t, bodies)

	for _, k := range []string{"version", "os", "arch", "mode", "go_stack", "$exception_list"} {
		if _, ok := props[k]; !ok {
			t.Errorf("exception properties missing %q (got %v)", k, props)
		}
	}
	if props["version"] != "v9.9.9-test" {
		t.Errorf("version = %v, want v9.9.9-test", props["version"])
	}
	if props["mode"] != "tui" {
		t.Errorf("mode = %v, want tui", props["mode"])
	}
	if gs, _ := props["go_stack"].(string); !strings.Contains(gs, "main.boom()") {
		t.Errorf("go_stack missing raw stack, got %q", gs)
	}

	list, _ := props["$exception_list"].([]interface{})
	if len(list) == 0 {
		t.Fatalf("$exception_list empty: %v", props["$exception_list"])
	}
	item, _ := list[0].(map[string]interface{})
	if item["type"] != "panic" {
		t.Errorf("exception type = %v, want panic", item["type"])
	}
	if val, _ := item["value"].(string); !strings.Contains(val, "test.site: kaboom") {
		t.Errorf("exception value = %q, want to contain 'test.site: kaboom'", val)
	}
	if _, ok := item["stacktrace"]; !ok {
		t.Errorf("exception item missing structured stacktrace: %v", item)
	}
}

// findExceptionProps locates the first "$exception" event across the captured
// /batch/ request bodies and returns its properties map.
func findExceptionProps(t *testing.T, bodies [][]byte) map[string]interface{} {
	t.Helper()
	for _, b := range bodies {
		var env struct {
			Batch []struct {
				Event      string                 `json:"event"`
				Properties map[string]interface{} `json:"properties"`
			} `json:"batch"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			continue
		}
		for _, m := range env.Batch {
			if m.Event == "$exception" {
				return m.Properties
			}
		}
	}
	t.Fatalf("no $exception event found in %d batch body(ies)", len(bodies))
	return nil
}
