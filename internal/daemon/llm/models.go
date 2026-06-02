package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kirby88/vix/internal/auth"
	"github.com/kirby88/vix/internal/config"
)

// ModelListing is one model discovered from a provider's model-list endpoint.
type ModelListing struct {
	Spec        string // full prefixed spec, e.g. "anthropic/claude-opus-4-8"
	Provider    string // provider id, e.g. "anthropic"
	DisplayName string // human-readable label
	Created     int64  // unix seconds the model was published; 0 if unknown
}

// modelsHTTPClient is used for model-list requests (separate from the streaming
// client; short timeout since these are quick metadata calls).
var modelsHTTPClient = &http.Client{Timeout: 20 * time.Second}

// ListModels fetches the available models for one provider using cred. The
// returned list is sorted newest-first so the latest models surface at the top.
func ListModels(ctx context.Context, provider ProviderID, cred config.Credential) ([]ModelListing, error) {
	switch provider {
	case ProviderAnthropic:
		return listAnthropicModels(ctx, "https://api.anthropic.com/v1", cred)
	case ProviderOpenAI:
		return listOpenAICompatibleModels(ctx, "https://api.openai.com/v1", "openai", cred, nil, keepOpenAIChatModel)
	case ProviderOpenRouter:
		return listOpenAICompatibleModels(ctx, "https://openrouter.ai/api/v1", "openrouter", cred, nil, nil)
	case ProviderMiniMax:
		return listOpenAICompatibleModels(ctx, miniMaxBaseURLFromEnv(), "minimax", cred, nil, nil)
	case ProviderMiMo:
		return listOpenAICompatibleModels(ctx, miMoBaseURLFromEnv(), "mimo", cred, nil, nil)
	case ProviderCopilot:
		return listCopilotModels(ctx, auth.GitHubCopilotBaseURL(cred.Value, ""), cred)
	}
	return nil, fmt.Errorf("model listing not supported for provider %q", provider)
}

// ListAllModels concurrently fetches models for every provider with a non-empty
// credential and returns a map keyed by provider id. Providers that error or
// have no credential are simply absent from the result (callers fall back to a
// curated list for those).
func ListAllModels(ctx context.Context, creds map[ProviderID]config.Credential) map[string][]ModelListing {
	results := map[string][]ModelListing{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for provider, cred := range creds {
		if cred.Value == "" {
			continue
		}
		wg.Add(1)
		go func(provider ProviderID, cred config.Credential) {
			defer wg.Done()
			models, err := ListModels(ctx, provider, cred)
			if err != nil {
				log.Printf("[models] list %s failed: %v", provider, err)
				return
			}
			if len(models) == 0 {
				return
			}
			mu.Lock()
			results[string(provider)] = models
			mu.Unlock()
		}(provider, cred)
	}

	wg.Wait()
	return results
}

// getModelsJSON performs an authenticated GET and decodes the JSON body.
func getModelsJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := modelsHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return fmt.Errorf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), snippet)
	}
	return json.Unmarshal(body, out)
}

// listAnthropicModels calls GET <baseURL>/models on the Anthropic API.
func listAnthropicModels(ctx context.Context, baseURL string, cred config.Credential) ([]ModelListing, error) {
	headers := map[string]string{
		"anthropic-version": "2023-06-01",
		"Accept":            "application/json",
	}
	if cred.Source == config.KeySourceOAuthToken {
		headers["Authorization"] = "Bearer " + cred.Value
		headers["anthropic-beta"] = "oauth-2025-04-20"
	} else {
		headers["x-api-key"] = cred.Value
	}

	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
	}
	if err := getModelsJSON(ctx, strings.TrimRight(baseURL, "/")+"/models?limit=1000", headers, &resp); err != nil {
		return nil, err
	}

	out := make([]ModelListing, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out = append(out, ModelListing{
			Spec:        "anthropic/" + m.ID,
			Provider:    "anthropic",
			DisplayName: name,
			Created:     parseUnixOrRFC3339(m.CreatedAt),
		})
	}
	sortModelsNewestFirst(out)
	return out, nil
}

// listOpenAICompatibleModels calls GET <baseURL>/models on any OpenAI
// chat-completions-compatible provider (OpenAI, OpenRouter, MiniMax, MiMo).
// prefix is the vix provider id used to build the model spec. keep, when
// non-nil, filters which model ids are included.
func listOpenAICompatibleModels(ctx context.Context, baseURL, prefix string, cred config.Credential, extraHeaders map[string]string, keep func(id string) bool) ([]ModelListing, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("no base URL configured for %s model listing", prefix)
	}
	headers := map[string]string{
		"Authorization": "Bearer " + cred.Value,
		"Accept":        "application/json",
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}

	var resp struct {
		Data []struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			Created json.RawMessage `json:"created"`
		} `json:"data"`
	}
	if err := getModelsJSON(ctx, strings.TrimRight(baseURL, "/")+"/models", headers, &resp); err != nil {
		return nil, err
	}

	out := make([]ModelListing, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID == "" {
			continue
		}
		if keep != nil && !keep(m.ID) {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, ModelListing{
			Spec:        prefix + "/" + m.ID,
			Provider:    prefix,
			DisplayName: name,
			Created:     parseLooseUnix(m.Created),
		})
	}
	sortModelsNewestFirst(out)
	return out, nil
}

// listCopilotModels calls GET <base>/models. base is derived from the proxy
// endpoint embedded in the Copilot token (passed in for testability).
func listCopilotModels(ctx context.Context, base string, cred config.Credential) ([]ModelListing, error) {
	headers := map[string]string{
		"Authorization":          "Bearer " + cred.Value,
		"Accept":                 "application/json",
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.107.0",
		"Editor-Plugin-Version":  "copilot-chat/0.35.0",
		"User-Agent":             "GitHubCopilotChat/0.35.0",
	}

	var resp struct {
		Data []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled *bool  `json:"model_picker_enabled"`
			Capabilities       struct {
				Type string `json:"type"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := getModelsJSON(ctx, strings.TrimRight(base, "/")+"/models", headers, &resp); err != nil {
		return nil, err
	}

	out := make([]ModelListing, 0, len(resp.Data))
	seen := map[string]bool{}
	for _, m := range resp.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		// Only chat models a user can actually pick.
		if m.Capabilities.Type != "" && m.Capabilities.Type != "chat" {
			continue
		}
		if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
			continue
		}
		seen[m.ID] = true
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, ModelListing{
			Spec:        "github-copilot/" + m.ID,
			Provider:    "github-copilot",
			DisplayName: name,
		})
	}
	// Copilot's catalogue has no timestamps; keep stable alphabetical-ish order
	// by display name so the list isn't random.
	sort.SliceStable(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

// keepOpenAIChatModel filters the (large, mixed) OpenAI model list down to
// chat-capable models, excluding embeddings/audio/image/moderation entries.
func keepOpenAIChatModel(id string) bool {
	l := strings.ToLower(id)
	for _, bad := range []string{"embedding", "whisper", "tts", "dall-e", "moderation", "audio", "transcribe", "realtime", "image", "search", "babbage", "davinci", "codex"} {
		if strings.Contains(l, bad) {
			return false
		}
	}
	return strings.HasPrefix(l, "gpt-") ||
		strings.HasPrefix(l, "o1") ||
		strings.HasPrefix(l, "o3") ||
		strings.HasPrefix(l, "o4") ||
		strings.HasPrefix(l, "chatgpt")
}

// sortModelsNewestFirst sorts by Created descending, then DisplayName ascending
// for stability when timestamps are equal/unknown.
func sortModelsNewestFirst(models []ModelListing) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Created != models[j].Created {
			return models[i].Created > models[j].Created
		}
		return models[i].DisplayName < models[j].DisplayName
	})
}

// parseUnixOrRFC3339 accepts either a unix-seconds string or an RFC3339
// timestamp and returns unix seconds (0 on failure).
func parseUnixOrRFC3339(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	var n int64
	if _, err := fmt.Sscan(s, &n); err == nil {
		return n
	}
	return 0
}

// parseLooseUnix reads a JSON number or numeric string as unix seconds.
func parseLooseUnix(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseUnixOrRFC3339(s)
	}
	return 0
}

// miniMaxBaseURLFromEnv / miMoBaseURLFromEnv mirror the streaming adapters'
// base-URL resolution so model listing hits the same endpoint.
func miniMaxBaseURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("MINIMAX_BASE_URL")); v != "" {
		return v
	}
	if strings.TrimSpace(os.Getenv("MINIMAX_REGION")) == "cn" {
		return "https://api.minimaxi.com/v1"
	}
	return "https://api.minimax.io/v1"
}

func miMoBaseURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("MIMO_BASE_URL")); v != "" {
		return v
	}
	return "https://api.xiaomimimo.com/v1"
}
