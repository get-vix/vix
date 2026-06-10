package daemon

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vixconfig "github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	promptloader "github.com/get-vix/vix/internal/daemon/prompt"
	"github.com/get-vix/vix/internal/protocol"
)

// SubagentConfig defines how a subagent behaves.
type SubagentConfig struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`   // short description for LLM tool listing
	Model        string   `json:"model,omitempty"`         // empty = inherit parent model
	Effort       string   `json:"effort,omitempty"`        // "adaptive", "low", "medium", "high", "max", or "" (inherit)
	Tools        []string `json:"tools,omitempty"`         // tool name filter; nil = all tools
	MaxTurns     int      `json:"max_turns,omitempty"`     // 0 = default (20)
	MaxTokens    int      `json:"max_tokens,omitempty"`    // per-LLM-call output token cap; 0 = default (32768)
	SystemPrompt string   `json:"system_prompt,omitempty"`
}

// SubagentResult holds the output of a completed subagent run.
type SubagentResult struct {
	Output              string
	IsError             bool
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	Elapsed             time.Duration
}

// TurnHooks provides typed callbacks for streaming events between LLM turns.
// All fields are optional — nil callbacks are skipped.
type TurnHooks struct {
	OnStreamDelta   func(delta string)
	OnThinkingDelta func(delta string)
	OnStreamDone    func(inputTokens, outputTokens, cacheCreation, cacheRead, elapsedMs int64)
	OnToolCall      func(ev protocol.EventToolCall)
	OnToolResult    func(toolID, name string, input map[string]any, output string, isError bool)
	OnBeforeStream  func(cancel context.CancelFunc)
	// OnRetry is called when a retryable API error is about to be retried.
	// Mirrors session.streamWithRetry's event.retry emission so workflow-agent
	// retries become visible in the trajectory instead of only vixd.log.
	OnRetry func(attempt, maxRetries, waitSecs int, reason string)
	// OnThinkingStall is called when a thinking block exceeded its stall
	// timeout. The caller appends a nudge message and retries; this hook
	// lets the TUI surface the event.
	OnThinkingStall func(elapsedMs int64, summaryChars int)
}

// BackgroundTask tracks an in-flight or completed background subagent.
type BackgroundTask struct {
	ID     string
	Name   string
	Done   chan struct{}
	Result *SubagentResult
	cancel context.CancelFunc
}

// taskCounter generates unique task IDs.
var taskCounter atomic.Int64

func nextTaskID() string {
	return fmt.Sprintf("task_%d", taskCounter.Add(1))
}

// RunSubagent executes a subagent with its own conversation, tools, and LLM instance.
// It blocks until the subagent completes or the context is cancelled.
// executeTool is called directly (in-process, no socket round-trip).
// searchDirs is the ordered set of .vix root directories to resolve system
// prompt includes from, in precedence order (highest first).
//
// toolTimeoutDefault and toolTimeoutMax propagate the parent session's
// tool_timeouts bounds so tool calls made by the subagent honour the same
// floor/cap as the rest of the session. Passing zero for either falls back
// to package-level defaults (defaultToolTimeoutDefault / defaultToolTimeoutMax).
func RunSubagent(
	ctx context.Context,
	config SubagentConfig,
	prompt string,
	cred vixconfig.Credential,
	parentModel string,
	executeTool func(name string, params map[string]any, cwd string) (*ToolResult, error),
	cwd string,
	hooks *TurnHooks,
	toolTimeoutDefault time.Duration,
	toolTimeoutMax time.Duration,
	searchDirs ...string,
) (*SubagentResult, error) {
	model := config.Model
	if model == "" {
		model = parentModel
	}

	maxTurns := config.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}

	effort := config.Effort
	if effort == "" {
		effort = llm.DefaultEffortFromSpec(model)
	}
	client, err := llm.NewFromModel(model, PluginConfig{}, effort, int64(config.MaxTokens))
	if err != nil {
		return nil, fmt.Errorf("cannot run subagent: %w", err)
	}
	tools := FilterToolSchemasWithBounds(config.Tools, toolTimeoutDefault, toolTimeoutMax)

	// Apply full template resolution ($(), $(file:), $(call:)) to the system prompt
	sysPrompt := promptloader.GetLoader().Resolve(
		config.SystemPrompt,
		map[string]string{"working_directory": cwd},
		promptloader.JoinSearchDirs(searchDirs...),
		nil,
	)
	system := []llm.SystemBlock{{Text: sysPrompt}}

	messages := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock(prompt)),
	}

	var totalInputTokens, totalOutputTokens, totalCacheCreation, totalCacheRead int64
	var totalElapsed time.Duration

	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return &SubagentResult{Output: "Cancelled", IsError: true}, ctx.Err()
		}

		var onDelta func(string)
		if hooks != nil && hooks.OnStreamDelta != nil {
			onDelta = hooks.OnStreamDelta
		}
		var onThinkingDelta func(string)
		if hooks != nil && hooks.OnThinkingDelta != nil {
			onThinkingDelta = hooks.OnThinkingDelta
		}

		msg, elapsed, err := client.StreamMessage(ctx, system, messages, tools, onDelta, onThinkingDelta)
		if err != nil {
			// Thinking stall: append the nudge and continue the turn loop.
			// Unlike session/workflow this has no outer retry budget — it's
			// bounded by maxTurns, so a pathological stall still terminates.
			// finalNext=false: subagent doesn't have a "final retry with
			// thinking disabled" concept; turns are semantically distinct
			// units, not retries of the same intent.
			if stallErr, nudge, ok := asThinkingStall(err, turn+1, maxTurns, false); ok {
				log.Printf("\033[31m[subagent] thinking stall after %s (turn %d/%d, nudging and retrying)\033[0m",
					stallErr.Elapsed, turn+1, maxTurns)
				if hooks != nil && hooks.OnThinkingStall != nil {
					hooks.OnThinkingStall(stallErr.Elapsed.Milliseconds(), len(stallErr.Summary))
				}
				messages = append(messages, nudge)
				continue
			}
			return &SubagentResult{Output: err.Error(), IsError: true}, err
		}

		LogLLMCall(model, system, messages, tools, msg)

		totalInputTokens += msg.Usage.InputTokens
		totalOutputTokens += msg.Usage.OutputTokens
		totalCacheCreation += msg.Usage.CacheCreationTokens
		totalCacheRead += msg.Usage.CacheReadTokens
		totalElapsed += elapsed

		if hooks != nil && hooks.OnStreamDone != nil {
			hooks.OnStreamDone(msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheCreationTokens, msg.Usage.CacheReadTokens, elapsed.Milliseconds())
		}

		messages = append(messages, msg.ToParam())

		if msg.StopReason == llm.StopEndTurn {
			text := extractTextFromMessage(msg)
			return &SubagentResult{
				Output:              text,
				InputTokens:         totalInputTokens,
				OutputTokens:        totalOutputTokens,
				CacheCreationTokens: totalCacheCreation,
				CacheReadTokens:     totalCacheRead,
				Elapsed:             totalElapsed,
			}, nil
		}

		if msg.StopReason == llm.StopToolUse {
			toolResults := subagentDispatchToolCalls(ctx, msg, executeTool, cwd, hooks, toolTimeoutDefault, toolTimeoutMax)
			if ctx.Err() != nil {
				return &SubagentResult{Output: "Cancelled", IsError: true}, ctx.Err()
			}
			messages = append(messages, llm.NewUserMessage(toolResults...))
			continue
		}

		if msg.StopReason == llm.StopMaxTokens {
			// Continue the conversation — the assistant message is already appended above
			messages = append(messages, llm.NewUserMessage(
				llm.NewTextBlock("Continue from where you left off."),
			))
			continue
		}

		return &SubagentResult{
			Output:              fmt.Sprintf("unexpected stop reason: %s", msg.StopReason),
			IsError:             true,
			InputTokens:         totalInputTokens,
			OutputTokens:        totalOutputTokens,
			CacheCreationTokens: totalCacheCreation,
			CacheReadTokens:     totalCacheRead,
			Elapsed:             totalElapsed,
		}, nil
	}

	lastText := ""
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		for _, block := range last.Content {
			if block.Type == llm.BlockText {
				lastText += block.Text
			}
		}
	}
	if lastText == "" {
		lastText = fmt.Sprintf("Subagent '%s' reached max turns (%d) without completing.", config.Name, maxTurns)
	}
	return &SubagentResult{
		Output:              lastText,
		InputTokens:         totalInputTokens,
		OutputTokens:        totalOutputTokens,
		CacheCreationTokens: totalCacheCreation,
		CacheReadTokens:     totalCacheRead,
		Elapsed:             totalElapsed,
	}, nil
}

// subagentDispatchToolCalls executes tool calls for a subagent or workflow agent
// using the unified dispatcher. No confirmation prompts, no interactive tool
// handlers — tools run directly with confirmed=true.
//
// toolTimeoutDefault and toolTimeoutMax propagate the session-configured
// tool_timeouts bounds from settings.json into the dispatcher so workflow
// agent tool calls honour the same floor/cap as the main agent. Passing zero
// for either value falls back to the package-level defaults
// (defaultToolTimeoutDefault / defaultToolTimeoutMax).
func subagentDispatchToolCalls(
	ctx context.Context,
	msg *llm.Message,
	executeTool func(name string, params map[string]any, cwd string) (*ToolResult, error),
	cwd string,
	hooks *TurnHooks,
	toolTimeoutDefault time.Duration,
	toolTimeoutMax time.Duration,
) []llm.ContentBlock {
	opts := dispatchOptions{
		cwd:                cwd,
		toolTimeoutDefault: toolTimeoutDefault,
		toolTimeoutMax:     toolTimeoutMax,
		executeTool: func(name string, input map[string]any) *ToolResult {
			input["confirmed"] = true
			result, err := executeTool(name, input, cwd)
			if err != nil {
				return &ToolResult{Output: err.Error(), IsError: true}
			}
			return result
		},
		emitToolCall: func(ev protocol.EventToolCall) {
			if hooks != nil && hooks.OnToolCall != nil {
				hooks.OnToolCall(ev)
			}
		},
		emitToolResult: func(toolID, name string, input map[string]any, output string, isError bool, _ int) {
			if hooks != nil && hooks.OnToolResult != nil {
				hooks.OnToolResult(toolID, name, input, output, isError)
			}
		},
	}
	return dispatchToolCalls(ctx, msg, opts)
}

// LoadCustomAgents parses .vix/agents/*.md files into SubagentConfig entries.
func LoadCustomAgents(dir string) map[string]SubagentConfig {
	agents := make(map[string]SubagentConfig)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return agents
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		config, err := parseAgentFile(path)
		if err != nil {
			log.Printf("[subagent] failed to parse %s: %v", path, err)
			continue
		}

		agents[config.Name] = config
	}

	return agents
}

// parseAgentFile reads a markdown file with YAML-like frontmatter.
func parseAgentFile(path string) (SubagentConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return SubagentConfig{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var config SubagentConfig
	var body strings.Builder

	state := 0

	for scanner.Scan() {
		line := scanner.Text()

		switch state {
		case 0:
			if strings.TrimSpace(line) == "---" {
				state = 1
			}
		case 1:
			if strings.TrimSpace(line) == "---" {
				state = 2
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])

			switch key {
			case "name":
				config.Name = val
			case "description":
				config.Description = val
			case "model":
				config.Model = val
			case "tools":
				for _, t := range strings.Split(val, ",") {
					t = strings.TrimSpace(t)
					if t != "" {
						config.Tools = append(config.Tools, t)
					}
				}
			case "effort":
				config.Effort = val
			case "max_turns":
				fmt.Sscanf(val, "%d", &config.MaxTurns)
			case "max_tokens":
				fmt.Sscanf(val, "%d", &config.MaxTokens)
			}
		case 2:
			body.WriteString(line)
			body.WriteString("\n")
		}
	}

	if config.Name == "" {
		base := filepath.Base(path)
		config.Name = strings.TrimSuffix(base, ".md")
	}

	config.SystemPrompt = strings.TrimSpace(body.String())
	if config.SystemPrompt == "" {
		config.SystemPrompt = fmt.Sprintf("You are the '%s' agent. Complete the given task.", config.Name)
	}

	return config, scanner.Err()
}

// BackgroundTaskRegistry manages background subagent tasks.
type BackgroundTaskRegistry struct {
	tasks sync.Map
}

func (r *BackgroundTaskRegistry) Store(task *BackgroundTask) {
	r.tasks.Store(task.ID, task)
}

func (r *BackgroundTaskRegistry) Load(id string) (*BackgroundTask, bool) {
	v, ok := r.tasks.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*BackgroundTask), true
}

// SpawnBackground launches a subagent in a goroutine and returns a task ID.
// The task gets its own cancellable context derived from the parent ctx, so it
// can be cancelled individually (via Cancel) or in bulk (via CancelAll) without
// affecting the parent.
func (r *BackgroundTaskRegistry) SpawnBackground(
	ctx context.Context,
	config SubagentConfig,
	prompt string,
	cred vixconfig.Credential,
	parentModel string,
	executeTool func(name string, params map[string]any, cwd string) (*ToolResult, error),
	cwd string,
	toolTimeoutDefault time.Duration,
	toolTimeoutMax time.Duration,
	searchDirs ...string,
) string {
	id := nextTaskID()
	taskCtx, cancel := context.WithCancel(ctx)
	task := &BackgroundTask{
		ID:     id,
		Name:   config.Name,
		Done:   make(chan struct{}),
		cancel: cancel,
	}
	r.Store(task)

	go func() {
		defer close(task.Done)
		defer cancel()

		t0 := time.Now()
		result, err := RunSubagent(taskCtx, config, prompt, cred, parentModel, executeTool, cwd, nil, toolTimeoutDefault, toolTimeoutMax, searchDirs...)
		elapsed := time.Since(t0)

		if err != nil && result == nil {
			result = &SubagentResult{Output: err.Error(), IsError: true}
		}

		log.Printf("[subagent] background task %s (%s) completed in %v", id, config.Name, elapsed)
		task.Result = result
	}()

	return id
}

// Cancel cancels a single in-flight background task by ID. No-op if the task
// is unknown or already finished.
func (r *BackgroundTaskRegistry) Cancel(id string) {
	if task, ok := r.Load(id); ok && task.cancel != nil {
		task.cancel()
	}
}

// CancelAll cancels every in-flight background task in the registry. Completed
// tasks are unaffected (their cancel funcs are no-ops).
func (r *BackgroundTaskRegistry) CancelAll() {
	r.tasks.Range(func(_, v any) bool {
		if task, ok := v.(*BackgroundTask); ok && task.cancel != nil {
			task.cancel()
		}
		return true
	})
}

// WaitForTask blocks until the task completes or the context is cancelled.
func (r *BackgroundTaskRegistry) WaitForTask(ctx context.Context, id string, timeout time.Duration) (*SubagentResult, error) {
	task, ok := r.Load(id)
	if !ok {
		return nil, fmt.Errorf("unknown task ID: %s", id)
	}

	select {
	case <-task.Done:
		return task.Result, nil
	default:
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-task.Done:
		return task.Result, nil
	case <-timer.C:
		return &SubagentResult{
			Output: fmt.Sprintf("Task %s (%s) is still running. Try again later.", id, task.Name),
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// extractTextFromMessage pulls the text content from an LLM message.
func extractTextFromMessage(msg *llm.Message) string {
	if msg.TextContent != "" {
		return msg.TextContent
	}
	var parts []string
	for _, block := range msg.Content {
		if block.Type == llm.BlockText {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, " ")
}
