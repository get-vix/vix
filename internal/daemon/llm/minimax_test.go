package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/providers"
)

const mmMinimalChunk = `{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`

func mmHandler(w http.ResponseWriter, r *http.Request) {
	sseHeader(w)
	sseSend(w, "", mmMinimalChunk)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// newMiniMaxTestClient builds the generic chat client parameterized the way the
// MiniMax provider spec does: a GroupId query param (when non-empty) and the
// reasoning_split effort style.
func newMiniMaxTestClient(t *testing.T, cfg Config, groupID string) Client {
	t.Helper()
	var qp map[string]string
	if groupID != "" {
		qp = map[string]string{"GroupId": groupID}
	}
	c, err := newChatCompletionsClient(cfg, chatParams{
		provider:    ProviderMiniMax,
		queryParams: qp,
		effortStyle: providers.EffortStyleReasoningSplit,
	})
	if err != nil {
		t.Fatalf("newChatCompletionsClient: %v", err)
	}
	return c
}

// TestMiniMax_GroupIDOnEveryRequest verifies that the configured GroupID
// shows up as ?GroupId=<id> on every outbound URL. Critical: some MiniMax
// workspaces fail every request without this query param.
func TestMiniMax_GroupIDOnEveryRequest(t *testing.T) {
	srv, log := recordingServer(t, mmHandler)

	client := newMiniMaxTestClient(t, Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "MiniMax-M2.7",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, "grp_abc")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Two calls, both should carry the GroupId.
	for i := 0; i < 2; i++ {
		if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	all := log.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(all))
	}
	for i, req := range all {
		if !strings.Contains(req.URL, "GroupId=grp_abc") {
			t.Errorf("request %d URL %q missing GroupId=grp_abc", i, req.URL)
		}
	}
}

// TestMiniMax_GroupIDMissing_NoQueryParam verifies an empty GroupID still
// builds a working client (no panic) and produces no GroupId query param.
func TestMiniMax_GroupIDMissing_NoQueryParam(t *testing.T) {
	srv, log := recordingServer(t, mmHandler)

	client := newMiniMaxTestClient(t, Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "MiniMax-M2.7",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	url := log.Last(t).URL
	if strings.Contains(url, "GroupId=") {
		t.Errorf("expected NO GroupId query param when GroupID is empty; got URL %q", url)
	}
}

// TestMiniMax_ReasoningSplitWhenEffortSet verifies that any non-empty
// effort triggers reasoning_split=true on the request body. MiniMax M2 has
// no level knob — it's binary.
func TestMiniMax_ReasoningSplitWhenEffortSet(t *testing.T) {
	cases := []struct {
		name        string
		effort      string
		wantPresent bool
	}{
		{"empty_effort", "", false},
		{"adaptive", "adaptive", true},
		{"high", "high", true},
		{"max", "max", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, log := recordingServer(t, mmHandler)

			client := newMiniMaxTestClient(t, Config{
				Credential: config.Credential{Value: "test-key"},
				Model:      "MiniMax-M2.7",
				Effort:     c.effort,
				MaxTokens:  1024,
				BaseURL:    srv.URL,
				StreamIdle: 5 * time.Second,
			}, "grp")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _, _ = client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)

			body := log.Last(t).JSONBody(t)
			got, present := body["reasoning_split"]
			if c.wantPresent && (!present || got != true) {
				t.Errorf("expected reasoning_split=true in body, got %v (present=%v)", got, present)
			}
			if !c.wantPresent && present {
				t.Errorf("expected NO reasoning_split in body, got %v", got)
			}
		})
	}
}

// TestMiniMax_BaseURLRouting verifies Config.BaseURL routes the request to our
// test server (the region/base-url resolution now lives in providers.json env
// interpolation; the client just honors the resolved base URL).
func TestMiniMax_BaseURLRouting(t *testing.T) {
	srv, log := recordingServer(t, mmHandler)

	client := newMiniMaxTestClient(t, Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "MiniMax-M2.7",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, "grp_x")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v (Config.BaseURL should have routed to the test server)", err)
	}

	if len(log.All()) != 1 {
		t.Fatalf("expected 1 request to the test server, got %d", len(log.All()))
	}
}

// TestMiniMax_AuthHeaderUsesBearer verifies the API key flows through as
// Authorization: Bearer ... — MiniMax's OpenAI-compatible endpoint uses
// the same scheme as OpenAI itself.
func TestMiniMax_AuthHeaderUsesBearer(t *testing.T) {
	srv, log := recordingServer(t, mmHandler)

	client := newMiniMaxTestClient(t, Config{
		Credential: config.Credential{Value: "mm-test-key"},
		Model:      "MiniMax-M2.7",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, "grp")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _ = client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)

	auth := log.Last(t).Headers.Get("Authorization")
	if auth != "Bearer mm-test-key" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer mm-test-key")
	}
}
