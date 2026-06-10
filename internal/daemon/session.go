package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/get-vix/vix/internal/agent"
	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/daemon/mcp"
	"github.com/get-vix/vix/internal/daemon/prompt"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/providers"
	"github.com/get-vix/vix/internal/telemetry"
)

// Session manages a single agent session over a persistent socket connection.
type Session struct {
	id                             string
	parentID                       string // non-empty if this session was forked; set once at creation
	forkTurnIdx                    int    // which turn it was forked at (0-based); meaningful only when parentID != ""
	server                         *Server
	llm                            LLM
	model                          string
	cwd                            string
	paths                          config.VixPaths
	forceInit                      bool
	enableAutomaticWritePermission bool
	enableAutomaticDirectoryAccess bool
	headless                       bool
	eventChan                      chan protocol.SessionEvent
	commandChan                    chan protocol.SessionCommand
	ctx                            context.Context
	cancel                         context.CancelFunc

	// Agent state
	messages []llm.MessageParam
	tools    []llm.ToolParam

	// Fork snapshots: a copy of messages after each completed normal turn,
	// protected by mu. Used by snapshotMessagesForFork to seed forked sessions.
	mu            sync.Mutex
	turnSnapshots [][]llm.MessageParam
	// lastInputTokens is the true prompt size (input + cache read + cache
	// creation) of the most recent turn. Drives auto-compaction. Protected
	// by mu; reset to 0 after a compaction so it doesn't immediately retrigger.
	lastInputTokens int64
	activePlan      *protocol.Plan
	backgroundTasks BackgroundTaskRegistry
	bashJobs        BashJobRegistry
	customAgents    map[string]SubagentConfig

	// Workflows loaded from config/workflow.json. Guarded by workflowsMu
	// because the daemon's config watcher can swap the slice from a separate
	// goroutine (hot reload) while the session loop reads it.
	workflowsMu sync.RWMutex
	workflows   []*WorkflowDef

	// Skills registry
	skills *agent.SkillRegistry

	// MCP server pool (nil when no mcp_servers are configured).
	mcpPool *mcp.Pool

	// Chat agent name from config
	chatAgent string

	// Project config (feature flags, etc.)
	projectConfig ProjectConfig

	// Allowed directories beyond cwd (runtime-approved or loaded from config).
	allowedDirsMu sync.RWMutex
	allowedDirs   map[string]bool

	// Deny list: paths and URLs that must be blocked regardless of
	// allowlist. Loaded from `deny_list` in settings.json and never mutated
	// at runtime. denyList stores cleaned absolute paths; denyURLs stores
	// raw entries (canonicalized at match time so we don't lose user
	// intent like trailing slashes / casing).
	denyListMu sync.RWMutex
	denyList   []string
	denyURLs   []string

	// Files approved for write-class operations (session-scoped, not persisted).
	approvedWriteFilesMu sync.RWMutex
	approvedWriteFiles   map[string]bool

	// Files that have been successfully read (or written) in this session.
	// Consulted by the read-before-edit gate to reject hallucinated patches.
	readFilesMu sync.RWMutex
	readFiles   map[string]bool

	// Session-scoped TODO list (not persisted).
	todoMu   sync.RWMutex
	todoList []protocol.TodoItem

	// Per-session thinking log (debug output written to TmpLogDir).
	thinkingLogMu   sync.Mutex
	thinkingLogFile *os.File
	thinkingInTurn  bool

	// Active LLM call cancellation
	cancelStream context.CancelFunc

	// configErr is non-nil when the session has no usable LLM client (e.g. no
	// credential for the selected model's provider). While set, s.llm is nil
	// and the session refuses to stream; the error surfaces to the UI on the
	// next input attempt.
	configErr error

	// Active plan/workflow cancellation
	planCancel context.CancelFunc

	// Buffered channel for user messages to inject into the running workflow agent.
	// Capacity 1: only the latest message is kept (newer overwrites older on the UI side).
	workflowMsgChan chan string

	// Telemetry accumulators
	startTime         time.Time
	lastRequestAt     time.Time
	turnCount         int
	totalInputTokens  int64
	totalOutputTokens int64
	totalCacheRead    int64
	totalCacheWrite   int64
	totalAPIWaitMs    int64
	sessionMode       string // "chat" or "workflow"
	activeWorkflow    string // name of the active workflow when sessionMode=="workflow"

	// workflowRunState is the last published snapshot of an in-flight (or
	// interrupted) workflow run — its resume cursor, step results, per-step
	// agent conversations, and budget accounting. Guarded by s.mu; snapshots
	// are immutable once published (see saveWorkflowProgress). Persisted in
	// the session record so interrupted runs survive a daemon restart.
	workflowRunState *WorkflowRunState

	// Persistence/attach state.
	// attachRecord is non-nil when this session is resuming a persisted record;
	// Run() emits event.replay (with restore validation) after initBrain.
	attachRecord *sessionRecord
	// closedByUser is set when a session.close command is received (the TUI "x"
	// action), distinguishing an explicit close (move record open->closed) from
	// a bare disconnect (record stays open for next-run reopen).
	closedByUser bool
}

// NewSession creates a new agent session.
func NewSession(id string, server *Server, llmClient LLM, model, cwd, configDir string, forceInit bool, enableAutomaticWritePermission bool, enableAutomaticDirectoryAccess bool, headless bool, parentCtx context.Context) *Session {
	ctx, cancel := context.WithCancel(parentCtx)
	return &Session{
		id:                             id,
		server:                         server,
		llm:                            llmClient,
		model:                          model,
		cwd:                            cwd,
		paths:                          config.NewVixPaths(configDir, server.homeVixDir, cwd),
		forceInit:                      forceInit,
		enableAutomaticWritePermission: enableAutomaticWritePermission,
		enableAutomaticDirectoryAccess: enableAutomaticDirectoryAccess,
		headless:                       headless,
		eventChan:                      make(chan protocol.SessionEvent, 256),
		commandChan:                    make(chan protocol.SessionCommand, 16),
		workflowMsgChan:                make(chan string, 1),
		ctx:                            ctx,
		cancel:                         cancel,
		tools:                          ToolSchemas(),
		todoList:                       make([]protocol.TodoItem, 0),
		startTime:                      time.Now(),
		sessionMode:                    "chat",
	}
}

// isPathAllowed returns true if absPath is accessible by default (cwd, $HOME,
// system dirs, or any runtime-approved directory). It delegates to
// isAccessibleByDefault so the gate used by all file tools is consistent with
// the one used by the bash tool's detectOutsidePaths heuristic.
func (s *Session) isPathAllowed(absPath string) bool {
	return isAccessibleByDefault(absPath, s.cwd, s.getAllowedDirs())
}

// pathHasAncestor returns true if path equals ancestor or is contained under it.
// Special-cased for ancestor=="/" so that any absolute path matches — without
// this, `dir + sep` becomes "//" and HasPrefix never matches anything.
func pathHasAncestor(path, ancestor string) bool {
	if path == ancestor {
		return true
	}
	sep := string(filepath.Separator)
	if ancestor == sep {
		return strings.HasPrefix(path, sep)
	}
	return strings.HasPrefix(path, ancestor+sep)
}

// addAllowedDir adds a directory to the session's runtime allowed set.
func (s *Session) addAllowedDir(absPath string) {
	s.allowedDirsMu.Lock()
	defer s.allowedDirsMu.Unlock()
	if s.allowedDirs == nil {
		s.allowedDirs = make(map[string]bool)
	}
	s.allowedDirs[absPath] = true
}

// getAllowedDirs returns a snapshot of all allowed directories.
func (s *Session) getAllowedDirs() []string {
	s.allowedDirsMu.RLock()
	defer s.allowedDirsMu.RUnlock()
	dirs := make([]string, 0, len(s.allowedDirs))
	for dir := range s.allowedDirs {
		dirs = append(dirs, dir)
	}
	return dirs
}

// denyListSnapshot returns a copy of the session's deny_list path entries.
// The returned slice is safe to read without holding the mutex.
func (s *Session) denyListSnapshot() []string {
	s.denyListMu.RLock()
	defer s.denyListMu.RUnlock()
	if len(s.denyList) == 0 {
		return nil
	}
	out := make([]string, len(s.denyList))
	copy(out, s.denyList)
	return out
}

// denyURLsSnapshot returns a copy of the session's deny_list URL entries.
func (s *Session) denyURLsSnapshot() []string {
	s.denyListMu.RLock()
	defer s.denyListMu.RUnlock()
	if len(s.denyURLs) == 0 {
		return nil
	}
	out := make([]string, len(s.denyURLs))
	copy(out, s.denyURLs)
	return out
}

// toolAllowedDirs returns the allowed-dirs list that tool handlers should see
// in their per-tool path check (resolvePathInAllowed). When
// --disable-automatic-directory-access is not set, we override the configured list
// with ["/"] so the isUnderAny/pathHasAncestor check trivially passes for any
// absolute path — matching the user-facing semantics of "the flag really does
// mean access anywhere, no questions, no per-tool rejection". The session-
// level gate in detectOutsideDirs is already bypassed by the flag; this is
// the second (per-handler) layer.
func (s *Session) toolAllowedDirs() []string {
	if s.enableAutomaticDirectoryAccess {
		return []string{"/"}
	}
	dirs := s.getAllowedDirs()
	if home := userHomeDir(); home != "" {
		dirs = append(dirs, home)
	}
	return dirs
}

// addApprovedWriteFile records a file path as approved for write-class operations.
func (s *Session) addApprovedWriteFile(absPath string) {
	s.approvedWriteFilesMu.Lock()
	defer s.approvedWriteFilesMu.Unlock()
	if s.approvedWriteFiles == nil {
		s.approvedWriteFiles = make(map[string]bool)
	}
	s.approvedWriteFiles[absPath] = true
}

// isWriteApproved reports whether the given absolute path has been approved for writes.
func (s *Session) isWriteApproved(absPath string) bool {
	s.approvedWriteFilesMu.RLock()
	defer s.approvedWriteFilesMu.RUnlock()
	return s.approvedWriteFiles[absPath]
}

// persistAllowedDirs saves directories to the project settings.json.
func (s *Session) persistAllowedDirs(dirs []string) {
	configPath := s.paths.ProjectSettingsWrite()
	if err := PersistAllowedDirectory(configPath, dirs); err != nil {
		log.Printf("[session] failed to persist allowed directories: %v", err)
	}
}

// emit sends an event to the client.
func (s *Session) emit(eventType string, data any) {
	select {
	case s.eventChan <- protocol.SessionEvent{Type: eventType, Data: data}:
	case <-s.ctx.Done():
	}
}

// emitIfVisible routes eventType through s.emit unless silent is true and
// the event is user-facing (stream/thinking/tool/workflow-step). Completion,
// error, retry, and confirmation events always pass through regardless of
// silent. Used by workflow-step code paths to honour per-step `silent: true`
// without bleeding into concurrent non-silent steps (hence step-scoped, not
// session-scoped).
func (s *Session) emitIfVisible(silent bool, eventType string, data any) {
	if silent && isUserFacingEvent(eventType) {
		return
	}
	s.emit(eventType, data)
}

// isUserFacingEvent returns true for event types that a silent workflow step
// should drop. Other events (errors, retries, stream_done, agent_done,
// workflow_complete, confirm_request, etc.) always pass through.
func isUserFacingEvent(t string) bool {
	switch t {
	case "event.stream_chunk",
		"event.thinking_chunk",
		"event.tool_call",
		"event.tool_result",
		"event.workflow_step_start",
		"event.workflow_step_done":
		return true
	}
	return false
}

// silentCtxKey scopes the silent flag to a context, so package-level code
// paths (tool dispatcher, bash streamer) can suppress developer-facing
// log.Printf noise during a silent step without a reference to *Session.
type silentCtxKey struct{}

func withSilentCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, silentCtxKey{}, true)
}

func isSilentCtx(ctx context.Context) bool {
	v, _ := ctx.Value(silentCtxKey{}).(bool)
	return v
}

// silentHooks wraps s.emitHooks() so that user-facing events are dropped.
// Errors, token accounting (stream_done), and cancellation hooks still fire.
func (s *Session) silentHooks() *TurnHooks {
	base := s.emitHooks()
	return &TurnHooks{
		OnStreamDelta:   func(string) {},
		OnThinkingDelta: func(delta string) { s.appendThinking(delta) }, // still logged to file
		OnStreamDone:    base.OnStreamDone,
		OnToolCall:      func(protocol.EventToolCall) {},
		OnToolResult: func(toolID, name string, input map[string]any, output string, isError bool) {
			// Surface tool errors even in silent mode — never swallow failures.
			if isError {
				base.OnToolResult(toolID, name, input, output, isError)
			}
		},
		OnBeforeStream: base.OnBeforeStream,
		// Retries are always visible, even in silent steps — otherwise a
		// silent step that fails 10 times looks identical to one that ran
		// in 120s, making triage impossible.
		OnRetry:         base.OnRetry,
		OnThinkingStall: base.OnThinkingStall,
	}
}

// hooksForStep returns emitHooks() or silentHooks() depending on silent.
func (s *Session) hooksForStep(silent bool) *TurnHooks {
	if silent {
		return s.silentHooks()
	}
	return s.emitHooks()
}

// emitHooks returns a TurnHooks wired to s.emit() for streaming events to the UI.
func (s *Session) emitHooks() *TurnHooks {
	return &TurnHooks{
		OnStreamDelta: func(delta string) {
			s.emit("event.stream_chunk", protocol.EventStreamChunk{Text: delta})
		},
		OnThinkingDelta: func(delta string) {
			s.appendThinking(delta)
			s.emit("event.thinking_chunk", protocol.EventThinkingChunk{Text: delta})
		},
		OnStreamDone: func(inputTokens, outputTokens, cacheCreation, cacheRead, elapsedMs int64) {
			s.thinkingBoundary()
			s.emit("event.stream_done", protocol.EventStreamDone{
				InputTokens:         inputTokens,
				OutputTokens:        outputTokens,
				CacheCreationTokens: cacheCreation,
				CacheReadTokens:     cacheRead,
				ElapsedMs:           elapsedMs,
			})
		},
		OnToolCall: func(ev protocol.EventToolCall) {
			s.emit("event.tool_call", ev)
		},
		OnToolResult: func(toolID, name string, input map[string]any, output string, isError bool) {
			s.emitToolResult(toolID, name, input, output, isError, 0)
		},
		OnBeforeStream: func(cancel context.CancelFunc) {
			s.cancelStream = cancel
		},
		OnRetry: func(attempt, maxRetries, waitSecs int, reason string) {
			s.emit("event.retry", protocol.EventRetry{
				Attempt:    attempt,
				MaxRetries: maxRetries,
				WaitSecs:   waitSecs,
				Reason:     reason,
			})
		},
		OnThinkingStall: func(elapsedMs int64, summaryChars int) {
			s.emit("event.thinking_stall", protocol.EventThinkingStall{
				ElapsedMs:    elapsedMs,
				SummaryChars: summaryChars,
			})
		},
	}
}

// emitToolResult emits an event.tool_result, enriching it with diff detail for edit_file.
func (s *Session) emitToolResult(toolID, name string, input map[string]any, output string, isError bool, lineOffset int) {
	ev := protocol.EventToolResult{
		ToolID: toolID, Name: name, Output: output, IsError: isError,
	}
	switch name {
	case "edit_file", "edit_minified_file":
		if !isError && input != nil {
			oldStr, _ := input["old_string"].(string)
			newStr, _ := input["new_string"].(string)
			if oldStr != "" || newStr != "" {
				ev.Detail = FormatEditDiff(oldStr, newStr, lineOffset)
			}
		}
	case "write_file", "write_minified_file":
		if !isError && input != nil {
			pathStr, _ := input["path"].(string)
			if resolvedPath, err := resolvePathInAllowed(s.cwd, s.toolAllowedDirs(), pathStr); err == nil {
				ev.Detail = formatWritePreview(resolvedPath)
			}
		}
	case "tool_orchestrator":
		ev.Output = ""
	case "web_fetch":
		if !isError {
			elapsedMs, _ := input["_elapsed_ms"].(int64)
			ev.Detail = fmt.Sprintf("%d", elapsedMs)
		}
	}
	s.emit("event.tool_result", ev)
}

// buildConfirmDetail returns a Detail string in the same format as EventToolResult.Detail,
// computed from the tool parameters before execution (since the file may not exist yet).
// This lets the UI reuse the exact same rendering path for both confirmed and non-confirmed tools.
func buildConfirmDetail(cwd, name string, input map[string]any) string {
	switch name {
	case "write_file", "write_minified_file":
		pathStr, _ := input["path"].(string)
		content, _ := input["content"].(string)
		lang := ""
		if pathStr != "" {
			lang = extToLang(filepath.Ext(pathStr))
		}
		lines := strings.Split(content, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		n := len(lines)
		truncated := n > 10
		if truncated {
			n = 10
		}
		var b strings.Builder
		b.WriteString("```" + lang + "\n")
		for _, line := range lines[:n] {
			b.WriteString(line + "\n")
		}
		if truncated {
			b.WriteString("// ...\n")
		}
		b.WriteString("```")
		return b.String()
	case "edit_file", "edit_minified_file":
		oldStr, _ := input["old_string"].(string)
		newStr, _ := input["new_string"].(string)
		if oldStr != "" || newStr != "" {
			return FormatEditDiff(oldStr, newStr, 0)
		}
	}
	return ""
}

// drainWorkflowMsg returns a pending user message if one has been enqueued via
// session.workflow_message, or "" if there is none. Non-blocking.
func (s *Session) drainWorkflowMsg() string {
	select {
	case msg := <-s.workflowMsgChan:
		return msg
	default:
		return ""
	}
}

// waitForCommand blocks until a command of the specified type is received, or ctx is cancelled.
func (s *Session) waitForCommand(ctx context.Context, types ...string) (protocol.SessionCommand, bool) {
	typeSet := make(map[string]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}

	for {
		select {
		case cmd := <-s.commandChan:
			if typeSet[cmd.Type] {
				return cmd, true
			}
			// Handle cancel at any time
			if cmd.Type == "session.cancel" {
				return cmd, false
			}
			// Ignore unmatched commands
		case <-ctx.Done():
			return protocol.SessionCommand{}, false
		}
	}
}

// Run is the main session loop. It initializes the brain, then waits for input.
func (s *Session) Run() {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			LogError("Session %s panic: %v\n%s", s.id, r, stack)
			telemetry.TrackPanic("session.Run", r, stack)
			s.emit("event.error", protocol.EventError{Message: fmt.Sprintf("session panic: %v", r)})
		}
		s.cancel()
		s.closeThinkingLog()
		if s.mcpPool != nil {
			s.mcpPool.Shutdown()
		}
	}()

	s.initBrain()

	// Attached (resumed) session: rebuild the client's viewport and apply
	// restore-time validation now that the model and workflows are resolved.
	s.emitReplay()

	for {
		select {
		case <-s.ctx.Done():
			return
		case cmd := <-s.commandChan:
			switch cmd.Type {
			case "session.input":
				s.lastRequestAt = time.Now()
				var data protocol.SessionInputData
				json.Unmarshal(cmd.Data, &data)
				s.handleInput(data.Text, data.Attachments)
			case "session.workflow":
				s.lastRequestAt = time.Now()
				var data protocol.SessionWorkflowData
				json.Unmarshal(cmd.Data, &data)
				s.handleWorkflowCommand(data.Name, data.Text)
			case "session.set_model":
				var data protocol.SessionSetModelData
				json.Unmarshal(cmd.Data, &data)
				if data.Model != "" {
					// Every model spec must carry an explicit provider
					// prefix (anthropic/, openai/, openrouter/, minimax/,
					// mimo/). The UI picker dispatches the Spec field, which
					// is already prefixed; bare names will fail in
					// llm.NewFromModel with a clear error.
					spec := data.Model
					// applyModel commits only on success, so a failed switch
					// leaves a previously-working session untouched.
					if err := s.applyModel(spec, 0); err != nil {
						s.emit("event.error", protocol.EventError{Message: fmt.Sprintf("Cannot switch to model %q: %v", spec, err)})
						log.Printf("[session] set_model failed for %s: %v", spec, err)
						continue
					}

					log.Printf("[session] model switched to %s (provider=%s)", spec, s.llm.Provider())

					// Persist the choice to the chat agent's frontmatter so
					// future sessions start with the same model. Best-effort:
					// log on failure rather than fail the (already-successful)
					// in-memory switch.
					if err := WriteChatAgentModel(s.paths, s.chatAgent, spec); err != nil {
						log.Printf("[session] WARN: failed to persist model choice to %s.md: %v", s.chatAgent, err)
					}
					s.persist()
				}
			case "session.trim":
				var data protocol.SessionTrimData
				json.Unmarshal(cmd.Data, &data)
				s.trimHistory(data.TurnIdx)
				s.persist()
			case "session.close":
				s.closedByUser = true
				return
			}
		}
	}
}

// applyModel resolves spec → LLM client and, on success, swaps in the client,
// updates s.model, and clears configErr. On failure it mutates no session state
// and returns the error for the caller to handle (initBrain records it as the
// unconfigured state; set_model keeps the prior working client). It never
// fabricates a client, so a missing credential can't leak into an LLM request.
func (s *Session) applyModel(spec string, maxTokens int64) error {
	client, err := llm.NewFromModel(spec, s.server.pluginConfig, llm.DefaultEffortFromSpec(spec), maxTokens)
	if err != nil {
		return err
	}
	s.llm = client
	s.model = spec
	s.configErr = nil
	return nil
}

// unconfiguredMessage renders the user-facing error for the session's current
// configErr. Missing credentials get a friendly, actionable message keyed by
// the model's display name; any other construction failure shows the raw error.
func (s *Session) unconfiguredMessage() string {
	if errors.Is(s.configErr, llm.ErrNoCredential) {
		name := providers.Default().DisplayName(s.model)
		return fmt.Sprintf("There are no credentials set to access %s. Go to Models (F3) to set your credentials.", name)
	}
	return s.configErr.Error()
}

// initBrain ensures the brain index exists (running brain.init if needed),
// then loads memory, custom agents, and workflows.
func (s *Session) initBrain() {
	s.emit("event.init_state", protocol.EventInitState{State: int(protocol.InitInProgress)})

	// Load skills and advertise them to the UI up front, before the
	// potentially slow brain.init below. Skill scanning only reads the
	// .vix/skills/ directories and has no dependency on the brain/LSP, so
	// emitting event.skills_available here lets the slash menu autocomplete
	// custom skills immediately instead of waiting for LSP initialization.
	// Pass layers highest-precedence-first (reverse of the "later wins" order
	// used for merged config): project before home.
	skillDirs := s.paths.Skills()
	reversed := make([]string, len(skillDirs))
	for i, d := range skillDirs {
		reversed[len(skillDirs)-1-i] = d
	}
	s.skills = agent.LoadSkills(reversed...)
	if s.skills.Count() > 0 {
		log.Printf("[session] loaded %d skill(s)", s.skills.Count())
	}
	s.emit("event.skills_available", protocol.EventSkillsAvailable{
		Skills: s.skillInfoList(),
	})

	// Initialize language map and LSP pool via brain.init
	handler := s.server.GetHandler("brain.init")
	if handler != nil {
		resp, err := handler(map[string]any{
			"params": map[string]any{
				"project_path":    s.cwd,
				"brain_dir":       s.paths.Brain(),
				"languages_paths": []string{s.paths.LanguagesFile()},
			},
		})
		if err != nil || resp["status"] != "ok" {
			log.Printf("[session] brain.init failed, continuing without LSP")
		}
	}

	// Load agents: layers are ordered so later entries override earlier.
	s.customAgents = make(map[string]SubagentConfig)
	for _, dir := range s.paths.Agents() {
		for k, v := range LoadCustomAgents(dir) {
			s.customAgents[k] = v
		}
	}

	if len(s.customAgents) > 0 {
		log.Printf("[session] loaded %d custom agent(s) from .vix/agents/", len(s.customAgents))
	}

	projectConfig := LoadProjectConfig(s.paths.Settings()...)
	s.projectConfig = projectConfig
	s.chatAgent = projectConfig.Agent
	s.setWorkflows(LoadWorkflowsFile(s.paths.WorkflowsFile()))

	// Rebuild the tool schema slice with the session-configured timeout
	// bounds so the LLM reads the real floor/cap in the bash and glob_files
	// descriptions instead of the package defaults wired in at NewSession
	// time (which ran before projectConfig was loaded).
	tDef, tMax := s.toolTimeoutBounds()
	s.tools = ToolSchemasWithBounds(tDef, tMax)

	// Seed allowed directories from config.
	if len(projectConfig.AllowedDirectories) > 0 {
		s.allowedDirsMu.Lock()
		s.allowedDirs = make(map[string]bool, len(projectConfig.AllowedDirectories))
		for _, dir := range projectConfig.AllowedDirectories {
			s.allowedDirs[dir] = true
		}
		s.allowedDirsMu.Unlock()
	}

	// Seed deny list (paths + urls) from config.
	if len(projectConfig.DenyPaths) > 0 || len(projectConfig.DenyURLs) > 0 {
		s.denyListMu.Lock()
		s.denyList = append([]string(nil), projectConfig.DenyPaths...)
		s.denyURLs = append([]string(nil), projectConfig.DenyURLs...)
		s.denyListMu.Unlock()
	}

	// Apply tool filtering AND model selection from the chat agent's
	// frontmatter (e.g. general.md). The chat agent's `model:` field is the
	// authoritative source for the session's model, falling back to the
	// daemon default (s.model) when the frontmatter omits it.
	agentFilePath := s.resolveAgentPath(s.chatAgent + ".md")
	modelSpec := s.model
	var modelMaxTokens int64
	if agentCfg, err := parseAgentFile(agentFilePath); err == nil {
		if len(agentCfg.Tools) > 0 {
			s.tools = FilterToolSchemasWithBounds(agentCfg.Tools, tDef, tMax)
			log.Printf("[session] chat agent tools from frontmatter: %v", agentCfg.Tools)
		}
		if agentCfg.Model != "" {
			modelSpec = agentCfg.Model
			modelMaxTokens = int64(agentCfg.MaxTokens)
		}
	}

	// Resolve the session's LLM client from the authoritative model spec. On
	// failure (e.g. no credential for the provider) the session enters the
	// unconfigured state: s.llm stays nil and the error surfaces to the UI on
	// the next input attempt, rather than fabricating a client that would leak
	// the missing credential into a doomed LLM request.
	if err := s.applyModel(modelSpec, modelMaxTokens); err != nil {
		s.configErr = err
		s.model = modelSpec
		s.llm = nil
		log.Printf("[session] WARN: no usable LLM client for %q (model=%q): %v — session unconfigured", s.chatAgent, modelSpec, err)
	} else {
		log.Printf("[session] chat agent model: %s (provider=%s)", s.model, s.llm.Provider())
	}

	if projectConfig.HasFeature(FeatureToolOrchestrator) {
		s.tools = FilterToolSchemasWithBounds([]string{
			"tool_orchestrator",
			"ask_question_to_user",
			"spawn_agent",
			"task_output",
		}, tDef, tMax)
		log.Printf("[session] tool_orchestrator feature enabled: %d tools exposed", len(s.tools))
	}
	if s.headless {
		s.tools = ExcludeTools(s.tools, "ask_question_to_user")
		log.Printf("[session] headless mode: removed ask_question_to_user from tools")
	}

	// Patch spawn_agent's description with the dynamically loaded agent list.
	// Must run AFTER FilterToolSchemas/ExcludeTools above, since those calls
	// rebuild s.tools from ToolSchemas() and would otherwise wipe the patch.
	PatchSpawnAgentDescription(s.tools, s.customAgents)

	// Initialise the MCP pool. This must come AFTER all tool-list filtering so
	// MCP tools are appended on top of whatever built-in set was selected for
	// this agent and are not wiped by FilterToolSchemas rebuilds above.
	if len(s.projectConfig.MCPServers) > 0 {
		// Filter out URL servers whose addresses appear in the session deny list.
		allowedServers := make([]mcp.ServerConfig, 0, len(s.projectConfig.MCPServers))
		for _, srv := range s.projectConfig.MCPServers {
			if srv.URL != "" {
				if denied, _ := isURLDenied(srv.URL, s.denyURLsSnapshot()); denied {
					log.Printf("[session] MCP server %q: URL in deny_list, skipping", srv.Name)
					continue
				}
			}
			allowedServers = append(allowedServers, srv)
		}
		if len(allowedServers) > 0 {
			pool := mcp.NewPool(s.ctx, allowedServers)
			s.mcpPool = pool
			s.tools = append(s.tools, pool.ToolSchemas()...)
			log.Printf("[session] MCP: %d server(s), %d tool(s) loaded",
				pool.ServerCount(), pool.ToolCount())
		}
	}

	if n := len(s.snapshotWorkflows()); n > 0 {
		log.Printf("[session] loaded %d workflow(s) from config", n)
	}

	// Expose the `skill` tool only when skills are loaded. Appended after all
	// tool-list filtering (like MCP tools) so it survives agent tool restrictions.
	if s.skills != nil && s.skills.Count() > 0 {
		s.tools = append(s.tools, SkillToolSchema())
	}
	log.Printf("[session] chat agent: %s", s.chatAgent)
	s.emit("event.init_state", protocol.EventInitState{State: int(protocol.InitDone), Model: s.model})
	s.emit("event.workflows_available", protocol.EventWorkflowsAvailable{
		Workflows: s.workflowInfoList(),
	})
	if cur, latest, url, method := s.server.UpdateStatus(); latest != "" {
		s.emit("event.update_available", protocol.EventUpdateAvailable{
			Current: cur,
			Latest:  latest,
			URL:     url,
			Method:  method,
		})
	}
}

// skillInfoList returns the loaded skills as name/description pairs, sorted by
// name, for slash-command autocomplete in the UI.
func (s *Session) skillInfoList() []protocol.SkillInfo {
	if s.skills == nil {
		return nil
	}
	all := s.skills.All()
	infos := make([]protocol.SkillInfo, 0, len(all))
	for name, sk := range all {
		infos = append(infos, protocol.SkillInfo{Name: name, Description: sk.Description})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// workflowInfoList returns the list of WorkflowInfo in config order.
func (s *Session) workflowInfoList() []protocol.WorkflowInfo {
	wfs := s.snapshotWorkflows()
	if len(wfs) == 0 {
		return nil
	}
	infos := make([]protocol.WorkflowInfo, len(wfs))
	for i, wf := range wfs {
		infos[i] = protocol.WorkflowInfo{Name: wf.Name}
	}
	return infos
}

// snapshotWorkflows returns the current workflow slice under the read lock.
func (s *Session) snapshotWorkflows() []*WorkflowDef {
	s.workflowsMu.RLock()
	defer s.workflowsMu.RUnlock()
	return s.workflows
}

// setWorkflows swaps the workflow slice under the write lock.
func (s *Session) setWorkflows(wfs []*WorkflowDef) {
	s.workflowsMu.Lock()
	s.workflows = wfs
	s.workflowsMu.Unlock()
}

// ReloadWorkflows swaps in a freshly-loaded workflow list and re-emits
// event.workflows_available so the TUI refreshes its slash menu and Shift+Tab
// cycle live. Called by the daemon config watcher when config/workflow.json
// changes on disk. A workflow already mid-execution holds its own definition,
// so this only affects the list of *available* workflows.
func (s *Session) ReloadWorkflows(wfs []*WorkflowDef) {
	s.setWorkflows(wfs)
	s.emit("event.workflows_available", protocol.EventWorkflowsAvailable{
		Workflows: s.workflowInfoList(),
	})
}

// brainDir returns the path to the brain index directory.
func (s *Session) brainDir() string {
	return s.paths.Brain()
}

// searchDirsSlice returns the layered search directories in precedence order
// (highest first) for use by prompt resolvers.
func (s *Session) searchDirsSlice() []string {
	layers := s.paths.Layers()
	out := make([]string, len(layers))
	for i, d := range layers {
		out[len(layers)-1-i] = d
	}
	return out
}

// searchDirs returns the layered search directories as a single
// prompt.JoinSearchDirs string (highest precedence first).
func (s *Session) searchDirs() string {
	return prompt.JoinSearchDirs(s.searchDirsSlice()...)
}

// resolveAgentPath searches the agent directories in reverse precedence order
// so that higher-precedence layers (project, or override) are checked first.
func (s *Session) resolveAgentPath(filename string) string {
	dirs := s.paths.Agents()
	// Search from highest precedence (last entry) to lowest.
	for i := len(dirs) - 1; i >= 0; i-- {
		candidate := filepath.Join(dirs[i], filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Fallback: highest-precedence path (will error naturally if absent).
	if len(dirs) > 0 {
		return filepath.Join(dirs[len(dirs)-1], filename)
	}
	return filename
}

// instructionFile holds a discovered instruction file path and its content.
type instructionFile struct {
	Path    string
	Content string
}

// discoverInstructionFiles finds CLAUDE.md and AGENTS.md files based on feature flags.
func (s *Session) discoverInstructionFiles() []instructionFile {
	var files []instructionFile

	if s.projectConfig.HasFeature(FeatureReadClaudeMD) {
		for _, path := range s.paths.ClaudeMD() {
			if data, err := os.ReadFile(path); err == nil {
				files = append(files, instructionFile{Path: path, Content: string(data)})
			}
		}
	}

	if s.projectConfig.HasFeature(FeatureReadAgentsMD) {
		path := filepath.Join(s.cwd, "AGENTS.md")
		if data, err := os.ReadFile(path); err == nil {
			files = append(files, instructionFile{Path: path, Content: string(data)})
		}
	}

	return files
}

func (s *Session) buildSystemPrompt() []llm.SystemBlock {
	var blocks []llm.SystemBlock

	// Load base system prompt from template
	funcs := map[string]func() string{
		"frequently_accessed_files": s.frequentlyAccessedFilesText,
	}
	agentFile := s.resolveAgentPath(s.chatAgent + ".md")
	basePrompt := prompt.GetLoader().Load(agentFile, map[string]string{
		"working_directory": s.cwd,
	}, s.searchDirs(), funcs)

	blocks = append(blocks, llm.SystemBlock{Text: basePrompt})

	// Inject frequently accessed files
	if filesText := s.frequentlyAccessedFilesText(); filesText != "" {
		blocks = append(blocks, llm.SystemBlock{Text: filesText})
	}

	// Inject project instruction files (CLAUDE.md, AGENTS.md)
	if instrFiles := s.discoverInstructionFiles(); len(instrFiles) > 0 {
		for _, f := range instrFiles {
			text := fmt.Sprintf("<system-reminder>\nContents of %s (project instructions):\n\n%s\n</system-reminder>", f.Path, f.Content)
			blocks = append(blocks, llm.SystemBlock{Text: text})
		}
		log.Printf("[session] loaded %d instruction file(s)", len(instrFiles))
	}

	// Inject available-skills metadata (level 1 of progressive disclosure):
	// just names + descriptions, so the model knows what it can load via the
	// `skill` tool without paying for the full bodies up front.
	if s.skills != nil && s.skills.Count() > 0 {
		if block := s.skills.FormatForSystemPrompt(); block != "" {
			blocks = append(blocks, llm.SystemBlock{Text: block})
		}
	}

	return blocks
}

// AddUserMessage appends a user message to the conversation, optionally with image attachments.
func (s *Session) AddUserMessage(text string, attachments ...protocol.Attachment) {
	var contentBlocks []llm.ContentBlock

	// Build text with image references
	textContent := text
	if len(attachments) > 0 {
		var refs strings.Builder
		for _, att := range attachments {
			refs.WriteString(fmt.Sprintf("[Image: %s]\n", att.Path))
		}
		if text == "" {
			textContent = "[Image attachment]"
		} else {
			textContent = refs.String() + "\n" + text
		}
	}
	contentBlocks = append(contentBlocks, llm.NewTextBlock(textContent))

	// Add image blocks
	for _, att := range attachments {
		contentBlocks = append(contentBlocks, llm.NewImageBlock(att.MediaType, att.Data))
	}

	s.messages = append(s.messages, llm.NewUserMessage(contentBlocks...))
}

// snapshotMessagesForFork returns a copy of the conversation history at the
// end of the turn identified by turnIdx (0-based). Returns nil if turnIdx is
// out of range. Safe to call from any goroutine.
func (s *Session) snapshotMessagesForFork(turnIdx int) []llm.MessageParam {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnIdx < 0 || turnIdx >= len(s.turnSnapshots) {
		return nil
	}
	return s.turnSnapshots[turnIdx]
}

// trimHistory replaces s.messages with the snapshot at turnIdx and discards
// all later snapshots. Out-of-range turnIdx is a no-op. Safe to call from any
// goroutine.
func (s *Session) trimHistory(turnIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnIdx < 0 || turnIdx >= len(s.turnSnapshots) {
		return
	}
	snap := s.turnSnapshots[turnIdx]
	s.messages = make([]llm.MessageParam, len(snap))
	copy(s.messages, snap)
	s.turnSnapshots = s.turnSnapshots[:turnIdx+1]
	log.Printf("[session] history trimmed to turn %d (%d messages)", turnIdx, len(s.messages))
}

// frequentlyAccessedFilesText returns a markdown-formatted string of the top 10
// frequently accessed files, or an empty string if none are available.
func (s *Session) frequentlyAccessedFilesText() string {
	resp, err := doGetTopFiles(s.server, map[string]any{"count": 10})
	if err != nil {
		return ""
	}
	resultMap, _ := resp["data"].(map[string]any)
	if resultMap == nil {
		return ""
	}

	filesInterface, ok := resultMap["files"]
	if !ok {
		return ""
	}

	files, ok := filesInterface.([]any)
	if !ok || len(files) == 0 {
		return ""
	}

	log.Printf("Injecting %d frequently accessed files into system prompt", len(files))

	var filesContent strings.Builder
	filesContent.WriteString("\n\n# Frequently Accessed Files\n\n")

	for _, fileInterface := range files {
		if fileMap, ok := fileInterface.(map[string]any); ok {
			path, _ := fileMap["path"].(string)
			content, _ := fileMap["content"].(string)
			filesContent.WriteString(fmt.Sprintf("## %s\n```\n%s\n```\n\n", path, content))
		}
	}

	return filesContent.String()
}

// writeClassTools is the set of tool names that mutate files and require
// confirmation when --disable-automatic-write-permission is set.
var writeClassTools = map[string]bool{
	"write_file":          true,
	"write_minified_file": true,
	"edit_file":           true,
	"edit_minified_file":  true,
	"delete_file":         true,
}

// filePathTools is the set of tools that take a "path" parameter and
// access the filesystem. Used for directory-access pre-checks.
var filePathTools = map[string]bool{
	"read_file":           true,
	"read_minified_file":  true,
	"write_file":          true,
	"write_minified_file": true,
	"edit_file":           true,
	"edit_minified_file":  true,
	"delete_file":         true,
	"grep":                true,
	"glob_files":          true,
}

// isOutsideWorkdir reports whether the resolved absolute path lies outside cwd.
func isOutsideWorkdir(cwd, resolvedPath string) bool {
	if resolvedPath == "" {
		return false
	}
	return resolvedPath != cwd &&
		!strings.HasPrefix(resolvedPath, cwd+string(filepath.Separator))
}

// detectOutsideDirs returns the directories a tool call wants to touch that
// lie outside cwd and the session's allowed set. Returns nil when access is
// permitted. The returned slice is suitable for use as `_requested_dirs`.
//
// The bash tool intentionally has no pre-flight: parsing a shell command
// string to predict which paths it will touch is unreliable (background
// jobs, env-sourced paths, multi-statement scripts all confuse the
// scanner). Landlock enforces the real boundary at syscall time, so the
// pre-flight only added false-positive denials without raising the
// security floor. File-IO tools (read_file, write_file, edit_file,
// grep, glob_files) still need the check because they execute inside
// vixd, which is not Landlock-sandboxed.
func (s *Session) detectOutsideDirs(name string, params map[string]any) []string {
	if filePathTools[name] {
		path, _ := params["path"].(string)
		if path != "" && filepath.IsAbs(path) {
			absPath := filepath.Clean(path)
			if !s.isPathAllowed(absPath) {
				return []string{filepath.Dir(absPath)}
			}
		}
	}
	return nil
}

// denyOutsideDirsError returns the headless-mode error message for a blocked
// directory access attempt.
func denyOutsideDirsError(name string, dirs []string) *ToolResult {
	if name == "bash" {
		return &ToolResult{Output: fmt.Sprintf("Access denied: paths outside working directory: %v", dirs), IsError: true}
	}
	return &ToolResult{Output: fmt.Sprintf("Access denied: path outside working directory: %s", dirs[0]), IsError: true}
}

// promptDirAccess emits a confirm_request for directory access and waits for
// the user's response. On approval, it adds the dirs to the session's allowed
// set (and persists them if the user chose "remember").
func (s *Session) promptDirAccess(ctx context.Context, name string, params map[string]any, dirs []string) (approved, cancelled bool) {
	s.emit("event.confirm_request", protocol.EventConfirmRequest{
		ToolName:      name,
		Params:        snapshotInput(params),
		RequestedDirs: dirs,
		Detail:        buildConfirmDetail(s.cwd, name, params),
	})
	cmd, ok := s.waitForCommand(ctx, "session.confirm")
	if !ok {
		return false, true
	}
	var confirmData protocol.SessionConfirmData
	json.Unmarshal(cmd.Data, &confirmData)
	if confirmData.Approved {
		for _, dir := range dirs {
			s.addAllowedDir(dir)
		}
		if confirmData.PersistDirs {
			s.persistAllowedDirs(dirs)
		}
	}
	return confirmData.Approved, false
}

// executeToolDirect calls a tool handler directly (in-process, no socket).
func (s *Session) executeToolDirect(ctx context.Context, name string, params map[string]any) *ToolResult {
	// Deny list: reject any tool call that targets a path or URL listed in
	// deny_list (or bash commands that reference such a path/URL). Runs
	// before every other gate so denials never surface as confirmation
	// prompts to the user.
	if blocked := checkDenyList(name, params, s.cwd, s.denyListSnapshot(), s.denyURLsSnapshot()); blocked != nil {
		return blocked
	}
	// Read-before-edit gate: reject patches to files the session has never
	// read. Runs before the confirmation check so the user is never asked
	// to approve a call that is about to be rejected anyway.
	if blocked := s.enforceReadGate(name, params); blocked != nil {
		return blocked
	}

	// Schema validation: reject malformed tool calls (missing required field,
	// wrong-typed field) before any handler runs, so the model gets a clear
	// error instead of a handler silently proceeding with a zero value.
	if err := validateToolInput(name, params); err != nil {
		return &ToolResult{Output: err.Error(), IsError: true}
	}

	// If the session has automatic write permission disabled, intercept write-class
	// tools and request user confirmation before executing them.
	if !s.enableAutomaticWritePermission && writeClassTools[name] {
		confirmed, _ := params["confirmed"].(bool)
		autoApproved := false
		if !confirmed {
			if pathStr, ok := params["path"].(string); ok {
				if resolved, err := resolvePathInAllowed(s.cwd, s.toolAllowedDirs(), pathStr); err == nil {
					autoApproved = s.isWriteApproved(resolved)
				}
			}
		}
		if !confirmed && !autoApproved {
			return &ToolResult{NeedsConfirmation: true, ToolName: name, Params: params}
		}
	}

	// Clone params and add session context
	p := make(map[string]any, len(params)+2)
	for k, v := range params {
		p[k] = v
	}
	p["cwd"] = s.cwd
	p["allowed_dirs"] = s.toolAllowedDirs()
	p["headless"] = s.headless
	// Internal: typed pointer to the Session, consumed by the bash handler to
	// reach bashJobs for `background: true`. Underscore-prefixed so it's
	// obviously not a model-provided field; never serialized back to the LLM.
	p["_session"] = s

	// Directory access pre-check: if the tool wants to touch paths outside cwd,
	// either auto-approve (when --disable-automatic-directory-access is not set),
	// deny (headless), or request confirmation via the dispatcher flow.
	if dirs := s.detectOutsideDirs(name, params); len(dirs) > 0 && !s.enableAutomaticDirectoryAccess {
		if s.headless {
			return denyOutsideDirsError(name, dirs)
		}
		params["_requested_dirs"] = dirs
		return &ToolResult{NeedsConfirmation: true, ToolName: name, Params: params}
	}

	// Route the `skill` tool inline: it needs the session's skill registry,
	// which server-level handlers can't reach. Returns the skill body plus a
	// listing of bundled files (progressive disclosure levels 2 and 3).
	if name == "skill" {
		skillName, _ := params["name"].(string)
		if skillName == "" {
			return &ToolResult{Output: "skill: missing required field \"name\"", IsError: true}
		}
		sk := s.skills.Get(skillName)
		if sk == nil {
			return &ToolResult{Output: fmt.Sprintf("skill: no skill named %q", skillName), IsError: true}
		}
		args, _ := params["arguments"].(string)
		return &ToolResult{Output: sk.LoadForTool(args)}
	}

	// Route MCP tools directly to the session's MCP pool.
	if strings.HasPrefix(name, "mcp__") {
		if s.mcpPool == nil {
			return &ToolResult{Output: "MCP is not initialised for this session (no mcp_servers configured)", IsError: true}
		}
		// Honour require_confirmation: true on the server config.
		if s.mcpPool.RequiresConfirmation(name) {
			confirmed, _ := params["confirmed"].(bool)
			if !confirmed {
				return &ToolResult{NeedsConfirmation: true, ToolName: name, Params: params}
			}
		}
		output, isError, err := s.mcpPool.Call(name, p)
		if err != nil {
			return &ToolResult{Output: fmt.Sprintf("MCP error: %v", err), IsError: true}
		}
		return &ToolResult{Output: output, IsError: isError}
	}

	handler := s.server.GetHandler("tool." + name)
	if handler == nil {
		return &ToolResult{Output: fmt.Sprintf("unknown tool: %s", name), IsError: true}
	}

	// Global tool timeout: every tool is capped at 120s by default. Only
	// bash and glob_files can raise that ceiling via a `timeout` param,
	// and even then the hard cap comes from settings.json `tool_timeouts`
	// (falling back to 600s / 10 minutes).
	def, maxv := s.toolTimeoutBounds()
	timeout := resolveToolTimeout(name, p, def, maxv)
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := handler(map[string]any{
		"command": "tool." + name,
		"params":  p,
		"ctx":     toolCtx,
	})
	if err != nil {
		return &ToolResult{Output: err.Error(), IsError: true}
	}
	if toolCtx.Err() == context.DeadlineExceeded {
		return &ToolResult{
			Output:  fmt.Sprintf("Error: tool timed out after %d seconds", int(timeout/time.Second)),
			IsError: true,
		}
	}

	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return &ToolResult{Output: fmt.Sprintf("Tool error: %s", msg), IsError: true}
	}

	data, _ := resp["data"].(map[string]any)

	// Check if tool requests confirmation
	if confirm, ok := data["confirm"].(bool); ok && confirm {
		return &ToolResult{
			NeedsConfirmation: true,
			ToolName:          name,
			Params:            params,
		}
	}

	output, _ := data["output"].(string)
	isError, _ := data["is_error"].(bool)
	lineOffsetF, _ := data["line_offset"].(float64)
	if elapsedMs, ok := data["elapsed_ms"].(int64); ok {
		params["_elapsed_ms"] = elapsedMs
	}
	s.maybeMarkRead(name, params, isError)
	return &ToolResult{Output: output, IsError: isError, LineOffset: int(lineOffsetF)}
}

// resolveToolTimeout returns the wall-clock ceiling that executeToolDirect /
// executeToolConfirmed should apply to a tool call.
//
// The rule is: every tool is capped at defaultTimeout by default. bash and
// glob_files are the only tools that can raise that ceiling via a `timeout`
// param, and even they are hard-capped at maxTimeout. There is no mode
// (including headless) in which a tool is allowed to exceed maxTimeout — if a
// probe genuinely needs more than that it must be restructured.
//
// The defaultTimeout and maxTimeout bounds come from ProjectConfig.ToolTimeouts,
// which is populated from the `tool_timeouts` block in settings.json and falls
// back to the package-level defaultToolTimeoutDefault/defaultToolTimeoutMax
// constants (120s / 600s) when absent or invalid.
func resolveToolTimeout(name string, params map[string]any, defaultTimeout, maxTimeout time.Duration) time.Duration {
	if name != "bash" && name != "glob_files" {
		return defaultTimeout
	}
	var requested int
	switch v := params["timeout"].(type) {
	case float64:
		requested = int(v)
	case int:
		requested = v
	case int64:
		requested = int(v)
	}
	if requested <= 0 {
		return defaultTimeout
	}
	d := time.Duration(requested) * time.Second
	if d > maxTimeout {
		return maxTimeout
	}
	if d < defaultTimeout {
		return defaultTimeout
	}
	return d
}

// toolTimeoutBounds returns the session's configured (default, max) tool
// timeouts as time.Duration, reading live from s.projectConfig on every call
// so a mid-session projectConfig mutation (if ever introduced) would not be
// silently cached. The bounds come from the `tool_timeouts` block in
// settings.json, with fallback to the package-level defaults.
func (s *Session) toolTimeoutBounds() (time.Duration, time.Duration) {
	return s.projectConfig.ToolTimeouts.Default, s.projectConfig.ToolTimeouts.Max
}

// executeToolConfirmed calls a tool handler with write-confirmation bypassed.
// Directory-access confirmation is still enforced: workflow and subagent tools
// must ask the user before touching paths outside cwd (unless headless, where
// we reject with an error).
func (s *Session) executeToolConfirmed(ctx context.Context, name string, params map[string]any) *ToolResult {
	// Session-level tools (todo list) live as Session methods, not registered
	// tool handlers. Subagents and workflow steps route through here, so
	// intercept before the GetHandler lookup — otherwise they'd get
	// "unknown tool: todo_read". The main-agent path handles these via
	// sessionDispatchToolCalls.handleSpecial and never reaches this function.
	switch name {
	case "todo_read":
		output, isErr := s.handleTodoRead(ctx, params)
		return &ToolResult{Output: output, IsError: isErr}
	case "todo_write":
		output, isErr := s.handleTodoWrite(ctx, params)
		return &ToolResult{Output: output, IsError: isErr}
	}

	// Read-before-edit gate: subagents and workflow agents share the parent
	// session's read set, so a subagent must also have read a file before
	// editing it (either directly, or inheriting from the parent's reads).
	if blocked := s.enforceReadGate(name, params); blocked != nil {
		return blocked
	}

	// Schema validation: same gate as executeToolDirect, covering the
	// subagent/workflow entry point. MCP tools have no local schema and are
	// passed through (validateToolInput returns nil for them).
	if err := validateToolInput(name, params); err != nil {
		return &ToolResult{Output: err.Error(), IsError: true}
	}

	if dirs := s.detectOutsideDirs(name, params); len(dirs) > 0 && !s.enableAutomaticDirectoryAccess {
		if s.headless {
			return denyOutsideDirsError(name, dirs)
		}
		approved, cancelled := s.promptDirAccess(ctx, name, params, dirs)
		if cancelled {
			return &ToolResult{Output: "Cancelled", IsError: true}
		}
		if !approved {
			return &ToolResult{Output: "Permission denied by user.", IsError: true}
		}
	}

	p := make(map[string]any, len(params)+2)
	for k, v := range params {
		p[k] = v
	}
	p["confirmed"] = true
	p["cwd"] = s.cwd
	p["allowed_dirs"] = s.toolAllowedDirs()
	p["headless"] = s.headless
	p["_session"] = s // see executeToolDirect for rationale

	// Route MCP tools directly to the session's MCP pool.
	if strings.HasPrefix(name, "mcp__") {
		if s.mcpPool == nil {
			return &ToolResult{Output: "MCP is not initialised for this session (no mcp_servers configured)", IsError: true}
		}
		output, isError, err := s.mcpPool.Call(name, p)
		if err != nil {
			return &ToolResult{Output: fmt.Sprintf("MCP error: %v", err), IsError: true}
		}
		return &ToolResult{Output: output, IsError: isError}
	}

	handler := s.server.GetHandler("tool." + name)
	if handler == nil {
		return &ToolResult{Output: fmt.Sprintf("unknown tool: %s", name), IsError: true}
	}

	def, maxv := s.toolTimeoutBounds()
	timeout := resolveToolTimeout(name, p, def, maxv)
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := handler(map[string]any{
		"command": "tool." + name,
		"params":  p,
		"ctx":     toolCtx,
	})
	if err != nil {
		return &ToolResult{Output: err.Error(), IsError: true}
	}
	if toolCtx.Err() == context.DeadlineExceeded {
		return &ToolResult{
			Output:  fmt.Sprintf("Error: tool timed out after %d seconds", int(timeout/time.Second)),
			IsError: true,
		}
	}

	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return &ToolResult{Output: fmt.Sprintf("Tool error: %s", msg), IsError: true}
	}

	data, _ := resp["data"].(map[string]any)
	output, _ := data["output"].(string)
	isError, _ := data["is_error"].(bool)
	lineOffsetF, _ := data["line_offset"].(float64)
	if elapsedMs, ok := data["elapsed_ms"].(int64); ok {
		params["_elapsed_ms"] = elapsedMs
	}
	s.maybeMarkRead(name, params, isError)
	return &ToolResult{Output: output, IsError: isError, LineOffset: int(lineOffsetF)}
}

const maxRetries = 10

// streamWithRetry calls StreamMessage with automatic retry and exponential
// backoff for transient errors (rate limits, server errors, network issues).
// Non-retryable errors (auth, bad request) fail immediately with a friendly message.
func (s *Session) streamWithRetry(
	system []llm.SystemBlock,
	onDelta func(string),
	onThinkingDelta func(string),
) (*llm.Message, time.Duration, error) {
	// Retry-scoped context: covers the whole loop including backoff sleeps.
	// `session.cancel` (from Escape) calls s.cancelStream → cancels retryCtx,
	// which (a) cancels the in-flight StreamMessage via the derived streamCtx
	// and (b) wakes the backoff select so we don't keep retrying after Escape.
	retryCtx, retryCancel := context.WithCancel(s.ctx)
	s.cancelStream = retryCancel
	defer retryCancel()

	turnID := newRequestID()
	var lastReason string
	// sawAnyStall is a sticky flag: once any attempt in this turn hits
	// a thinking stall, the final attempt runs with thinking disabled
	// regardless of what other errors intervene (idle timeouts, transient
	// API failures). Narrowly gating on "previous attempt was a stall"
	// let interleaved idle_timeouts silently waste the saved final shot.
	var sawAnyStall bool
	for attempt := range maxRetries {
		attemptCtx := withRequestID(retryCtx, fmt.Sprintf("%s.%d", turnID, attempt+1))
		streamCtx, streamCancel := context.WithCancel(attemptCtx)

		var streamOpts StreamOpts
		if attempt == maxRetries-1 && sawAnyStall {
			empty := ""
			streamOpts.EffortOverride = &empty
			log.Printf("\033[33m[session req=%s] final attempt — disabling extended thinking for this call\033[0m", turnID)
		}
		msg, elapsed, err := s.llm.StreamMessageWith(streamCtx, system, s.messages, s.tools, onDelta, onThinkingDelta, streamOpts)
		streamCancel()
		if err == nil {
			return msg, elapsed, nil
		}

		if errors.Is(err, context.Canceled) || retryCtx.Err() != nil {
			return nil, 0, context.Canceled
		}

		// Thinking stall: the model exceeded the per-block thinking budget.
		// Surface the summary back to the model as a nudge and retry in the
		// standard backoff loop (counts as one of the maxRetries attempts).
		// finalNext=true means the NEXT call will run with thinking disabled.
		finalNext := attempt == maxRetries-2
		if stallErr, nudge, ok := asThinkingStall(err, attempt+1, maxRetries, finalNext); ok {
			sawAnyStall = true
			s.emit("event.thinking_stall", protocol.EventThinkingStall{
				ElapsedMs:    stallErr.Elapsed.Milliseconds(),
				SummaryChars: len(stallErr.Summary),
			})
			s.messages = append(s.messages, nudge)
			log.Printf("\033[31m[session req=%s] thinking stall after %s (attempt %d/%d, nudging and retrying)\033[0m",
				turnID, stallErr.Elapsed, attempt+1, maxRetries)
			lastReason = "Thinking stall — nudging model"
			s.emit("event.stream_done", protocol.EventStreamDone{})
			CloseIdleHTTPConnections()
			select {
			case <-retryCtx.Done():
				return nil, 0, context.Canceled
			default:
			}
			continue
		}
		// Note: sawAnyStall intentionally NOT cleared on non-stall
		// errors — once any stall happens in this turn, the final
		// attempt should still run with thinking disabled.

		retryable, reason := classifyError(err)
		lastReason = reason
		if !retryable {
			log.Printf("\033[31m[session req=%s] API error: %s — %v\033[0m", turnID, reason, err)
			return nil, 0, fmt.Errorf("%s", reason)
		}
		log.Printf("\033[31m[session req=%s] API error (attempt %d/%d, retrying): %s — %v\033[0m", turnID, attempt+1, maxRetries, reason, err)

		// Flush any partial streaming content in the UI
		s.emit("event.stream_done", protocol.EventStreamDone{})

		// Calculate backoff: honour Retry-After when present, otherwise use
		// min(1s * 2^attempt, cap) + jitter. Rate-limit (429) responses use a
		// 300 s cap so subscription-tier accounts can wait out their window.
		var wait time.Duration
		var waitSecs int
		if ra := rateLimitRetryAfter(err); ra > 0 {
			wait = ra
			waitSecs = int(math.Ceil(ra.Seconds()))
		} else {
			backoffCap := 60.0
			if isRateLimitError(err) {
				backoffCap = 300.0
			}
			delaySec := math.Min(math.Pow(2, float64(attempt)), backoffCap)
			jitter := rand.Float64() * 0.5
			wait = time.Duration((delaySec + jitter) * float64(time.Second))
			waitSecs = int(math.Ceil(delaySec + jitter))
		}

		s.emit("event.retry", protocol.EventRetry{
			Attempt:    attempt + 1,
			MaxRetries: maxRetries,
			WaitSecs:   waitSecs,
			Reason:     reason,
		})

		// Drop pooled conns so the next attempt uses a fresh TCP
		// connection. See AgentRunner.Send for rationale.
		CloseIdleHTTPConnections()

		select {
		case <-time.After(wait):
		case <-retryCtx.Done():
			return nil, 0, context.Canceled
		}
	}
	return nil, 0, fmt.Errorf("API request failed after %d attempts: %s", maxRetries, lastReason)
}

// buildStallNudge composes the user-role message appended on a thinking
// stall. When summary is empty (e.g. display=omitted on a future model),
// falls back to a generic prompt so we never inject a "reasoning: <empty>".
//
// attempt/max give the model concrete deadline awareness ("attempt N of M").
// finalNext=true means the caller will disable extended thinking on the
// next request — the nudge warns the model accordingly so it doesn't try
// to open another thinking block and get confused when the API rejects it.
func buildStallNudge(elapsed time.Duration, summary string, attempt, max int, finalNext bool) string {
	summary = strings.TrimSpace(summary)
	var header string
	switch {
	case finalNext:
		header = fmt.Sprintf(
			"Vix detected a stall in extended thinking after %s. This was attempt %d of %d. "+
				"Your next response will be the FINAL attempt — vix is disabling extended thinking for it. "+
				"You must emit your answer or a tool call directly, without opening a thinking block. "+
				"If you stall again after that, vix will abort this step.",
			elapsed.Round(time.Second), attempt, max,
		)
	default:
		header = fmt.Sprintf(
			"Vix detected a stall in extended thinking after %s. This was attempt %d of %d. "+
				"If vix has to nudge you %d times, it will abort this step without retrying further.",
			elapsed.Round(time.Second), attempt, max, max,
		)
	}
	if summary == "" {
		return header + " Please conclude and answer now without further extended thinking."
	}
	return fmt.Sprintf(
		"%s\n\nYour reasoning so far (summarized):\n\n%s\n\n"+
			"Please conclude and answer now without further extended thinking.",
		header, summary,
	)
}

// asThinkingStall checks whether err is a ThinkingStallError and, if so,
// returns the concrete error plus the user-role nudge message to append
// to the conversation before the next attempt. Returns (nil, zero, false)
// for non-stall errors.
//
// attempt/max are the caller's loop variables (attempt is 1-indexed in the
// nudge text; pass attempt+1 when your loop counter starts at 0). finalNext
// signals that the caller will disable extended thinking on its next call
// — the nudge wording reflects that so the model doesn't reopen a thinking
// block it cannot use.
//
// Shared by session streamWithRetry, workflow step runner, and subagent
// turn loop so all three surfaces react to stalls the same way.
func asThinkingStall(err error, attempt, max int, finalNext bool) (*ThinkingStallError, llm.MessageParam, bool) {
	var stallErr *ThinkingStallError
	if !errors.As(err, &stallErr) {
		return nil, llm.MessageParam{}, false
	}
	nudge := llm.NewUserMessage(
		llm.NewTextBlock(buildStallNudge(stallErr.Elapsed, stallErr.Summary, attempt, max, finalNext)),
	)
	return stallErr, nudge, true
}

func (s *Session) handleInput(text string, attachments []protocol.Attachment) {
	if text == "/exit" {
		s.emit("event.quit", nil)
		return
	}

	if text == "/clear" {
		s.mu.Lock()
		s.messages = nil
		s.turnSnapshots = nil
		s.todoList = nil
		s.workflowRunState = nil
		s.mu.Unlock()
		s.persist()
		s.emit("event.clear", nil)
		s.emit("event.todo_list_updated", protocol.EventTodoListUpdated{Todos: []protocol.TodoItem{}})
		s.emit("event.agent_done", nil)
		return
	}

	// /skills — list all loaded skills
	if text == "/skills" {
		s.emit("event.stream_chunk", protocol.EventStreamChunk{Text: s.skills.FormatSkillsList()})
		s.emit("event.agent_done", nil)
		return
	}

	// Unconfigured session: no usable LLM client (e.g. no credential for the
	// selected model's provider). Surface the error to the UI without ever
	// contacting the LLM. Placed before /compact, which also needs the client.
	if s.configErr != nil {
		s.emit("event.error", protocol.EventError{Message: s.unconfiguredMessage()})
		s.emit("event.agent_done", nil)
		return
	}

	// /compact [N] — summarize older turns to free context. Handled daemon-side
	// because it mutates s.messages and needs an LLM call.
	if text == "/compact" || strings.HasPrefix(text, "/compact ") {
		n := 0
		if fields := strings.Fields(text); len(fields) > 1 {
			v, err := strconv.Atoi(fields[1])
			if err != nil || v < 1 {
				s.emit("event.error", protocol.EventError{Message: "Usage: /compact [N]  (N = turn number)"})
				s.emit("event.agent_done", nil)
				return
			}
			n = v
		}
		s.mu.Lock()
		keepFromMsgIdx, summarizedTurns, ok := s.resolveCompactionKeep(n)
		s.mu.Unlock()
		if !ok {
			s.emit("event.error", protocol.EventError{Message: "Nothing to compact (no earlier turns, or N out of range)."})
			s.emit("event.agent_done", nil)
			return
		}
		s.compactMessages(keepFromMsgIdx, summarizedTurns, false)
		s.emit("event.agent_done", nil)
		return
	}

	// /<skill-name> [args] — explicit skill invocation. If the leading slash
	// token names a loaded skill, render its body (with args substituted) and
	// use that as the turn's user message. This is the user-driven counterpart
	// to the model-driven `skill` tool.
	if strings.HasPrefix(text, "/") && s.skills != nil && s.skills.Count() > 0 {
		rest := strings.TrimPrefix(text, "/")
		name := rest
		args := ""
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			name = rest[:i]
			args = strings.TrimSpace(rest[i+1:])
		}
		if sk := s.skills.Get(name); sk != nil {
			text = sk.LoadForTool(args)
		}
	}

	// Validate attachments before adding
	for _, att := range attachments {
		if err := protocol.ValidateAttachment(att); err != nil {
			s.emit("event.error", protocol.EventError{Message: fmt.Sprintf("Invalid attachment: %v", err)})
			s.emit("event.agent_done", nil)
			return
		}
	}

	s.AddUserMessage(text, attachments...)

	// Inner loop: agent turns
	todoNudges := 0
	for {
		s.maybeAutoCompact()
		system := s.buildSystemPrompt()

		msg, elapsed, err := s.streamWithRetry(system, func(delta string) {
			s.emit("event.stream_chunk", protocol.EventStreamChunk{Text: delta})
		}, func(delta string) {
			s.appendThinking(delta)
			s.emit("event.thinking_chunk", protocol.EventThinkingChunk{Text: delta})
		})

		if err != nil {
			if errors.Is(err, context.Canceled) {
				s.emit("event.stream_done", protocol.EventStreamDone{})
				break
			}
			s.emit("event.error", protocol.EventError{Message: err.Error()})
			break
		}

		s.thinkingBoundary()
		s.emit("event.stream_done", protocol.EventStreamDone{
			InputTokens:         msg.Usage.InputTokens,
			OutputTokens:        msg.Usage.OutputTokens,
			CacheCreationTokens: msg.Usage.CacheCreationTokens,
			CacheReadTokens:     msg.Usage.CacheReadTokens,
			ElapsedMs:           elapsed.Milliseconds(),
		})
		s.accumulateTurnTelemetry(msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheCreationTokens, msg.Usage.CacheReadTokens, elapsed.Milliseconds(), countToolUses(msg))

		s.mu.Lock()
		s.lastInputTokens = msg.Usage.InputTokens + msg.Usage.CacheReadTokens + msg.Usage.CacheCreationTokens
		s.mu.Unlock()

		LogLLMCall(s.model, system, s.messages, s.tools, msg)
		s.messages = append(s.messages, msg.ToParam())

		if msg.StopReason == llm.StopEndTurn {
			log.Printf("\033[34m[session] end of turn detected\033[0m")
			if todoNudges < 3 && s.hasPendingTodos() {
				todoNudges++
				s.messages = append(s.messages, llm.NewUserMessage(
					llm.NewTextBlock("You still have pending or in-progress TODO items. Please either complete them or call todo_write with an empty list to clear the list before finishing."),
				))
				continue
			}
			break
		}

		if msg.StopReason == "tool_use" {
			streamCtx, streamCancel := context.WithCancel(s.ctx)
			s.cancelStream = streamCancel
			toolResults := s.sessionDispatchToolCalls(streamCtx, msg)
			cancelled := streamCtx.Err() != nil
			streamCancel()
			// Always append tool results — the API requires every tool_use
			// to have a matching tool_result, even on cancellation.
			if len(toolResults) > 0 {
				s.messages = append(s.messages, llm.NewUserMessage(toolResults...))
			}
			if cancelled {
				s.emit("event.stream_done", protocol.EventStreamDone{})
				break
			}
			continue
		}

		break
	}

	s.mu.Lock()
	snapshot := make([]llm.MessageParam, len(s.messages))
	copy(snapshot, s.messages)
	s.turnSnapshots = append(s.turnSnapshots, snapshot)
	s.mu.Unlock()
	s.persist()
	s.emit("event.agent_done", nil)
}

// compactionSystemPrompt instructs the model to produce a dense, faithful
// summary of the dropped conversation prefix during compaction.
const compactionSystemPrompt = `You are compacting a software-engineering conversation to save context.
Summarize the messages below into a dense, faithful briefing that lets the assistant continue the work without re-reading them.

Preserve, in this order:
1. The user's goals and any explicit instructions or constraints still in effect.
2. Key decisions made and their rationale.
3. Files created/edited/read (exact paths) and the gist of important changes.
4. Important tool outputs, command results, errors, and their resolutions.
5. Open tasks, TODOs, and unresolved questions.

Be concise but specific — keep identifiers, paths, and commands verbatim. Do not invent facts. Do not include a preamble or sign-off; output only the summary.`

// compactionSummaryPrefix labels the synthetic user message that replaces the
// compacted prefix so the assistant knows it is reading a summary.
const compactionSummaryPrefix = "[Summary of earlier conversation, compacted to save context]\n\n"

// compactionRequestPrompt is appended as a trailing user message before the
// summarization call. The dropped prefix always ends on an assistant message
// (turn boundaries land after the assistant's final response); sending it as-is
// makes the API treat it as an assistant prefill, which some models reject. The
// trailing user turn keeps the request valid and states the summarization ask
// explicitly.
const compactionRequestPrompt = "Summarize the conversation above following the instructions in the system prompt."

// resolveCompactionKeep maps a compaction policy to a message-index boundary
// (keepFromMsgIdx) and the count of turns being summarized. explicitN > 0 means
// /compact N (keep turns after N); otherwise the configured policy applies.
// Returns ok=false when there is nothing to compact. Must be called under s.mu.
func (s *Session) resolveCompactionKeep(explicitN int) (keepFromMsgIdx, summarizedTurns int, ok bool) {
	total := len(s.turnSnapshots)
	if total == 0 {
		return 0, 0, false
	}

	var dropTurns int
	switch {
	case explicitN > 0:
		if explicitN >= total {
			return 0, 0, false // keep "turns after N" — N must leave a tail
		}
		dropTurns = explicitN
	case s.projectConfig.Compaction.KeepLastNTurns > 0:
		keep := s.projectConfig.Compaction.KeepLastNTurns
		if keep >= total {
			return 0, 0, false
		}
		dropTurns = total - keep
	default: // ratio
		keep := int(math.Ceil(s.projectConfig.Compaction.KeepRatio * float64(total)))
		if keep < 1 {
			keep = 1
		}
		if keep >= total {
			return 0, 0, false
		}
		dropTurns = total - keep
	}

	// turnSnapshots[dropTurns-1] is the cumulative messages prefix through the
	// last dropped turn — its length is the keep boundary. Snapshots are taken
	// only at turn boundaries, so this never splits a tool_use/tool_result pair.
	keepFromMsgIdx = len(s.turnSnapshots[dropTurns-1])
	return keepFromMsgIdx, dropTurns, true
}

// maybeAutoCompact compacts the conversation when automatic compaction is
// enabled, the model's context window is known, and the last turn's prompt
// exceeded the configured threshold. Called at the top of the turn loop.
func (s *Session) maybeAutoCompact() {
	cfg := s.projectConfig.Compaction
	if !cfg.Auto {
		return
	}
	window := providers.Default().ContextWindow(s.model)
	if window <= 0 {
		return // unknown window → safe fallback, no auto-compaction
	}

	s.mu.Lock()
	if s.lastInputTokens < int64(cfg.Threshold*float64(window)) {
		s.mu.Unlock()
		return
	}
	keepFromMsgIdx, summarizedTurns, ok := s.resolveCompactionKeep(0)
	s.mu.Unlock()
	if !ok {
		// Threshold breached but a single trailing turn already fills the
		// window — nothing safe to compact. Warn once and continue.
		log.Printf("\033[33m[session] auto-compaction skipped: nothing to compact below threshold\033[0m")
		return
	}
	s.compactMessages(keepFromMsgIdx, summarizedTurns, true)
}

// compactMessages summarizes s.messages[:keepFromMsgIdx] and replaces the
// history with [summary, ...tail], rebuilding turnSnapshots to keep the
// retained turns' boundaries consistent. summarizedTurns is the number of turns
// folded into the summary. auto distinguishes the trigger source for the event.
func (s *Session) compactMessages(keepFromMsgIdx, summarizedTurns int, auto bool) {
	s.mu.Lock()
	if keepFromMsgIdx <= 0 || keepFromMsgIdx >= len(s.messages) {
		s.mu.Unlock()
		s.emit("event.error", protocol.EventError{Message: "Nothing to compact."})
		return
	}
	dropped := make([]llm.MessageParam, keepFromMsgIdx)
	copy(dropped, s.messages[:keepFromMsgIdx])
	fromTokens := s.lastInputTokens
	s.mu.Unlock()

	summary, err := s.summarizeMessages(dropped)
	if err != nil {
		s.emit("event.error", protocol.EventError{Message: "Compaction failed: " + err.Error()})
		return
	}

	summaryMsg := llm.NewUserMessage(llm.NewTextBlock(compactionSummaryPrefix + summary))

	s.mu.Lock()
	// History is only mutated by the single-threaded turn loop / command
	// handler, so keepFromMsgIdx is still valid here.
	if keepFromMsgIdx >= len(s.messages) {
		s.mu.Unlock()
		s.emit("event.error", protocol.EventError{Message: "Nothing to compact."})
		return
	}
	tail := s.messages[keepFromMsgIdx:]
	newMessages := make([]llm.MessageParam, 0, 1+len(tail))
	newMessages = append(newMessages, summaryMsg)
	newMessages = append(newMessages, tail...)

	// Rebuild turnSnapshots for the kept turns, re-based onto newMessages. The
	// summary occupies index 0, so a kept turn's old boundary len(s_j) maps to
	// 1 + (len(s_j) - keepFromMsgIdx).
	var newSnapshots [][]llm.MessageParam
	for j := summarizedTurns; j < len(s.turnSnapshots); j++ {
		boundary := 1 + (len(s.turnSnapshots[j]) - keepFromMsgIdx)
		if boundary < 1 {
			boundary = 1
		}
		if boundary > len(newMessages) {
			boundary = len(newMessages)
		}
		snap := make([]llm.MessageParam, boundary)
		copy(snap, newMessages[:boundary])
		newSnapshots = append(newSnapshots, snap)
	}

	s.messages = newMessages
	s.turnSnapshots = newSnapshots
	s.lastInputTokens = 0 // recomputed on the next turn; avoids immediate retrigger
	s.mu.Unlock()

	s.emit("event.compacted", protocol.EventCompacted{
		FromTokens:      fromTokens,
		SummarizedTurns: summarizedTurns,
		Auto:            auto,
	})
}

// summarizeMessages runs a one-shot, tool-free LLM call to summarize the given
// messages. Streaming deltas are discarded (no UI side-effects).
func (s *Session) summarizeMessages(msgs []llm.MessageParam) (string, error) {
	system := []llm.SystemBlock{{Text: compactionSystemPrompt}}
	// Ensure the conversation ends on a user message: the dropped prefix ends on
	// an assistant turn, which the API would otherwise treat as a prefill.
	msgs = append(msgs[:len(msgs):len(msgs)], llm.NewUserMessage(llm.NewTextBlock(compactionRequestPrompt)))
	msg, _, err := s.llm.StreamMessage(s.ctx, system, msgs, nil, func(string) {}, func(string) {})
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(msg.TextContent)
	if summary == "" {
		return "", fmt.Errorf("summarization produced no text")
	}
	return summary, nil
}

// interactiveTools are tools that require sequential execution (user interaction, blocking waits).
var interactiveTools = map[string]bool{
	"ask_question_to_user": true,
	"spawn_agent":          true,
	"task_output":          true,
	"tool_orchestrator":    true,
	"todo_write":           true,
	"todo_read":            true,
}

// writeTools are tools that mutate files — their presence forces sequential execution.
var writeTools = map[string]bool{
	"write_file":          true,
	"write_minified_file": true,
	"edit_file":           true,
	"edit_minified_file":  true,
	"delete_file":         true,
}

// toolTask holds parsed info for a single tool call in a batch.
type toolTask struct {
	toolUse llm.ToolCall
	input   map[string]any
	summary string
	reason  string
	// Bash-specific alternative-tool justifications (empty or "N/A" → omitted).
	bashReasonNotReadFile       string
	bashReasonNotEditFile       string
	bashReasonNotGlobFiles      string
	bashReasonToIncreaseTimeout string
	interactive                 bool
	result                      *ToolResult
	apiResult                   llm.ContentBlock
}

// dispatchOptions configures the unified tool dispatcher.
// All callback fields are optional (nil = disabled).
type dispatchOptions struct {
	// cwd is the working directory passed to executeTool.
	cwd string
	// executeTool runs the named tool with the given input. It must be non-nil.
	// The implementation is responsible for setting confirmed=true when needed.
	executeTool func(name string, input map[string]any) *ToolResult
	// handleSpecial handles tools that need session-level logic (ask_question_to_user,
	// spawn_agent, task_output). Returns (result, true) when it handles the tool,
	// (nil, false) when it doesn't recognise it.
	handleSpecial func(ctx context.Context, name string, input map[string]any) (*ToolResult, bool)
	// confirmFn is called when executeTool returns NeedsConfirmation=true.
	// Returns approved=true to proceed, or cancelled=true to abort.
	// When nil, NeedsConfirmation results are treated as denied.
	confirmFn func(ctx context.Context, name string, input map[string]any) (approved, cancelled bool)
	// emitToolCall is called once per tool, before execution.
	emitToolCall func(ev protocol.EventToolCall)
	// emitToolResult is called after each tool completes.
	emitToolResult func(toolID, name string, input map[string]any, output string, isError bool, lineOffset int)
	// toolTimeoutDefault is the floor for tool-call timeouts used by the
	// TimeoutSec field of emitted tool_call events. When zero, falls back to
	// the package-level defaultToolTimeoutDefault constant.
	toolTimeoutDefault time.Duration
	// toolTimeoutMax is the cap for tool-call timeouts. When zero, falls back
	// to the package-level defaultToolTimeoutMax constant.
	toolTimeoutMax time.Duration
}

// snapshotInput returns a shallow copy of m so the encoded event does not
// share the underlying map with the dispatch goroutine. The dispatch goroutine
// continues to read/write t.input after emit (executeToolDirect adds
// _requested_dirs, confirmFn deletes it after approval, resolveConfirmation
// reruns with confirmed=true, parallel tool execution iterates the map),
// while the writer goroutine reflect-encodes the same map for the SSE
// transport. Concurrent map iteration + insertion panics inside
// encoding/json with "index out of range [N] with length N" — see
// jobs/2026-04-24__09-49-29 trials hit at server.go:writeEvent. Shallow is
// enough because tool inputs from json.Unmarshal are flat (string/bool/int
// values); the nested maps the encoder might recurse into are not the ones
// that get mutated post-emit.
func snapshotInput(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// dispatchToolCalls is the single, unified tool dispatcher for both the main agent
// and subagents/workflow agents. Session-specific behaviour (confirmation prompts,
// interactive tools, UI events) is injected via dispatchOptions.
func dispatchToolCalls(ctx context.Context, msg *llm.Message, opts dispatchOptions) []llm.ContentBlock {
	// --- Stage 1: Parse & classify ---
	var tasks []*toolTask
	hasInteractive := false
	hasWrite := false

	for _, toolUse := range msg.ToolCalls {
		input := toolUse.Input
		if input == nil {
			input = map[string]any{}
		}

		reason, _ := input["reason"].(string)
		var bashReasonNotReadFile, bashReasonNotEditFile, bashReasonNotGlobFiles, bashReasonToIncreaseTimeout string
		if toolUse.Name == "bash" {
			bashReasonNotReadFile, _ = input["reason_to_use_instead_of_read_file_tool"].(string)
			bashReasonNotEditFile, _ = input["reason_to_use_instead_of_edit_file_tool"].(string)
			bashReasonNotGlobFiles, _ = input["reason_to_use_instead_of_glob_files_tool"].(string)
			bashReasonToIncreaseTimeout, _ = input["reason_to_increase_timeout"].(string)
			// Treat "N/A" as absent.
			if bashReasonNotReadFile == "N/A" {
				bashReasonNotReadFile = ""
			}
			if bashReasonNotEditFile == "N/A" {
				bashReasonNotEditFile = ""
			}
			if bashReasonNotGlobFiles == "N/A" {
				bashReasonNotGlobFiles = ""
			}
			if bashReasonToIncreaseTimeout == "N/A" {
				bashReasonToIncreaseTimeout = ""
			}
		}
		summary := SummarizeToolInput(toolUse.Name, input)

		t := &toolTask{
			toolUse:                     toolUse,
			input:                       input,
			summary:                     summary,
			reason:                      reason,
			bashReasonNotReadFile:       bashReasonNotReadFile,
			bashReasonNotEditFile:       bashReasonNotEditFile,
			bashReasonNotGlobFiles:      bashReasonNotGlobFiles,
			bashReasonToIncreaseTimeout: bashReasonToIncreaseTimeout,
			interactive:                 interactiveTools[toolUse.Name] && opts.handleSpecial != nil,
		}

		if t.interactive {
			hasInteractive = true
		}
		if writeTools[toolUse.Name] {
			hasWrite = true
		}

		tasks = append(tasks, t)
	}

	if len(tasks) == 0 {
		return nil
	}

	// Resolve the tool-timeout bounds once up front. The bounds are supplied
	// by the caller via dispatchOptions; when unset (zero), fall back to the
	// package-level defaults. The bounds are constant for the duration of
	// this emit loop, and every per-task TimeoutSec value below is computed
	// against them.
	def := opts.toolTimeoutDefault
	if def <= 0 {
		def = defaultToolTimeoutDefault
	}
	maxv := opts.toolTimeoutMax
	if maxv <= 0 {
		maxv = defaultToolTimeoutMax
	}

	// Emit ALL tool_call events upfront
	silent := isSilentCtx(ctx)
	for _, t := range tasks {
		if !silent {
			log.Printf("[dispatch] tool call: %s %s", t.toolUse.Name, t.summary)
		}
		if opts.emitToolCall != nil {
			opts.emitToolCall(protocol.EventToolCall{
				ToolID:                  t.toolUse.ID,
				Name:                    t.toolUse.Name,
				Arguments:               snapshotInput(t.input),
				Summary:                 t.summary,
				Reason:                  t.reason,
				TimeoutSec:              int(resolveToolTimeout(t.toolUse.Name, t.input, def, maxv).Seconds()),
				ReasonNotReadFile:       t.bashReasonNotReadFile,
				ReasonNotEditFile:       t.bashReasonNotEditFile,
				ReasonNotGlobFiles:      t.bashReasonNotGlobFiles,
				ReasonToIncreaseTimeout: t.bashReasonToIncreaseTimeout,
			})
		}
	}

	// --- Stage 2: Execute ---
	canParallelize := !hasInteractive && !hasWrite && len(tasks) > 1

	if canParallelize {
		executeToolsParallel(ctx, tasks, opts)
	} else {
		executeToolsSequential(ctx, tasks, opts)
	}

	// --- Stage 3: Collect results ---
	toolResults := make([]llm.ContentBlock, 0, len(tasks))

	for _, t := range tasks {
		if t.result == nil {
			// Tool was skipped or interrupted (e.g. ctx cancelled mid-dispatch).
			// The API requires every tool_use to have a matching tool_result,
			// so synthesize a cancellation result.
			t.result = &ToolResult{Output: "Cancelled", IsError: true}
			t.apiResult = llm.NewToolResultBlock(t.toolUse.ID, "Cancelled", true)
			if opts.emitToolResult != nil {
				opts.emitToolResult(t.toolUse.ID, t.toolUse.Name, t.input, "Cancelled", true, 0)
			}
			toolResults = append(toolResults, t.apiResult)
			continue
		}

		toolResults = append(toolResults, t.apiResult)
	}

	return toolResults
}

// executeToolsParallel runs all tools concurrently, emitting results as they complete.
func executeToolsParallel(ctx context.Context, tasks []*toolTask, opts dispatchOptions) {
	var wg sync.WaitGroup

	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}

		// Launch goroutine for tool execution
		wg.Add(1)
		go func(t *toolTask) {
			defer wg.Done()

			silent := isSilentCtx(ctx)
			if !silent {
				log.Printf("[dispatch] exec start: %s id=%s", t.toolUse.Name, t.toolUse.ID)
			}
			t0 := time.Now()
			result := opts.executeTool(t.toolUse.Name, t.input)
			if !silent {
				log.Printf("[dispatch] exec done: %s id=%s elapsed=%s is_error=%v",
					t.toolUse.Name, t.toolUse.ID, time.Since(t0), result != nil && result.IsError)
			}
			if result == nil {
				result = &ToolResult{Output: "tool returned nil", IsError: true}
			}

			// If tool needs confirmation, defer to post-parallel sequential handling.
			if result.NeedsConfirmation {
				t.result = result
				return
			}

			t.result = result
			t.apiResult = llm.NewToolResultBlock(t.toolUse.ID, result.Output, result.IsError)
			if opts.emitToolResult != nil {
				opts.emitToolResult(t.toolUse.ID, t.toolUse.Name, t.input, result.Output, result.IsError, result.LineOffset)
			}
		}(t)
	}

	wg.Wait()

	// Handle any tools that returned NeedsConfirmation (sequentially)
	for _, t := range tasks {
		if t.result == nil || !t.result.NeedsConfirmation {
			continue
		}
		result := resolveConfirmation(ctx, t, opts)
		t.result = result
		t.apiResult = llm.NewToolResultBlock(t.toolUse.ID, result.Output, result.IsError)
		if opts.emitToolResult != nil {
			opts.emitToolResult(t.toolUse.ID, t.toolUse.Name, t.input, result.Output, result.IsError, result.LineOffset)
		}
	}
}

// executeToolsSequential runs tools one at a time.
func executeToolsSequential(ctx context.Context, tasks []*toolTask, opts dispatchOptions) {
	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}

		// Delegate to session-level handler if available (ask_question_to_user, spawn_agent, task_output)
		if opts.handleSpecial != nil {
			if result, handled := opts.handleSpecial(ctx, t.toolUse.Name, t.input); handled {
				t.result = result
				t.apiResult = llm.NewToolResultBlock(t.toolUse.ID, result.Output, result.IsError)
				if result.IsError && opts.emitToolResult != nil {
					opts.emitToolResult(t.toolUse.ID, t.toolUse.Name, t.input, result.Output, true, 0)
				}
				continue
			}
		}

		silent := isSilentCtx(ctx)
		if !silent {
			log.Printf("[dispatch] exec start: %s id=%s", t.toolUse.Name, t.toolUse.ID)
		}
		t0 := time.Now()
		result := opts.executeTool(t.toolUse.Name, t.input)
		if !silent {
			log.Printf("[dispatch] exec done: %s id=%s elapsed=%s is_error=%v",
				t.toolUse.Name, t.toolUse.ID, time.Since(t0), result != nil && result.IsError)
		}
		if result == nil {
			result = &ToolResult{Output: "tool returned nil", IsError: true}
		}

		if result.NeedsConfirmation {
			result = resolveConfirmation(ctx, t, opts)
		}

		t.result = result
		t.apiResult = llm.NewToolResultBlock(t.toolUse.ID, result.Output, result.IsError)
		if opts.emitToolResult != nil {
			opts.emitToolResult(t.toolUse.ID, t.toolUse.Name, t.input, result.Output, result.IsError, result.LineOffset)
		}
	}
}

// resolveConfirmation handles a NeedsConfirmation result by calling opts.confirmFn.
// When confirmFn is nil, the tool is treated as denied.
func resolveConfirmation(ctx context.Context, t *toolTask, opts dispatchOptions) *ToolResult {
	if opts.confirmFn == nil {
		return &ToolResult{Output: "Permission denied.", IsError: true}
	}
	approved, cancelled := opts.confirmFn(ctx, t.toolUse.Name, t.input)
	if cancelled {
		return &ToolResult{Output: "Cancelled", IsError: true}
	}
	if !approved {
		return &ToolResult{Output: "Permission denied by user.", IsError: true}
	}
	// Re-run with confirmed flag set
	p := make(map[string]any, len(t.input)+1)
	for k, v := range t.input {
		p[k] = v
	}
	p["confirmed"] = true
	return opts.executeTool(t.toolUse.Name, p)
}

// sessionDispatchToolCalls is the Session-specific wrapper around the unified dispatcher.
func (s *Session) sessionDispatchToolCalls(ctx context.Context, msg *llm.Message) []llm.ContentBlock {
	def, maxv := s.toolTimeoutBounds()
	opts := dispatchOptions{
		cwd:                s.cwd,
		toolTimeoutDefault: def,
		toolTimeoutMax:     maxv,
		executeTool: func(name string, input map[string]any) *ToolResult {
			return s.executeToolDirect(ctx, name, input)
		},
		handleSpecial: func(ctx context.Context, name string, input map[string]any) (*ToolResult, bool) {
			switch name {
			case "ask_question_to_user":
				result, err := s.handleAskQuestionsBatch(ctx, input)
				if err != nil {
					return &ToolResult{Output: "Cancelled", IsError: true}, true
				}
				return result, true
			case "spawn_agent":
				output, isErr := s.handleSpawnAgent(ctx, input)
				return &ToolResult{Output: output, IsError: isErr}, true
			case "task_output":
				output, isErr := s.handleTaskOutput(ctx, input)
				return &ToolResult{Output: output, IsError: isErr}, true
			case "todo_write":
				output, isErr := s.handleTodoWrite(ctx, input)
				return &ToolResult{Output: output, IsError: isErr}, true
			case "todo_read":
				output, isErr := s.handleTodoRead(ctx, input)
				return &ToolResult{Output: output, IsError: isErr}, true
			}
			return nil, false
		},
		confirmFn: func(ctx context.Context, name string, input map[string]any) (approved, cancelled bool) {
			// Extract requested directories for directory-access confirmations.
			var requestedDirs []string
			if rd, ok := input["_requested_dirs"].([]string); ok {
				requestedDirs = rd
			}
			s.emit("event.confirm_request", protocol.EventConfirmRequest{
				ToolName:      name,
				Params:        snapshotInput(input),
				RequestedDirs: requestedDirs,
				Detail:        buildConfirmDetail(s.cwd, name, input),
			})
			cmd, ok := s.waitForCommand(s.ctx, "session.confirm")
			if !ok {
				return false, true
			}
			var confirmData protocol.SessionConfirmData
			json.Unmarshal(cmd.Data, &confirmData)
			// If approved and directories were requested, add them to the session.
			if confirmData.Approved && len(requestedDirs) > 0 {
				for _, dir := range requestedDirs {
					s.addAllowedDir(dir)
				}
				if confirmData.PersistDirs {
					s.persistAllowedDirs(requestedDirs)
				}
				// Clean the internal field before re-execution.
				delete(input, "_requested_dirs")
			}
			// If approved and this is a write-class tool, remember the file for auto-approval.
			if confirmData.Approved && writeClassTools[name] {
				if pathStr, ok := input["path"].(string); ok {
					if resolved, err := resolvePathInAllowed(s.cwd, s.toolAllowedDirs(), pathStr); err == nil {
						s.addApprovedWriteFile(resolved)
					}
				}
			}
			return confirmData.Approved, false
		},
		emitToolCall: func(ev protocol.EventToolCall) {
			s.emit("event.tool_call", ev)
		},
		emitToolResult: func(toolID, name string, input map[string]any, output string, isError bool, lineOffset int) {
			s.emitToolResult(toolID, name, input, output, isError, lineOffset)
		},
	}
	return dispatchToolCalls(ctx, msg, opts)
}

// providerFor returns the provider name for the given model.
// claude-* models use "anthropic"; all others use "openai".
func providerFor(model string) string {
	if strings.HasPrefix(model, "claude") {
		return "anthropic"
	}
	return "openai"
}

// handleWorkflowCommand handles a session.workflow command by looking up and executing
// the workflow matching the given name.
func (s *Session) handleWorkflowCommand(name, text string) {
	// Unconfigured session: no usable LLM client. Workflows stream too, so
	// refuse before doing any work and surface the error to the UI.
	if s.configErr != nil {
		s.emit("event.error", protocol.EventError{Message: s.unconfiguredMessage()})
		s.emit("event.agent_done", nil)
		return
	}

	var wf *WorkflowDef
	for _, w := range s.snapshotWorkflows() {
		if w.Name == name {
			wf = w
			break
		}
	}
	if wf == nil {
		msg := fmt.Sprintf("workflow %q not found", name)
		log.Printf("[session] %s", msg)
		s.emit("event.error", protocol.EventError{Message: msg})
		s.emit("event.agent_done", nil)
		return
	}

	s.sessionMode = "workflow"
	s.activeWorkflow = name
	s.persist()

	// Resume an interrupted run of this workflow (user cancel or daemon
	// restart): continue from the persisted cursor instead of starting over.
	// Any text the user submitted alongside is injected into the resumed
	// step's agent via the usual workflow-message channel. A different
	// workflow name (or a fresh start after completion) discards old state.
	var resume *WorkflowRunState
	if st := s.snapshotWorkflowRunState(); st != nil {
		if st.Name == name && st.Resumable() {
			if _, ok := wf.Steps[st.CurrentRef.ID]; ok {
				resume = st
			}
		}
		if resume == nil {
			s.setWorkflowRunState(nil)
		}
	}
	if resume != nil && text != "" {
		select {
		case s.workflowMsgChan <- text:
		default:
		}
	}
	if resume != nil {
		s.emit("event.stream_chunk", protocol.EventStreamChunk{
			Text: fmt.Sprintf("Resuming workflow %q at step '%s' (iteration %d).\n", name, resume.CurrentRef.ID, resume.Iteration),
		})
	}

	planCtx, planCancel := context.WithCancel(s.ctx)
	s.planCancel = planCancel
	defer func() {
		planCancel()
		s.planCancel = nil
	}()

	err := s.executeWorkflow(planCtx, wf, text, resume)
	if err != nil && !errors.Is(err, context.Canceled) {
		s.emit("event.error", protocol.EventError{Message: fmt.Sprintf("workflow failed: %v", err)})
	}
	s.emit("event.agent_done", nil)
}

// handleSpawnAgent resolves and runs a subagent.
func (s *Session) handleSpawnAgent(ctx context.Context, input map[string]any) (string, bool) {
	prompt, _ := input["prompt"].(string)
	if prompt == "" {
		return "spawn_agent requires a 'prompt' parameter", true
	}

	agentType, _ := input["agent_type"].(string)
	if agentType == "" {
		// Default to "general" if it exists, otherwise first available agent
		if _, ok := s.customAgents["general"]; ok {
			agentType = "general"
		} else {
			for k := range s.customAgents {
				agentType = k
				break
			}
		}
		if agentType == "" {
			return "No agents available. Define agents in .vix/agents/", true
		}
	}
	background, _ := input["background"].(bool)

	config, ok := s.customAgents[agentType]
	if !ok {
		available := make([]string, 0)
		for k := range s.customAgents {
			available = append(available, k)
		}
		return fmt.Sprintf("Unknown agent type '%s'. Available: %s", agentType, strings.Join(available, ", ")), true
	}

	cred := s.llm.Credential()
	parentModel := s.model

	// In headless mode, remove ask_question_to_user from subagent tools
	if s.headless && config.Tools != nil {
		var filtered []string
		for _, t := range config.Tools {
			if t != "ask_question_to_user" {
				filtered = append(filtered, t)
			}
		}
		config.Tools = filtered
	}

	if background {
		// Background tasks must outlive the current tool dispatch — bind both
		// the subagent loop and its tool executor to the session context, not
		// the per-dispatch ctx, which is cancelled as soon as
		// sessionDispatchToolCalls returns.
		bgCtx := s.ctx
		bgExecuteTool := func(name string, params map[string]any, cwd string) (*ToolResult, error) {
			return s.executeToolConfirmed(bgCtx, name, params), nil
		}
		bgDef, bgMax := s.toolTimeoutBounds()
		taskID := s.backgroundTasks.SpawnBackground(bgCtx, config, prompt, cred, parentModel, bgExecuteTool, s.cwd, bgDef, bgMax, s.searchDirsSlice()...)
		s.emit("event.tool_result", protocol.EventToolResult{
			Name:   "spawn_agent",
			Output: fmt.Sprintf("Background task started. Task ID: %s", taskID),
		})
		log.Printf("[subagent] spawned background task %s (type=%s)", taskID, config.Name)
		return fmt.Sprintf("Background task started. Task ID: %s\nUse task_output to retrieve the result when ready.", taskID), false
	}

	// Foreground subagents share the dispatch ctx so cancellation propagates.
	executeTool := func(name string, params map[string]any, cwd string) (*ToolResult, error) {
		return s.executeToolConfirmed(ctx, name, params), nil
	}
	log.Printf("[subagent] spawning foreground agent (type=%s)", config.Name)
	def, maxv := s.toolTimeoutBounds()
	result, err := RunSubagent(ctx, config, prompt, cred, parentModel, executeTool, s.cwd, s.emitHooks(), def, maxv, s.searchDirsSlice()...)

	if err != nil {
		return fmt.Sprintf("Subagent error: %v", err), true
	}

	return result.Output, result.IsError
}

// RunExploration spawns a named agent (looked up from s.customAgents) as a
// foreground subagent and blocks until it completes or ctx is cancelled.
// It is intended for external callers such as the web API that need to run an
// agent on behalf of the session without going through the normal command loop.
func (s *Session) RunExploration(ctx context.Context, agentName, prompt string) (*SubagentResult, error) {
	config, ok := s.customAgents[agentName]
	if !ok {
		return nil, fmt.Errorf("unknown agent: %q", agentName)
	}
	executeTool := func(name string, params map[string]any, cwd string) (*ToolResult, error) {
		return s.executeToolConfirmed(ctx, name, params), nil
	}
	def, maxv := s.toolTimeoutBounds()
	return RunSubagent(ctx, config, prompt, s.llm.Credential(), s.model, executeTool, s.cwd, nil, def, maxv, s.searchDirsSlice()...)
}

func (s *Session) handleTaskOutput(ctx context.Context, input map[string]any) (string, bool) {
	taskID, _ := input["task_id"].(string)
	if taskID == "" {
		return "task_output requires a 'task_id' parameter", true
	}

	result, err := s.backgroundTasks.WaitForTask(ctx, taskID, 30*time.Second)
	if err != nil {
		return fmt.Sprintf("Error waiting for task: %v", err), true
	}

	if result.InputTokens > 0 || result.OutputTokens > 0 {
		s.emit("event.stream_done", protocol.EventStreamDone{
			InputTokens:         result.InputTokens,
			OutputTokens:        result.OutputTokens,
			CacheCreationTokens: result.CacheCreationTokens,
			CacheReadTokens:     result.CacheReadTokens,
			ElapsedMs:           result.Elapsed.Milliseconds(),
		})
	}

	return result.Output, result.IsError
}

// handleAskQuestionsBatch emits a user question event and waits for all answers.
func (s *Session) handleAskQuestionsBatch(ctx context.Context, input map[string]any) (*ToolResult, error) {
	questionsRaw, ok := input["questions"].([]any)
	if !ok || len(questionsRaw) == 0 {
		return &ToolResult{Output: "ask_questions_batch requires a non-empty 'questions' array", IsError: true}, nil
	}

	var questions []protocol.QuestionDef
	for _, qRaw := range questionsRaw {
		qMap, ok := qRaw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := qMap["id"].(string)
		category, _ := qMap["category"].(string)
		question, _ := qMap["question"].(string)
		var options []string
		if raw, ok := qMap["options"].([]any); ok {
			for _, o := range raw {
				if str, ok := o.(string); ok {
					options = append(options, str)
				}
			}
		}
		questions = append(questions, protocol.QuestionDef{
			ID:       id,
			Category: category,
			Question: question,
			Options:  options,
		})
	}

	s.emit("event.user_question", protocol.EventUserQuestion{
		Questions: questions,
	})

	cmd, ok2 := s.waitForCommand(ctx, "session.user_answer")
	if !ok2 {
		return nil, ctx.Err()
	}

	var answerData protocol.SessionUserAnswerData
	json.Unmarshal(cmd.Data, &answerData)

	// For a single question, return the answer directly.
	if len(questions) == 1 {
		if answerData.Answers != nil {
			if ans, ok := answerData.Answers[questions[0].ID]; ok {
				return &ToolResult{Output: ans}, nil
			}
		}
		return &ToolResult{Output: answerData.Answer}, nil
	}

	// For multiple questions, format answers as readable text for the LLM.
	if answerData.Answers != nil {
		var sb strings.Builder
		for _, q := range questions {
			ans, exists := answerData.Answers[q.ID]
			if exists {
				sb.WriteString(fmt.Sprintf("%s: %s\n", q.Category, ans))
			}
		}
		return &ToolResult{Output: sb.String()}, nil
	}

	return &ToolResult{Output: answerData.Answer}, nil
}

// extractStatusCode returns the HTTP status code from an API error, or 0 if not applicable.
// Today this is Anthropic-only; once the OpenAI/MiniMax adapters land, this
// helper moves into errors.go as a provider-aware dispatcher.
func extractStatusCode(err error) int {
	return extractStatusCodeAnthropic(err)
}

// accumulateTurnTelemetry tracks a completed LLM turn and updates session totals.
func (s *Session) accumulateTurnTelemetry(inputTokens, outputTokens, cacheWrite, cacheRead, elapsedMs int64, toolCalls int) {

	s.turnCount++
	s.totalInputTokens += inputTokens
	s.totalOutputTokens += outputTokens
	s.totalCacheRead += cacheRead
	s.totalCacheWrite += cacheWrite
	s.totalAPIWaitMs += elapsedMs
}

// countToolUses counts tool_use blocks in an LLM response message.
func countToolUses(msg *llm.Message) int {
	return len(msg.ToolCalls)
}
