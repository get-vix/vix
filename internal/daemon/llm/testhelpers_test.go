package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/providers"
)

// newChatTestClient builds the generic Chat Completions client pointed at a
// test server, with the reasoning_effort style (the common case for the shared
// wire/stream tests). Provider identity is OpenRouter for logging only.
func newChatTestClient(t *testing.T, baseURL, model, effort string, idle time.Duration) Client {
	t.Helper()
	c, err := newChatCompletionsClient(Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      model,
		Effort:     effort,
		MaxTokens:  1024,
		BaseURL:    baseURL,
		StreamIdle: idle,
	}, chatParams{
		provider:    ProviderOpenRouter,
		effortStyle: providers.EffortStyleReasoningEffort,
	})
	if err != nil {
		t.Fatalf("newChatCompletionsClient: %v", err)
	}
	return c
}

// recordedRequest is one captured outbound HTTP request from a test.
type recordedRequest struct {
	URL     string
	Headers http.Header
	Body    []byte
}

// JSONBody returns the request body parsed as a JSON object (any other
// shape produces a t.Fatal). Useful for asserting on field presence and
// values without writing per-test boilerplate.
func (r recordedRequest) JSONBody(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("request body not JSON: %v\nbody=%s", err, r.Body)
	}
	return out
}

// requestLog is the thread-safe accumulator a recordingServer writes into.
type requestLog struct {
	mu       sync.Mutex
	requests []recordedRequest
}

// All returns a snapshot of every captured request, in arrival order.
func (l *requestLog) All() []recordedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]recordedRequest, len(l.requests))
	copy(out, l.requests)
	return out
}

// Last returns the most recent captured request, or fails the test when
// none has been received.
func (l *requestLog) Last(t *testing.T) recordedRequest {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.requests) == 0 {
		t.Fatalf("no requests recorded")
	}
	return l.requests[len(l.requests)-1]
}

// recordingServer wraps httptest.NewServer with a handler that captures
// every inbound request (URL, headers, body) into the returned requestLog
// before delegating to respond. respond is responsible for writing the
// response body — the recorder doesn't write anything itself.
func recordingServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		log.mu.Lock()
		log.requests = append(log.requests, recordedRequest{
			URL:     r.URL.String(),
			Headers: r.Header.Clone(),
			Body:    body,
		})
		log.mu.Unlock()
		// Re-attach the body so the handler can re-read if it wants to.
		r.Body = io.NopCloser(newBytesReader(body))
		respond(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

// sseSend writes one Server-Sent-Event frame to w and flushes if possible.
// data should be a single line (no embedded newlines) per the SSE spec —
// callers must compact their JSON.
func sseSend(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseHeader writes the standard text/event-stream prelude.
func sseHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
}

// --- internal: tiny bytes.Reader replacement so we don't depend on bytes ---

type bytesReader struct {
	b []byte
	i int
}

func newBytesReader(b []byte) *bytesReader { return &bytesReader{b: b} }
func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
