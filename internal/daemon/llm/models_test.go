package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

func TestFilterRecentModels(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-24 * time.Hour).Unix()          // well within the window
	old := now.Add(-modelMaxAge - 24*time.Hour).Unix() // just past the window
	models := []ModelListing{
		{Spec: "p/recent", Created: recent},
		{Spec: "p/old", Created: old},
		{Spec: "p/undated", Created: 0}, // unknown publish date — always kept
	}
	got := filterRecentModels(models, now)
	keep := map[string]bool{}
	for _, m := range got {
		keep[m.Spec] = true
	}
	if !keep["p/recent"] || !keep["p/undated"] || keep["p/old"] {
		t.Errorf("filterRecentModels kept the wrong set: %+v", got)
	}
	if len(got) != 2 {
		t.Errorf("got %d models, want 2", len(got))
	}
}

func TestListOpenAICompatibleModels(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"model-old","created":100},
			{"id":"model-new","name":"Shiny New","created":200}
		]}`))
	}))
	defer srv.Close()

	models, err := listOpenAICompatibleModels(context.Background(), srv.URL, "openai", config.Credential{Value: "sk-test"}, nil, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	// Newest first.
	if models[0].Spec != "openai/model-new" || models[1].Spec != "openai/model-old" {
		t.Errorf("order/spec wrong: %+v", models)
	}
	if models[0].DisplayName != "Shiny New" {
		t.Errorf("display name = %q", models[0].DisplayName)
	}
	if models[1].DisplayName != "model-old" {
		t.Errorf("fallback display name = %q", models[1].DisplayName)
	}
}

func TestListOpenAICompatibleModelsFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o","created":2},{"id":"text-embedding-3","created":1}]}`))
	}))
	defer srv.Close()

	models, err := listOpenAICompatibleModels(context.Background(), srv.URL, "openai", config.Credential{Value: "k"}, nil, keepOpenAIChatModel)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 1 || models[0].Spec != "openai/gpt-4o" {
		t.Errorf("filter failed: %+v", models)
	}
}

func TestKeepOpenAIChatModel(t *testing.T) {
	keep := map[string]bool{"gpt-4o": true, "gpt-5.1": true, "o3": true, "o4-mini": true, "chatgpt-4o-latest": true}
	drop := map[string]bool{"text-embedding-3-large": true, "whisper-1": true, "dall-e-3": true, "tts-1": true, "omni-moderation-latest": true, "gpt-4o-audio-preview": true}
	for id := range keep {
		if !keepOpenAIChatModel(id) {
			t.Errorf("expected keep %q", id)
		}
	}
	for id := range drop {
		if keepOpenAIChatModel(id) {
			t.Errorf("expected drop %q", id)
		}
	}
}

func TestListAnthropicModels(t *testing.T) {
	var gotKey, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotBeta = r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-opus-4-7","display_name":"Claude Opus 4.7","created_at":"2025-11-01T00:00:00Z"},
			{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8","created_at":"2026-02-01T00:00:00Z"}
		]}`))
	}))
	defer srv.Close()

	models, err := listAnthropicModels(context.Background(), srv.URL, config.Credential{Value: "sk-ant", Source: config.KeySourceEnv})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotKey != "sk-ant" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotBeta != "" {
		t.Errorf("did not expect anthropic-beta for api-key auth")
	}
	if len(models) != 2 {
		t.Fatalf("got %d", len(models))
	}
	// Newest (4.8) first.
	if models[0].Spec != "anthropic/claude-opus-4-8" || models[0].DisplayName != "Claude Opus 4.8" {
		t.Errorf("first model = %+v", models[0])
	}
	if models[0].Created <= models[1].Created {
		t.Errorf("not sorted newest-first: %d vs %d", models[0].Created, models[1].Created)
	}
}

func TestListAnthropicModelsOAuthHeaders(t *testing.T) {
	var gotAuth, gotBeta, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := listAnthropicModels(context.Background(), srv.URL, config.Credential{Value: "oauth-tok", Source: config.KeySourceOAuthToken})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotAuth != "Bearer oauth-tok" || gotBeta == "" {
		t.Errorf("OAuth headers wrong: auth=%q beta=%q", gotAuth, gotBeta)
	}
	if gotKey != "" {
		t.Errorf("should not send x-api-key for OAuth")
	}
}

func TestListCodexModels(t *testing.T) {
	// Codex has no live model-list endpoint; the catalogue is curated in code.
	models, err := listCodexModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]string{}
	for _, m := range models {
		got[m.Spec] = m.DisplayName
		if m.Provider != "openai-codex" {
			t.Errorf("provider = %q, want openai-codex", m.Provider)
		}
	}
	if got["openai-codex/gpt-5.5"] == "" || got["openai-codex/gpt-5.4"] == "" {
		t.Errorf("codex models = %+v", got)
	}
}

func TestParseUnixOrRFC3339(t *testing.T) {
	if got := parseUnixOrRFC3339("2026-02-01T00:00:00Z"); got == 0 {
		t.Errorf("RFC3339 parse failed")
	}
	if got := parseUnixOrRFC3339("1700000000"); got != 1700000000 {
		t.Errorf("unix string parse = %d", got)
	}
	if got := parseUnixOrRFC3339(""); got != 0 {
		t.Errorf("empty should be 0, got %d", got)
	}
}

func TestParseModelCodexRoute(t *testing.T) {
	prov, model, err := ParseModel("openai-codex/gpt-5.5")
	if err != nil || prov != ProviderCodex || model != "gpt-5.5" {
		t.Errorf("ParseModel codex: prov=%v model=%q err=%v", prov, model, err)
	}
}

func TestProviderUsesOAuth(t *testing.T) {
	if !ProviderAnthropic.UsesOAuth() || !ProviderCodex.UsesOAuth() {
		t.Error("anthropic and codex should use OAuth")
	}
	if ProviderOpenAI.UsesOAuth() || ProviderOpenRouter.UsesOAuth() {
		t.Error("openai/openrouter should not use OAuth")
	}
}
