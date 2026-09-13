package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// --- Session ID context helpers ---

func TestSessionIDContextRoundtrip(t *testing.T) {
	ctx := WithSessionID(context.Background(), "abc-123")
	if got := SessionIDFromContext(ctx); got != "abc-123" {
		t.Errorf("SessionIDFromContext = %q, want %q", got, "abc-123")
	}
}

func TestSessionIDFromContext_Empty(t *testing.T) {
	if got := SessionIDFromContext(context.Background()); got != "" {
		t.Errorf("SessionIDFromContext on bare context = %q, want empty", got)
	}
}

func TestSessionIDFromContext_Overwrite(t *testing.T) {
	ctx := WithSessionID(context.Background(), "first")
	ctx = WithSessionID(ctx, "second")
	if got := SessionIDFromContext(ctx); got != "second" {
		t.Errorf("SessionIDFromContext = %q, want %q", got, "second")
	}
}

// --- headerStripperTransport unit tests ---

// noopTransport is a minimal RoundTripper that records the request it receives.
type noopTransport struct {
	lastReq *http.Request
}

func (t *noopTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.lastReq = req
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, nil
}

func TestHeaderStripperTransport_SessionHeaderInjected(t *testing.T) {
	base := &noopTransport{}
	tr := &headerStripperTransport{
		base:          base,
		sessionHeader: "x-opencode-session",
	}

	req, _ := http.NewRequest("GET", "https://api.example/v1/chat", nil)
	req = req.WithContext(WithSessionID(req.Context(), "thread-uuid-42"))

	_, _ = tr.RoundTrip(req)

	if got := base.lastReq.Header.Get("x-opencode-session"); got != "thread-uuid-42" {
		t.Errorf("session header = %q, want %q", got, "thread-uuid-42")
	}
}

func TestHeaderStripperTransport_NoSessionHeaderKey(t *testing.T) {
	base := &noopTransport{}
	tr := &headerStripperTransport{
		base:          base,
		sessionHeader: "", // no session header configured
	}

	req, _ := http.NewRequest("GET", "https://api.example/v1/chat", nil)
	req = req.WithContext(WithSessionID(req.Context(), "thread-uuid-42"))

	_, _ = tr.RoundTrip(req)

	if got := base.lastReq.Header.Get("x-opencode-session"); got != "" {
		t.Errorf("session header should not be set when key is empty, got %q", got)
	}
}

func TestHeaderStripperTransport_NoSessionIDInContext(t *testing.T) {
	base := &noopTransport{}
	tr := &headerStripperTransport{
		base:          base,
		sessionHeader: "x-opencode-session",
	}

	req, _ := http.NewRequest("GET", "https://api.example/v1/chat", nil)
	// No WithSessionID on context

	_, _ = tr.RoundTrip(req)

	if got := base.lastReq.Header.Get("x-opencode-session"); got != "" {
		t.Errorf("session header should not be set when context has no session ID, got %q", got)
	}
}

func TestHeaderStripperTransport_SetAndStripHeaders(t *testing.T) {
	base := &noopTransport{}
	tr := &headerStripperTransport{
		base:  base,
		set:   map[string]string{"X-Custom": "val1"},
		strip: []string{"Authorization"},
	}

	req, _ := http.NewRequest("GET", "https://api.example/v1/chat", nil)
	req.Header.Set("Authorization", "Bearer old-token")

	_, _ = tr.RoundTrip(req)

	if got := base.lastReq.Header.Get("X-Custom"); got != "val1" {
		t.Errorf("X-Custom = %q, want %q", got, "val1")
	}
	if got := base.lastReq.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be stripped, got %q", got)
	}
}

// --- End-to-end: PluginConfig → NewPluginHTTPClient → real HTTP request ---

// TestSessionHeader_EndToEnd verifies the full path: a PluginConfig with
// SessionHeader set produces an HTTP client that stamps the session ID
// (from context) onto every outbound request. This is the key contract
// between the provider config, the transport, and the thread's session ID.
func TestSessionHeader_EndToEnd(t *testing.T) {
	var gotHeader atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader.Store(r.Header.Get("x-opencode-session"))
		w.WriteHeader(http.StatusOK)
		io.Copy(w, r.Body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	// Build a client with session_header configured — the same way
	// NewFromModel does it after resolving the provider's InferenceSpec.
	client := NewPluginHTTPClient(PluginConfig{
		SessionHeader: "x-opencode-session",
	})

	// Make a request with a session ID in context (the thread UUID).
	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", nil)
	req = req.WithContext(WithSessionID(req.Context(), "sess-abc-123"))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	resp.Body.Close()

	if got := gotHeader.Load().(string); got != "sess-abc-123" {
		t.Errorf("server received session header = %q, want %q", got, "sess-abc-123")
	}
}

// TestSessionHeader_EndToEnd_Empty verifies that when no session ID is in
// the context, the session header is not sent even when configured.
func TestSessionHeader_EndToEnd_Empty(t *testing.T) {
	var gotHeader atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader.Store(r.Header.Get("x-opencode-session"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := NewPluginHTTPClient(PluginConfig{
		SessionHeader: "x-opencode-session",
	})

	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", nil)
	// No WithSessionID — bare context

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	resp.Body.Close()

	if got := gotHeader.Load().(string); got != "" {
		t.Errorf("server received session header = %q, want empty (no session in context)", got)
	}
}

// TestSessionHeader_EndToEnd_PluginHeaders verifies session header injection
// works alongside regular plugin header set/strip.
func TestSessionHeader_EndToEnd_PluginHeaders(t *testing.T) {
	var gotSession atomic.Value
	var gotCustom atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession.Store(r.Header.Get("x-opencode-session"))
		gotCustom.Store(r.Header.Get("X-Plugin"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := NewPluginHTTPClient(PluginConfig{
		SessionHeader: "x-opencode-session",
		Headers: map[string]*string{
			"X-Plugin": strPtr("v2"),
		},
	})

	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", nil)
	req = req.WithContext(WithSessionID(req.Context(), "sess-xyz-789"))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	resp.Body.Close()

	if got := gotSession.Load().(string); got != "sess-xyz-789" {
		t.Errorf("session header = %q, want %q", got, "sess-xyz-789")
	}
	if got := gotCustom.Load().(string); got != "v2" {
		t.Errorf("plugin header = %q, want %q", got, "v2")
	}
}


