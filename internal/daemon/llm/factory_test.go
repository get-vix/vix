package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/providers"
)

func TestParseModel(t *testing.T) {
	cases := []struct {
		spec      string
		wantProv  ProviderID
		wantModel string
		wantErr   bool
	}{
		{"anthropic/claude-opus-4-8", ProviderAnthropic, "claude-opus-4-8", false},
		{"openai/gpt-5.1", ProviderOpenAI, "gpt-5.1", false},
		{"openrouter/openai/gpt-5.1", ProviderOpenRouter, "openai/gpt-5.1", false},
		{"minimax/MiniMax-M2.7", ProviderMiniMax, "MiniMax-M2.7", false},
		{"mimo/mimo-v2.5-pro", ProviderMiMo, "mimo-v2.5-pro", false},
		{"", "", "", true},
		{"claude-sonnet-4-6", "", "", true}, // bare name, no prefix
		{"gemini/pro", "", "", true},        // unknown prefix
	}
	for _, c := range cases {
		prov, model, err := ParseModel(c.spec)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseModel(%q): expected error, got (%q, %q)", c.spec, prov, model)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseModel(%q): unexpected error %v", c.spec, err)
			continue
		}
		if prov != c.wantProv || model != c.wantModel {
			t.Errorf("ParseModel(%q) = (%q, %q), want (%q, %q)", c.spec, prov, model, c.wantProv, c.wantModel)
		}
	}
}

func TestDefaultEffortFromSpec(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		{"anthropic/claude-opus-4-8", "adaptive"},
		{"minimax/MiniMax-M2.7", "adaptive"},
		{"openai/gpt-5.1", "medium"}, // reasoning-capable
		{"openai/gpt-4o", ""},        // not reasoning
		{"openrouter/openai/o3", "medium"},
		{"mimo/mimo-v2-flash", ""},
		{"bogus", ""}, // parse error → empty
	}
	for _, c := range cases {
		if got := DefaultEffortFromSpec(c.spec); got != c.want {
			t.Errorf("DefaultEffortFromSpec(%q) = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestOpenAIAuthOptions_ExtraHeaders(t *testing.T) {
	// A credential with extra headers (e.g. the Codex backend's
	// chatgpt-account-id) yields one WithAPIKey option plus one per header.
	cred := config.Credential{Value: "tok", ExtraHeaders: map[string]string{"chatgpt-account-id": "acct-123"}}
	opts := openaiAuthOptions(cred)
	if len(opts) != 2 {
		t.Errorf("expected 2 options (api key + 1 header), got %d", len(opts))
	}
}

// TestNewFromModel_LocalProvidersNeedNoCredential asserts the keyless local
// providers construct a chat-completions client without ErrNoCredential when
// no API key is configured anywhere.
func TestNewFromModel_LocalProvidersNeedNoCredential(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	t.Setenv("LLAMACPP_API_KEY", "")
	cases := []struct {
		spec     string
		provider ProviderID
		model    string
	}{
		{"ollama/qwen3:8b", "ollama", "qwen3:8b"},
		{"llamacpp/qwen2.5-coder-7b", "llamacpp", "qwen2.5-coder-7b"},
	}
	for _, c := range cases {
		client, err := NewFromModel(c.spec, nil, "", 0)
		if err != nil {
			t.Errorf("NewFromModel(%q): unexpected error %v", c.spec, err)
			continue
		}
		if client.Provider() != c.provider || client.Model() != c.model {
			t.Errorf("NewFromModel(%q) = (%q, %q), want (%q, %q)",
				c.spec, client.Provider(), client.Model(), c.provider, c.model)
		}
	}
}

// TestBuildResponses_UsesInferenceBaseURL is the regression for OpenCode
// responses-wire models (grok-4.6, gpt-5.6-luna, …): when Config.BaseURL is
// empty, buildResponses must honor inf.BaseURL instead of the OpenAI SDK
// default (api.openai.com).
func TestBuildResponses_UsesInferenceBaseURL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sseHeader(w)
		sseSend(w, "response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"grok-4.6","output":[{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`)
	}))
	defer srv.Close()

	client, err := buildResponses(providers.ProviderSpec{ID: "opencode"}, providers.InferenceSpec{
		BaseURL: srv.URL,
	}, Config{
		Credential: config.Credential{Value: "opencode-key"},
		Model:      "grok-4.6",
		MaxTokens:  128,
		StreamIdle: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("buildResponses: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v (inf.BaseURL should have routed to the test server)", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 request to inf.BaseURL, got %d", hits.Load())
	}
}

// TestBuildResponses_CredBaseURLWins verifies an explicit Config.BaseURL
// (credential endpoint override) is not overwritten by inf.BaseURL.
func TestBuildResponses_CredBaseURLWins(t *testing.T) {
	var infHits, credHits atomic.Int32
	infSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		infHits.Add(1)
	}))
	defer infSrv.Close()
	credSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credHits.Add(1)
		sseHeader(w)
		sseSend(w, "response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"o3","output":[{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`)
	}))
	defer credSrv.Close()

	client, err := buildResponses(providers.ProviderSpec{ID: "openai"}, providers.InferenceSpec{
		BaseURL: infSrv.URL,
	}, Config{
		Credential: config.Credential{Value: "key"},
		Model:      "o3",
		MaxTokens:  128,
		BaseURL:    credSrv.URL,
		StreamIdle: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("buildResponses: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if credHits.Load() != 1 {
		t.Fatalf("expected 1 request to cred BaseURL, got %d", credHits.Load())
	}
	if infHits.Load() != 0 {
		t.Fatalf("inf.BaseURL should not have been hit, got %d", infHits.Load())
	}
}
