package llm

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

const mimoMinimalChunk = `{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`

func mimoHandler(w http.ResponseWriter, r *http.Request) {
	sseHeader(w)
	sseSend(w, "", mimoMinimalChunk)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestMiMo_StreamRoundTrip verifies a basic streaming completion is parsed
// into a neutral *Message with the expected text and end_turn stop reason.
func TestMiMo_StreamRoundTrip(t *testing.T) {
	srv, _ := recordingServer(t, mimoHandler)

	client, err := NewMiMo(Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "mimo-v2.5-pro",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMiMo: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if msg.TextContent != "ok" {
		t.Errorf("TextContent = %q, want %q", msg.TextContent, "ok")
	}
	if msg.StopReason != StopEndTurn {
		t.Errorf("StopReason = %q, want %q", msg.StopReason, StopEndTurn)
	}
}

// TestMiMo_AuthHeaderUsesBearer verifies the API key flows through as
// Authorization: Bearer ... — MiMo's OpenAI-compatible endpoint uses the
// same scheme as OpenAI itself.
func TestMiMo_AuthHeaderUsesBearer(t *testing.T) {
	srv, log := recordingServer(t, mimoHandler)

	client, _ := NewMiMo(Config{
		Credential: config.Credential{Value: "mimo-test-key"},
		Model:      "mimo-v2.5-pro",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _ = client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)

	auth := log.Last(t).Headers.Get("Authorization")
	if auth != "Bearer mimo-test-key" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer mimo-test-key")
	}
}

// TestMiMo_BaseURLOverride verifies Config.BaseURL beats MiMoOptions.BaseURL
// and the built-in default, routing the request to our test server.
func TestMiMo_BaseURLOverride(t *testing.T) {
	srv, log := recordingServer(t, mimoHandler)

	client, _ := NewMiMo(Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "mimo-v2.5-pro",
		MaxTokens:  1024,
		BaseURL:    srv.URL, // top-level override
		StreamIdle: 5 * time.Second,
		MiMo:       MiMoOptions{BaseURL: "https://other.invalid/v1"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v (Config.BaseURL should have routed to the test server)", err)
	}

	if len(log.All()) != 1 {
		t.Fatalf("expected 1 request to the test server, got %d", len(log.All()))
	}
}

// TestMiMo_ProviderAndModel verifies the adapter reports its identity.
func TestMiMo_ProviderAndModel(t *testing.T) {
	client, err := NewMiMo(Config{
		Credential: config.Credential{Value: "k"},
		Model:      "mimo-v2.5-pro",
	})
	if err != nil {
		t.Fatalf("NewMiMo: %v", err)
	}
	if client.Provider() != ProviderMiMo {
		t.Errorf("Provider = %q, want %q", client.Provider(), ProviderMiMo)
	}
	if client.Model() != "mimo-v2.5-pro" {
		t.Errorf("Model = %q, want %q", client.Model(), "mimo-v2.5-pro")
	}
}
