package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/providers"
)

// Local-provider discovery: Ollama and llama.cpp expose OpenAI-compatible
// /models endpoints, so the daemon probes each local provider's server live
// instead of relying on the static providers.json catalogue. The single RPC
// (providers.local_status) doubles as the reachability check the TUI renders
// as a dot next to the provider name.

// LocalModel is one live-discovered model on a local provider's server.
type LocalModel struct {
	Spec        string `json:"spec"`         // prefixed, e.g. "ollama/qwen3:8b"
	DisplayName string `json:"display_name"` // bare server-side id
	// ContextWindow is the serving context in tokens (Ollama: the model's
	// trained context_length from /api/show; llama.cpp: n_ctx from /props).
	// 0 means unknown.
	ContextWindow int64 `json:"context_window,omitempty"`
	// Loaded marks a model currently held in memory (Ollama /api/ps).
	Loaded bool `json:"loaded,omitempty"`
}

// LocalProviderState is the probe result for one local provider.
type LocalProviderState struct {
	Provider  string       `json:"provider"`
	BaseURL   string       `json:"base_url"`
	Reachable bool         `json:"reachable"`
	Models    []LocalModel `json:"models"`
}

const (
	// localProbeTimeout bounds one HTTP request against a local server. Local
	// sockets answer in milliseconds; anything slower is effectively down.
	localProbeTimeout = 1500 * time.Millisecond
	// localEnrichTimeout bounds the optional metadata calls (/api/ps,
	// /api/show, /props) as a whole.
	localEnrichTimeout = 2 * time.Second
	// localStatusTTL is how long a probe result is served from cache, so
	// cursor movement in the TUI doesn't hammer the local server.
	localStatusTTL = 5 * time.Second
	// localShowLimit caps the per-model /api/show calls per probe.
	localShowLimit = 32
)

var localHTTP = &http.Client{Timeout: localProbeTimeout}

type localStatusCache struct {
	mu      sync.Mutex
	entries map[string]localCacheEntry
}

type localCacheEntry struct {
	state LocalProviderState
	at    time.Time
}

var localCache = &localStatusCache{entries: map[string]localCacheEntry{}}

func (c *localStatusCache) get(key string) (LocalProviderState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > localStatusTTL {
		return LocalProviderState{}, false
	}
	return e.state, true
}

func (c *localStatusCache) put(key string, st LocalProviderState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = localCacheEntry{state: st, at: time.Now()}
}

// localProviderStates probes every local provider in the registry,
// concurrently, serving fresh-enough results from cache.
func localProviderStates(ctx context.Context) []LocalProviderState {
	var locals []providers.ProviderSpec
	for _, p := range providers.Default().All() {
		if p.Local {
			locals = append(locals, p)
		}
	}
	out := make([]LocalProviderState, len(locals))
	var wg sync.WaitGroup
	for i, p := range locals {
		baseURL := strings.TrimRight(p.Inference.Resolve().BaseURL, "/")
		key := p.ID + "|" + baseURL
		if st, ok := localCache.get(key); ok {
			out[i] = st
			continue
		}
		wg.Add(1)
		go func(i int, p providers.ProviderSpec, baseURL, key string) {
			defer wg.Done()
			st := probeLocalProvider(ctx, p, baseURL)
			localCache.put(key, st)
			out[i] = st
		}(i, p, baseURL, key)
	}
	wg.Wait()
	return out
}

// probeLocalProvider checks one local provider's server: GET {base}/models for
// reachability + the model list, then best-effort metadata enrichment.
func probeLocalProvider(ctx context.Context, p providers.ProviderSpec, baseURL string) LocalProviderState {
	st := LocalProviderState{Provider: p.ID, BaseURL: baseURL}

	cred := config.ResolveProviderCredential(p.ID)
	ids, err := fetchOpenAIModelIDs(ctx, baseURL+"/models", cred.Value)
	if err != nil {
		return st // unreachable (or non-conforming): Reachable stays false
	}
	st.Reachable = true

	sort.Strings(ids)
	prefix := p.Prefix()
	st.Models = make([]LocalModel, 0, len(ids))
	for _, id := range ids {
		st.Models = append(st.Models, LocalModel{Spec: prefix + id, DisplayName: id})
	}

	ectx, cancel := context.WithTimeout(ctx, localEnrichTimeout)
	defer cancel()
	switch p.ID {
	case "ollama":
		enrichOllama(ectx, baseURL, st.Models)
	case "llamacpp":
		enrichLlamaCpp(ectx, baseURL, cred.Value, st.Models)
	}
	return st
}

// fetchOpenAIModelIDs GETs an OpenAI-compatible /models endpoint and returns
// the model ids. The bearer value is sent for servers started with an API key
// (llama-server --api-key); keyless servers ignore it.
func fetchOpenAIModelIDs(ctx context.Context, url, bearer string) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := localGetJSON(ctx, url, bearer, &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// nativeRoot strips the OpenAI-compat /v1 suffix to reach a server's native
// endpoints (Ollama /api/*, llama.cpp /props).
func nativeRoot(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/v1")
}

// enrichOllama marks loaded models via /api/ps and fills context windows via
// per-model /api/show. All failures are silent: the data is cosmetic.
func enrichOllama(ctx context.Context, baseURL string, models []LocalModel) {
	root := nativeRoot(baseURL)

	var ps struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := localGetJSON(ctx, root+"/api/ps", "", &ps); err == nil {
		loaded := make(map[string]bool, len(ps.Models))
		for _, m := range ps.Models {
			loaded[m.Name] = true
		}
		for i := range models {
			models[i].Loaded = loaded[models[i].DisplayName]
		}
	}

	var wg sync.WaitGroup
	for i := range models {
		if i >= localShowLimit {
			break
		}
		wg.Add(1)
		go func(m *LocalModel) {
			defer wg.Done()
			m.ContextWindow = ollamaContextLength(ctx, root, m.DisplayName)
		}(&models[i])
	}
	wg.Wait()
}

// ollamaContextLength POSTs /api/show for one model and extracts the
// architecture-prefixed context_length from model_info (e.g.
// "qwen3.context_length"). Returns 0 when unavailable.
func ollamaContextLength(ctx context.Context, root, model string) int64 {
	body, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, root+"/api/show", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := localHTTP.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var payload struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0
	}
	for k, v := range payload.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if f, ok := v.(float64); ok {
				return int64(f)
			}
		}
	}
	return 0
}

// enrichLlamaCpp reads the serving context (n_ctx) from llama.cpp's /props and
// applies it to every listed model (a plain llama-server lists exactly one).
func enrichLlamaCpp(ctx context.Context, baseURL, bearer string, models []LocalModel) {
	var props struct {
		DefaultGenerationSettings struct {
			NCtx int64 `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := localGetJSON(ctx, nativeRoot(baseURL)+"/props", bearer, &props); err != nil {
		return
	}
	if props.DefaultGenerationSettings.NCtx <= 0 {
		return
	}
	for i := range models {
		models[i].ContextWindow = props.DefaultGenerationSettings.NCtx
	}
}

// localGetJSON GETs a URL with an optional bearer token and decodes the JSON
// response into out.
func localGetJSON(ctx context.Context, url, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := localHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// RegisterLocalProviderHandlers wires the local-provider discovery RPC.
func RegisterLocalProviderHandlers(s *Server) {
	// providers.local_status — probe every local provider (cached ~5s) and
	// return reachability plus the live model list.
	s.RegisterHandler("providers.local_status", func(data map[string]any) (map[string]any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), localProbeTimeout+localEnrichTimeout)
		defer cancel()
		return map[string]any{
			"status":    "ok",
			"providers": localProviderStates(ctx),
		}, nil
	})
}

// --- Client side (connection-level RPC) ---

// LocalProviderStatus probes the local providers daemon-side and returns the
// per-provider reachability + live model lists, keyed by provider id.
func (c *Client) LocalProviderStatus() (map[string]LocalProviderState, error) {
	resp, err := c.sendRequest(map[string]any{"command": "providers.local_status"})
	if err != nil {
		return nil, err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		if msg == "" {
			msg = "local provider status request failed"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	raw, err := json.Marshal(resp["providers"])
	if err != nil {
		return nil, err
	}
	var states []LocalProviderState
	if err := json.Unmarshal(raw, &states); err != nil {
		return nil, err
	}
	out := make(map[string]LocalProviderState, len(states))
	for _, st := range states {
		out[st.Provider] = st
	}
	return out, nil
}
