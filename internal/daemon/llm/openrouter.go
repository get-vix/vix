package llm

import (
	"encoding/json"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"

// NewOpenRouter constructs the OpenRouter adapter (OpenAI Chat Completions wire
// format) with OpenRouter's app-attribution headers and per-request extras
// (usage accounting and optional upstream routing).
func NewOpenRouter(cfg Config) (Client, error) {
	var reqOpts []option.RequestOption
	if cfg.OpenRouter.HTTPReferer != "" {
		reqOpts = append(reqOpts, option.WithHeader("HTTP-Referer", cfg.OpenRouter.HTTPReferer))
	}
	if cfg.OpenRouter.XTitle != "" {
		reqOpts = append(reqOpts, option.WithHeader("X-Title", cfg.OpenRouter.XTitle))
	}

	model := cfg.Model
	routing := cfg.OpenRouter.Routing
	tune := func(params *openai.ChatCompletionNewParams, effort string) []option.RequestOption {
		addReasoningEffort(params, effort, model)
		out := []option.RequestOption{option.WithJSONSet("usage.include", true)}
		if routing != nil {
			if routingJSON, err := json.Marshal(routing); err == nil {
				out = append(out, option.WithJSONSet("provider", json.RawMessage(routingJSON)))
			}
		}
		return out
	}

	return newChatCompletionsClient(cfg, chatCompletionsConfig{
		provider: ProviderOpenRouter,
		baseURL:  openRouterDefaultBaseURL,
		reqOpts:  reqOpts,
		tune:     tune,
	}), nil
}
