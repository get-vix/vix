package llm

import (
	"github.com/openai/openai-go/option"

	"github.com/get-vix/vix/internal/config"
)

// openaiAuthOptions builds openai-go request options for cred. OpenAI-family
// providers (OpenAI, OpenRouter, MiniMax, MiMo) send both API keys and OAuth
// access tokens as Authorization: Bearer, so the value always goes through
// WithAPIKey regardless of HeaderStyle. Any extra headers implied by the auth
// method (e.g. chatgpt-account-id for the Codex backend) are applied on top.
func openaiAuthOptions(cred config.Credential) []option.RequestOption {
	opts := []option.RequestOption{option.WithAPIKey(cred.Value)}
	for k, v := range cred.ExtraHeaders {
		opts = append(opts, option.WithHeader(k, v))
	}
	return opts
}
