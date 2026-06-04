package llm

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// TestPluginConfig_HeadersAppliedAndStripped exercises the plugin-header
// pipeline end-to-end: a non-nil header value is SET on outgoing requests,
// and a nil value STRIPS the header even when the SDK would normally set
// it. We verify against the Anthropic adapter (which sets x-api-key from
// the credential by default) so the strip path is observable.
func TestPluginConfig_HeadersAppliedAndStripped(t *testing.T) {
	stripVal := (*string)(nil)
	overrideAuth := "Bearer my-oauth-token"

	srv, log := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		sseSend(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		sseSend(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		sseSend(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseSend(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
		sseSend(w, "message_stop", `{"type":"message_stop"}`)
	})

	client, err := NewAnthropic(Config{
		// A non-empty credential normally produces an `x-api-key` header.
		Credential: config.Credential{Value: "sk-ant-test"},
		Model:      "claude-test",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
		PluginCfg: PluginConfig{
			Headers: map[string]*string{
				"Authorization": &overrideAuth, // SET
				"x-api-key":     stripVal,      // STRIP (nil pointer)
				"X-Custom":      strPtr("v1"),  // SET
			},
		},
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	hdr := log.Last(t).Headers
	if got := hdr.Get("Authorization"); got != overrideAuth {
		t.Errorf("Authorization = %q, want %q", got, overrideAuth)
	}
	if got := hdr.Get("X-Custom"); got != "v1" {
		t.Errorf("X-Custom = %q, want %q", got, "v1")
	}
	// Critical: x-api-key MUST be absent even though the credential set it.
	if got := hdr.Get("X-Api-Key"); got != "" {
		t.Errorf("x-api-key should be stripped, got %q", got)
	}
}

// TestPluginConfig_SystemPrefixPrepended verifies the SystemPrefix string
// shows up as the first system block on every outgoing request.
func TestPluginConfig_SystemPrefixPrepended(t *testing.T) {
	srv, log := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		sseSend(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		sseSend(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		sseSend(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseSend(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
		sseSend(w, "message_stop", `{"type":"message_stop"}`)
	})

	client, _ := NewAnthropic(Config{
		Credential: config.Credential{Value: "sk-ant-test"},
		Model:      "claude-test",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
		PluginCfg: PluginConfig{
			SystemPrefix: "custom plugin prefix for testing",
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Call with one explicit system block; the prefix should be prepended.
	if _, _, err := client.StreamMessage(ctx, []SystemBlock{{Text: "you are vix"}}, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	body := log.Last(t).JSONBody(t)
	system, _ := body["system"].([]any)
	if len(system) < 2 {
		t.Fatalf("expected at least 2 system blocks (prefix + caller's), got %v", body["system"])
	}
	first, _ := system[0].(map[string]any)
	if got := first["text"]; !strings.Contains(stringify(got), "plugin prefix") {
		t.Errorf("first system block should be the plugin prefix; got %v", first)
	}
}

func strPtr(s string) *string { return &s }
func stringify(v any) string  { s, _ := v.(string); return s }
