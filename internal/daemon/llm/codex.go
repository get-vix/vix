package llm

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/openai/openai-go/option"

	"github.com/kirby88/vix/internal/auth"
)

// codexBaseURL is the ChatGPT backend's Codex Responses endpoint root. The SDK
// appends "/responses" to it. OpenAI endorses OSS harnesses using a ChatGPT
// (Codex) subscription through this flow, so — unlike the removed Copilot path
// — we identify honestly as vix rather than impersonating the official client.
const codexBaseURL = "https://chatgpt.com/backend-api/codex"

// NewCodex constructs the ChatGPT/Codex subscription adapter. It reuses the
// OpenAI Responses adapter (identical wire format) with the ChatGPT base URL,
// the OAuth access token as a Bearer credential, and the account/session
// headers the backend expects.
//
// The credential value is the Codex OAuth access token (a JWT). The
// chatgpt-account-id header is derived from that token's claims.
//
// The ChatGPT backend's exact contract is not covered by automated tests here.
// If a live `vix login openai-codex` session misbehaves, the likely tuning
// points are: the OpenAI-Beta value, whether session_id is required, and
// whether the Responses request must set store=false (the shared adapter
// leaves store at the server default).
func NewCodex(cfg Config) (Client, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = codexBaseURL
	}

	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithAPIKey(cfg.Credential.Value), // sent as Authorization: Bearer
		option.WithBaseURL(baseURL),
		option.WithHeader("OpenAI-Beta", "responses=experimental"),
		option.WithHeader("originator", "vix"),
	}
	if accountID := auth.CodexAccountID(cfg.Credential.Value); accountID != "" {
		opts = append(opts, option.WithHeader("chatgpt-account-id", accountID))
	}
	if sessionID := newCodexSessionID(); sessionID != "" {
		opts = append(opts, option.WithHeader("session_id", sessionID))
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = NewPluginHTTPClient(cfg.PluginCfg)
	}
	opts = append(opts, option.WithHTTPClient(httpClient))

	return newResponsesClient(cfg, ProviderCodex, opts), nil
}

// newCodexSessionID returns a random hex session identifier, or "" if the
// system RNG is unavailable (the header is then simply omitted).
func newCodexSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
