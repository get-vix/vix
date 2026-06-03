package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// llmLogDir is the directory for LLM call logs, set during server init.
var llmLogDir string

// SetLLMLogDir sets the directory for LLM call logs.
func SetLLMLogDir(dir string) {
	llmLogDir = dir
}

// LogLLMCall logs an LLM call and response to {logDir}/{datetime}.json
// using the provider-neutral types so every adapter produces the same
// log shape.
func LogLLMCall(
	model string,
	system []llm.SystemBlock,
	messages []llm.MessageParam,
	tools []llm.ToolParam,
	response *llm.Message,
) {
	defer func() {
		recover() // Never let logging break the agent
	}()

	logDir := llmLogDir
	if logDir == "" {
		logDir = filepath.Join(".vix", "logs")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}

	now := time.Now()
	ts := now.Format("20060102_150405_000000")

	var content []map[string]any
	for _, block := range response.Content {
		switch block.Type {
		case llm.BlockText:
			content = append(content, map[string]any{
				"type": "text",
				"text": block.Text,
			})
		case llm.BlockThinking:
			content = append(content, map[string]any{
				"type":      "thinking",
				"text":      block.Text,
				"signature": block.Signature,
			})
		case llm.BlockToolUse:
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    block.ID,
				"name":  block.Name,
				"input": block.Input,
			})
		default:
			content = append(content, map[string]any{
				"type": string(block.Type),
			})
		}
	}

	var usage map[string]any
	if response.Usage.InputTokens > 0 || response.Usage.OutputTokens > 0 {
		usage = map[string]any{
			"input_tokens":          response.Usage.InputTokens,
			"output_tokens":         response.Usage.OutputTokens,
			"cache_creation_tokens": response.Usage.CacheCreationTokens,
			"cache_read_tokens":     response.Usage.CacheReadTokens,
		}
		if response.Usage.ReasoningTokens > 0 {
			usage["reasoning_tokens"] = response.Usage.ReasoningTokens
		}
		if response.Usage.CostUSD > 0 {
			usage["cost_usd"] = response.Usage.CostUSD
		}
	}

	var systemParts []string
	for _, block := range system {
		systemParts = append(systemParts, block.Text)
	}

	logEntry := map[string]any{
		"timestamp": now.Format(time.RFC3339),
		"model":     model,
		"system":    strings.Join(systemParts, ""),
		"messages":  messages,
		"tools":     tools,
		"response": map[string]any{
			"content":     content,
			"stop_reason": string(response.StopReason),
			"usage":       usage,
		},
	}

	data, err := json.MarshalIndent(logEntry, "", "  ")
	if err != nil {
		return
	}

	os.WriteFile(filepath.Join(logDir, ts+".json"), data, 0o644)
}
