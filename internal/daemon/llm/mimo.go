package llm

import (
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// mimoDefaultBaseURL is the first-party Xiaomi MiMo Open Platform endpoint.
const mimoDefaultBaseURL = "https://api.xiaomimimo.com/v1"

// NewMiMo constructs the Xiaomi MiMo adapter. MiMo exposes an OpenAI-compatible
// Chat Completions endpoint with no GroupId/region quirks — just a base URL and
// a Bearer API key. Its reasoning-capable models take the standard OpenAI
// reasoning_effort knob.
func NewMiMo(cfg Config) (Client, error) {
	baseURL := cfg.MiMo.BaseURL
	if baseURL == "" {
		baseURL = mimoDefaultBaseURL
	}

	model := cfg.Model
	return newChatCompletionsClient(cfg, chatCompletionsConfig{
		provider: ProviderMiMo,
		baseURL:  baseURL,
		tune: func(params *openai.ChatCompletionNewParams, effort string) []option.RequestOption {
			addReasoningEffort(params, effort, model)
			return nil
		},
	}), nil
}
