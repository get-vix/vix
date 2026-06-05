package llm

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/providers"
)

// orMinimalChunk emits a single chat.completion.chunk with finish_reason
// and usage so the request can complete without us caring about content.
const orMinimalChunk = `{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`

func orHandler(w http.ResponseWriter, r *http.Request) {
	sseHeader(w)
	sseSend(w, "", orMinimalChunk)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// newOpenRouterTestClient builds the generic chat client parameterized the way
// the OpenRouter provider spec does: attribution headers, the usage.include
// body injection (plus any extra jsonSet such as provider routing), and the
// reasoning_effort style.
func newOpenRouterTestClient(t *testing.T, cfg Config, headers map[string]string, extraJSON map[string]any) Client {
	t.Helper()
	jsonSet := map[string]any{"usage.include": true}
	for k, v := range extraJSON {
		jsonSet[k] = v
	}
	c, err := newChatCompletionsClient(cfg, chatParams{
		provider:    ProviderOpenRouter,
		headers:     headers,
		jsonSet:     jsonSet,
		effortStyle: providers.EffortStyleReasoningEffort,
	})
	if err != nil {
		t.Fatalf("newChatCompletionsClient: %v", err)
	}
	return c
}

// TestOpenRouter_AttributionHeaders verifies HTTP-Referer and X-Title are
// sent when configured. OpenRouter uses these for ranking/attribution.
func TestOpenRouter_AttributionHeaders(t *testing.T) {
	srv, log := recordingServer(t, orHandler)

	client := newOpenRouterTestClient(t, Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "anthropic/claude",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, map[string]string{
		"HTTP-Referer": "https://example.test",
		"X-Title":      "vix-test",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	hdr := log.Last(t).Headers
	if got := hdr.Get("HTTP-Referer"); got != "https://example.test" {
		t.Errorf("HTTP-Referer = %q, want %q", got, "https://example.test")
	}
	if got := hdr.Get("X-Title"); got != "vix-test" {
		t.Errorf("X-Title = %q, want %q", got, "vix-test")
	}
}

// TestOpenRouter_UsageIncludeRequested verifies the request body has
// usage.include=true. Without it OpenRouter omits usage.cost from
// responses, which our CostUSD telemetry depends on.
func TestOpenRouter_UsageIncludeRequested(t *testing.T) {
	srv, log := recordingServer(t, orHandler)

	client := newOpenRouterTestClient(t, Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "anthropic/claude",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _ = client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)

	body := log.Last(t).JSONBody(t)
	usage, _ := body["usage"].(map[string]any)
	if usage == nil || usage["include"] != true {
		t.Errorf("expected usage.include=true; got %v", body["usage"])
	}
}

// TestOpenRouter_RoutingBlock verifies a provider routing map injected via
// json_set gets serialized as a `provider` field on the request body.
func TestOpenRouter_RoutingBlock(t *testing.T) {
	srv, log := recordingServer(t, orHandler)

	client := newOpenRouterTestClient(t, Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "anthropic/claude",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, nil, map[string]any{
		"provider": map[string]any{
			"order":           []string{"anthropic"},
			"allow_fallbacks": false,
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _ = client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)

	body := log.Last(t).JSONBody(t)
	provider, _ := body["provider"].(map[string]any)
	if provider == nil {
		t.Fatalf("expected provider routing block in body; got %v", body)
	}
	if provider["allow_fallbacks"] != false {
		t.Errorf("provider.allow_fallbacks = %v, want false", provider["allow_fallbacks"])
	}
	order, _ := provider["order"].([]any)
	if len(order) != 1 || order[0] != "anthropic" {
		t.Errorf("provider.order = %v, want [anthropic]", order)
	}
}

// TestOpenRouter_ReasoningEffortForReasoningModel verifies reasoning_effort
// is sent for reasoning-capable model identifiers and omitted otherwise.
func TestOpenRouter_ReasoningEffortForReasoningModel(t *testing.T) {
	cases := []struct {
		model       string
		effort      string
		wantPresent bool
		wantEffort  string
	}{
		{"openai/o3", "high", true, "high"},
		{"openai/gpt-5-thinking", "medium", true, "medium"},
		{"anthropic/claude-opus-4-8", "high", false, ""}, // not reasoning-capable per the isReasoningOpenAIModel check
		{"openai/gpt-4o", "high", false, ""},
		{"openai/o3", "", false, ""}, // empty effort never sends the field
	}
	for _, c := range cases {
		t.Run(c.model+"_"+c.effort, func(t *testing.T) {
			srv, log := recordingServer(t, orHandler)

			client := newOpenRouterTestClient(t, Config{
				Credential: config.Credential{Value: "test-key"},
				Model:      c.model,
				Effort:     c.effort,
				MaxTokens:  1024,
				BaseURL:    srv.URL,
				StreamIdle: 5 * time.Second,
			}, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _, _ = client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)

			body := log.Last(t).JSONBody(t)
			got, present := body["reasoning_effort"]
			if c.wantPresent && !present {
				t.Errorf("expected reasoning_effort in body, got %v", body)
				return
			}
			if !c.wantPresent && present {
				t.Errorf("expected NO reasoning_effort in body, got %v", got)
				return
			}
			if c.wantPresent && got != c.wantEffort {
				t.Errorf("reasoning_effort = %v, want %q", got, c.wantEffort)
			}
		})
	}
}

// TestOpenRouter_AuthHeaderUsesBearer verifies the API key is sent via the
// Authorization header (Bearer scheme).
func TestOpenRouter_AuthHeaderUsesBearer(t *testing.T) {
	srv, log := recordingServer(t, orHandler)

	client := newOpenRouterTestClient(t, Config{
		Credential: config.Credential{Value: "sk-or-test"},
		Model:      "anthropic/claude",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _ = client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)

	auth := log.Last(t).Headers.Get("Authorization")
	if auth != "Bearer sk-or-test" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer sk-or-test")
	}
}
