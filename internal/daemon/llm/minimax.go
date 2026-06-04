package llm

import (
	"log"
	"net/http"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const (
	miniMaxIntlBaseURL = "https://api.minimax.io/v1"
	miniMaxCNBaseURL   = "https://api.minimaxi.com/v1"
)

// NewMiniMax constructs the MiniMax adapter (OpenAI Chat Completions wire
// format) with a GroupId query-param middleware and MiniMax's binary
// reasoning_split knob.
func NewMiniMax(cfg Config) (Client, error) {
	baseURL := cfg.MiniMax.BaseURL
	if baseURL == "" {
		if cfg.MiniMax.Region == "cn" {
			baseURL = miniMaxCNBaseURL
		} else {
			baseURL = miniMaxIntlBaseURL
		}
	}

	var reqOpts []option.RequestOption
	if cfg.MiniMax.GroupID == "" {
		log.Printf("[llm minimax] MINIMAX_GROUP_ID is unset; some workspaces require it")
	} else {
		reqOpts = append(reqOpts, option.WithMiddleware(miniMaxGroupIDMiddleware(cfg.MiniMax.GroupID)))
	}

	return newChatCompletionsClient(cfg, chatCompletionsConfig{
		provider: ProviderMiniMax,
		baseURL:  baseURL,
		reqOpts:  reqOpts,
		// MiniMax M2 has no effort level — reasoning is binary, and the knob is
		// a JSON body field rather than the OpenAI reasoning_effort param.
		tune: func(_ *openai.ChatCompletionNewParams, effort string) []option.RequestOption {
			if effort != "" {
				return []option.RequestOption{option.WithJSONSet("reasoning_split", true)}
			}
			return nil
		},
	}), nil
}

// miniMaxGroupIDMiddleware appends ?GroupId=<id> to every outgoing request.
func miniMaxGroupIDMiddleware(groupID string) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		q := req.URL.Query()
		q.Set("GroupId", groupID)
		req.URL.RawQuery = q.Encode()
		return next(req)
	}
}
