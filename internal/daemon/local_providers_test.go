package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/get-vix/vix/internal/providers"
)

func localSpec(id string) providers.ProviderSpec {
	return providers.ProviderSpec{
		ID:          id,
		ModelPrefix: id,
		WireFormat:  providers.WireChatCompletions,
		Local:       true,
	}
}

// TestProbeLocalProvider_Ollama covers the full Ollama probe: /v1/models for
// the list, /api/ps for loaded markers, /api/show for context windows.
func TestProbeLocalProvider_Ollama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "qwen3:8b"}, {"id": "llama3.2:3b"}},
			})
		case "/api/ps":
			json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"name": "qwen3:8b"}},
			})
		case "/api/show":
			var req struct {
				Model string `json:"model"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			ctx := map[string]any{"qwen3.context_length": 40960}
			if req.Model == "llama3.2:3b" {
				ctx = map[string]any{"llama.context_length": 131072}
			}
			json.NewEncoder(w).Encode(map[string]any{"model_info": ctx})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := probeLocalProvider(context.Background(), localSpec("ollama"), srv.URL+"/v1")
	if !st.Reachable {
		t.Fatal("expected reachable")
	}
	if len(st.Models) != 2 {
		t.Fatalf("models = %+v, want 2", st.Models)
	}
	// Sorted by id: llama3.2:3b before qwen3:8b.
	if st.Models[0].Spec != "ollama/llama3.2:3b" || st.Models[1].Spec != "ollama/qwen3:8b" {
		t.Errorf("specs = %q, %q", st.Models[0].Spec, st.Models[1].Spec)
	}
	if !st.Models[1].Loaded || st.Models[0].Loaded {
		t.Errorf("loaded markers wrong: %+v", st.Models)
	}
	if st.Models[0].ContextWindow != 131072 || st.Models[1].ContextWindow != 40960 {
		t.Errorf("context windows = %d, %d", st.Models[0].ContextWindow, st.Models[1].ContextWindow)
	}
}

// TestProbeLocalProvider_LlamaCpp covers the llama.cpp probe: single model
// from /v1/models, serving context from /props.
func TestProbeLocalProvider_LlamaCpp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "qwen2.5-coder-7b-instruct-q4_k_m.gguf"}},
			})
		case "/props":
			json.NewEncoder(w).Encode(map[string]any{
				"default_generation_settings": map[string]any{"n_ctx": 8192},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := probeLocalProvider(context.Background(), localSpec("llamacpp"), srv.URL+"/v1")
	if !st.Reachable {
		t.Fatal("expected reachable")
	}
	if len(st.Models) != 1 {
		t.Fatalf("models = %+v, want 1", st.Models)
	}
	if st.Models[0].Spec != "llamacpp/qwen2.5-coder-7b-instruct-q4_k_m.gguf" {
		t.Errorf("spec = %q", st.Models[0].Spec)
	}
	if st.Models[0].ContextWindow != 8192 {
		t.Errorf("context window = %d, want 8192", st.Models[0].ContextWindow)
	}
}

// TestProbeLocalProvider_Unreachable asserts a dead server yields
// reachable=false and no models — never an error surfaced to the caller.
func TestProbeLocalProvider_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // dead immediately

	st := probeLocalProvider(context.Background(), localSpec("ollama"), srv.URL+"/v1")
	if st.Reachable {
		t.Error("expected unreachable")
	}
	if len(st.Models) != 0 {
		t.Errorf("models = %+v, want none", st.Models)
	}
}

// TestProbeLocalProvider_EnrichFailureIsCosmetic asserts a server whose native
// endpoints 404 (e.g. llama-swap proxying only the OpenAI surface) still
// yields the model list.
func TestProbeLocalProvider_EnrichFailureIsCosmetic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "m1"}, {"id": "m2"}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	st := probeLocalProvider(context.Background(), localSpec("llamacpp"), srv.URL+"/v1")
	if !st.Reachable || len(st.Models) != 2 {
		t.Fatalf("state = %+v, want reachable with 2 models", st)
	}
	if st.Models[0].ContextWindow != 0 {
		t.Errorf("context window = %d, want 0 (unknown)", st.Models[0].ContextWindow)
	}
}
