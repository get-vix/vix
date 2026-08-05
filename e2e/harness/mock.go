package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// replyKind discriminates a scripted model turn.
type replyKind int

const (
	replyText replyKind = iota
	replyToolUse
	replyError
)

// Reply is one scripted model turn handed to the mock. Construct with Text,
// ToolUse, TextChunks, Thinking, or HTTPError — these are plain value builders,
// not a chained DSL. The single modifier WithUsage attaches a usage override.
type Reply struct {
	kind     replyKind
	text     string
	chunks   []string // when non-empty, text is streamed as these deltas in order
	thinking string   // when non-empty, a thinking/reasoning block precedes the answer
	toolName string
	toolArgs string // raw JSON object string for the tool input

	// inTokens/outTokens override the usage reported for this turn (0 = renderer
	// default). inTokens is the prompt size the daemon records as lastInputTokens,
	// so a large value drives auto-compaction.
	inTokens  int64
	outTokens int64

	// errStatus/errMsg describe an HTTP-level error response (kind=replyError).
	errStatus int
	errMsg    string
}

// Text scripts a final assistant text turn (stop_reason=end_turn).
func Text(s string) Reply { return Reply{kind: replyText, text: s} }

// TextChunks scripts a final assistant text turn streamed as multiple deltas, so
// scenarios can observe incremental rendering. The full text is the chunks
// concatenated.
func TextChunks(chunks ...string) Reply {
	return Reply{kind: replyText, text: strings.Join(chunks, ""), chunks: chunks}
}

// Thinking scripts a turn that emits a thinking/reasoning block (thought) before
// the visible answer (text). On the Messages wire this is a `thinking` content
// block; on Responses it's a reasoning summary. Chat Completions has no thinking
// channel, so only the answer renders there.
func Thinking(thought, answer string) Reply {
	return Reply{kind: replyText, thinking: thought, text: answer}
}

// ToolUse scripts a single tool call (stop_reason=tool_use). argsJSON must be a
// JSON object string, e.g. `{"path":"hello.txt","content":"hi"}`.
func ToolUse(name, argsJSON string) Reply {
	return Reply{kind: replyToolUse, toolName: name, toolArgs: argsJSON}
}

// HTTPError scripts a non-200 provider response carrying message, rendered in
// the active wire's error envelope. The status drives vix's error classifier:
// 429/500/502/503/529 are retryable (the next queued reply serves the retry),
// while 400/401/403/404 fail fast. Use it for error-rendering and retry
// scenarios.
func HTTPError(status int, message string) Reply {
	return Reply{kind: replyError, errStatus: status, errMsg: message}
}

// WithUsage overrides the usage reported for this turn. inputTokens is the
// prompt size the daemon records as lastInputTokens — set it above
// threshold×context-window to trigger auto-compaction.
func (r Reply) WithUsage(inputTokens, outputTokens int64) Reply {
	r.inTokens, r.outTokens = inputTokens, outputTokens
	return r
}

// writeJSONError writes a non-200 response with a JSON body, so the provider SDK
// surfaces a typed API error that vix's classifyError can categorise by status.
func writeJSONError(w http.ResponseWriter, status int, body any) {
	payload, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// RequestView is a wire-agnostic projection of one inbound LLM request, so
// scenario assertions read the same regardless of provider dialect.
type RequestView struct {
	Wire string         // which wire dialect served this request
	Raw  map[string]any // decoded request body, for arbitrary inspection
	body []byte
}

// LastUserText returns the text of the most recent user message (best-effort,
// per wire dialect).
func (v RequestView) LastUserText() string {
	switch v.Wire {
	case wireChatCompletions:
		return chatLastUserText(v.Raw)
	case wireResponses:
		return respLastUserText(v.Raw)
	default:
		return lastUserText(v.Raw)
	}
}

// LastToolResult returns the content of the most recent tool result fed back to
// the model (best-effort, per wire dialect).
func (v RequestView) LastToolResult() string {
	switch v.Wire {
	case wireChatCompletions:
		return chatLastToolResult(v.Raw)
	case wireResponses:
		return respLastToolResult(v.Raw)
	default:
		return lastToolResult(v.Raw)
	}
}

// Body returns the raw request bytes.
func (v RequestView) Body() []byte { return v.body }

// Mock is an in-process HTTP server that impersonates the LLM provider. It
// routes on request path to the matching wire renderer and serves scripted
// replies via a parking rendezvous: a handler blocks (holding the connection
// open) until a reply is available, either pre-loaded with Enqueue or supplied
// live with Reply after inspecting the request via Next.
type Mock struct {
	srv *httptest.Server

	mu      sync.Mutex
	queue   []Reply // pre-loaded replies (blind scripting)
	history []RequestView
	local   *localModel // advertised local-provider model (discovery probes)

	requests chan RequestView // non-blocking offer to Next()
	replies  chan Reply       // live replies from Reply()
	quit     chan struct{}    // closed by close(); unblocks parked handlers
}

// newMock starts the mock server on loopback.
func newMock() *Mock {
	m := &Mock{
		requests: make(chan RequestView, 64),
		replies:  make(chan Reply),
		quit:     make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", m.handle(wireMessages))
	mux.HandleFunc("/responses", m.handle(wireResponses))
	mux.HandleFunc("/v1/responses", m.handle(wireResponses))
	mux.HandleFunc("/chat/completions", m.handle(wireChatCompletions))
	mux.HandleFunc("/v1/chat/completions", m.handle(wireChatCompletions))
	// Local-provider discovery (llama.cpp / Ollama).
	mux.HandleFunc("/v1/models", m.handleOpenAIModels)
	mux.HandleFunc("/props", m.handleLlamaProps)
	mux.HandleFunc("/api/ps", m.handleOllamaPS)
	mux.HandleFunc("/api/show", m.handleOllamaShow)
	m.srv = httptest.NewServer(mux)
	return m
}

// BaseURL is the loopback URL vix should target (no trailing /v1).
func (m *Mock) BaseURL() string { return m.srv.URL }

// close shuts the server down. It first unblocks any handler parked in
// nextReply (e.g. an unexpected async request like auto-title generation with an
// empty queue) so srv.Close() doesn't hang waiting on an outstanding request.
func (m *Mock) close() {
	close(m.quit)
	m.srv.Close()
}

// Enqueue pre-loads replies served in order with no test involvement — use for
// turns you don't need to inspect.
func (m *Mock) Enqueue(replies ...Reply) {
	m.mu.Lock()
	m.queue = append(m.queue, replies...)
	m.mu.Unlock()
}

// Next blocks until the next request arrives and returns its wire-agnostic
// view. Pair with Reply to act as the model for that exact turn. The bound is
// the caller's context deadline, surfaced by the harness as a timeout dump.
func (m *Mock) Next() RequestView { return <-m.requests }

// Reply supplies the response for a handler currently parked waiting (i.e. one
// not satisfied by the Enqueue queue). It blocks until a handler takes it.
func (m *Mock) Reply(r Reply) { m.replies <- r }

// Requests returns a snapshot of every request seen so far (full history,
// independent of the Next channel).
func (m *Mock) Requests() []RequestView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RequestView, len(m.history))
	copy(out, m.history)
	return out
}

func (m *Mock) handle(wire string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := readAll(r)
		view := RequestView{Wire: wire, body: body}
		_ = json.Unmarshal(body, &view.Raw)

		m.mu.Lock()
		m.history = append(m.history, view)
		m.mu.Unlock()

		// Offer to Next() without ever blocking the handler.
		select {
		case m.requests <- view:
		default:
		}

		reply := m.nextReply()
		writeSSE(w, wire, reply)
	}
}

// nextReply pops a pre-loaded reply or parks until Reply() provides one (or the
// mock is closed, in which case it returns a benign empty reply so the handler
// can finish and the server can shut down).
func (m *Mock) nextReply() Reply {
	m.mu.Lock()
	if len(m.queue) > 0 {
		r := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()
		return r
	}
	m.mu.Unlock()
	select {
	case r := <-m.replies:
		return r
	case <-m.quit:
		return Reply{kind: replyText, text: ""}
	}
}

// writeSSE dispatches to the per-wire renderer.
func writeSSE(w http.ResponseWriter, wire string, r Reply) {
	switch wire {
	case wireMessages:
		writeMessagesSSE(w, r)
	case wireChatCompletions:
		writeChatCompletionsSSE(w, r)
	case wireResponses:
		writeResponsesSSE(w, r)
	default:
		http.Error(w, fmt.Sprintf("e2e mock: unknown wire %q", wire), http.StatusNotImplemented)
	}
}

const (
	wireMessages        = "messages"
	wireResponses       = "responses"
	wireChatCompletions = "chat_completions"
)
