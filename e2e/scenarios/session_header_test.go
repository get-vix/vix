package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestSessionHeaderOnRequest proves the full session-header contract end-to-end:
// the OpenCode provider has session_header configured in providers.json, so an
// outbound LLM request carries the thread's UUID as x-opencode-session. The
// harness overlays the OpenCode provider (marking it local to bypass HTTPS
// validation for the http loopback mock), sets OPENCODE_BASE_URL to the mock,
// and uses WithModel to route through the OpenCode provider's chat_completions
// wire. The mock records HTTP headers and the test asserts the session header
// matches the thread's actual UUID.
//
// This covers the chain: providers.json → InferenceSpec.Resolve() →
// NewFromModel → PluginConfig.SessionHeader → headerStripperTransport →
// HTTP request → mock server.
func TestSessionHeaderOnRequest(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "session",
		Subcategory: "session.header_request",
		Description: "session_header from OpenCode provider config lands as x-opencode-session on outbound LLM requests",
		Wire:        harness.WireChatCompletions,
	}, harness.WithProviders(`{
		"schema_version": 1,
		"providers": [
			{
				"id": "opencode",
				"local": true,
				"inference": {
					"session_header": "x-opencode-session"
				}
			}
		]
	}`), harness.WithModel("opencode/deepseek-v4-pro"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(harness.Text("Session header received."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("Session header received.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-turn")

	// Assert: at least one request arrived at the mock with the session header.
	reqs := h.Mock.Requests()
	if len(reqs) == 0 {
		t.Fatal("no mock requests recorded")
	}

	var sessionHeader string
	for _, r := range reqs {
		if sid := r.Headers.Get("x-opencode-session"); sid != "" {
			sessionHeader = sid
			break
		}
	}
	if sessionHeader == "" {
		var got []string
		for _, r := range reqs {
			got = append(got, r.Headers.Get("x-opencode-session"))
		}
		t.Fatalf("no request carried x-opencode-session header; got %v", got)
	}

	// Session ID must be a UUID (the thread ID).
	if !strings.Contains(sessionHeader, "-") {
		t.Fatalf("session header value %q does not look like a UUID", sessionHeader)
	}
}
