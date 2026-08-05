package protocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ThreadCommand is a message sent from client to daemon.
//
// AuthToken carries the shared-secret token the daemon was started with via
// -auth-token-path. The daemon validates it on every message — both the
// initial thread.start and every follow-up — and closes the connection on
// mismatch. The auth check is OFF by default: when vixd is launched without
// -auth-token-path the daemon-side token is empty, AuthToken is ignored,
// and any caller is accepted (legacy single-user-host behaviour). The
// omitempty tag keeps the wire format clean in that mode.
type ThreadCommand struct {
	Type      string          `json:"type"`
	AuthToken string          `json:"auth_token,omitempty"`
	Data      json.RawMessage `json:"data"`
}

// ThreadEvent is a message sent from daemon to client.
type ThreadEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// --- Client → Daemon command payloads ---

// InstanceRegisterData is the payload of an "instance.register" command. A vix
// process opens one such connection at startup and holds it open for its whole
// lifetime so the daemon can count attached instances independently of threads
// (a single vix instance may hold several thread connections, or none). The
// connection closing — on clean exit or process death — is the liveness signal;
// no heartbeat is sent. The fields are advisory (logging/observability only);
// counting relies on the connection itself.
type InstanceRegisterData struct {
	InstanceID string `json:"instance_id,omitempty"`
	Mode       string `json:"mode,omitempty"` // "tui" | "headless"
}

// ThreadStartData is sent to start a new agent thread.
type ThreadStartData struct {
	CWD                            string `json:"cwd"`
	ConfigDir                      string `json:"config_dir,omitempty"`
	Model                          string `json:"model"`
	ForceInit                      bool   `json:"force_init"`
	EnableAutomaticWritePermission bool   `json:"enable_automatic_write_permission"`
	EnableAutomaticDirectoryAccess bool   `json:"enable_automatic_directory_access"`
	Headless                       bool   `json:"headless"`
	// ClientVersion is the vix binary version opening this thread. The daemon
	// refuses the thread (event.error, code "version_mismatch") when it does
	// not exactly match the daemon's own version — a long-lived daemon must
	// never serve a client from a different build.
	ClientVersion string `json:"client_version,omitempty"`
	// Fork fields: when ForkThreadID is non-empty the new thread is seeded
	// with the conversation history of the named thread up to and including
	// the turn at ForkTurnIdx (0-based).
	ForkThreadID string `json:"fork_thread_id,omitempty"`
	ForkTurnIdx  int    `json:"fork_turn_idx,omitempty"`
	// AttachThreadID, when non-empty, asks the daemon to resume a persisted
	// thread by ID instead of creating a fresh one: it loads the on-disk
	// record from open/, reuses that ID, and replays the conversation to the
	// client via event.replay. Records in closed/ are not attachable — an
	// explicitly closed thread stays closed. If no open record exists the
	// daemon answers with event.error carrying Code "thread_not_found".
	AttachThreadID string `json:"attach_thread_id,omitempty"`
}

// TriggerInfo records what fired a vix-initiated thread: a scheduled job's
// trigger type ("cron" | "at") and the job id.
type TriggerInfo struct {
	Type string `json:"type"`
	Ref  string `json:"ref,omitempty"`
}

// ThreadSummary is the lightweight projection of a persisted thread returned
// by the thread.list RPC. It carries just enough to populate the Threads
// list without loading full conversation histories.
type ThreadSummary struct {
	ID    string `json:"id"`
	CWD   string `json:"cwd"`
	Model string `json:"model"`
	// Title is the thread's display title: set by an LLM summarization pass
	// after a few turns (user threads) or at creation time (job runs). When
	// empty, clients fall back to FirstMessage.
	Title         string `json:"title,omitempty"`
	FirstMessage  string `json:"first_message,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`      // RFC3339
	LastRequestAt string `json:"last_request_at,omitempty"` // RFC3339
	// Attached is true when this thread is currently live in the daemon (open
	// in some connection). The launching client uses it to avoid attaching a
	// thread another instance already owns (exclusive single-writer ownership).
	Attached bool `json:"attached,omitempty"`
	// Origin distinguishes user-started threads ("", the default) from
	// vix-initiated ones ("vix" — scheduled job runs, synthetic alerts). The
	// TUI groups the threads list by it and never auto-claims vix-initiated
	// threads on launch.
	Origin  string       `json:"origin,omitempty"`
	Trigger *TriggerInfo `json:"trigger,omitempty"`
	// JobStatus carries the finished run's status (ok | error | timeout) for
	// vix-initiated threads, powering the badge in the threads list.
	JobStatus string `json:"job_status,omitempty"`
	// Unread reports whether the thread holds content the user hasn't seen
	// (thread-global, persisted — survives restarts). Cleared via the
	// thread.mark_read command when the user views the thread.
	Unread bool `json:"unread,omitempty"`
}

// DirUsage is a working directory ranked by how many open threads use it,
// returned by the thread.dirs RPC. It powers the welcome screen's
// recent-directories list and the default working directory for new threads.
type DirUsage struct {
	// Path is the working directory (thread CWD).
	Path string `json:"path"`
	// Count is the number of open user threads rooted at Path.
	Count int `json:"count"`
	// LastRequestAt is the most recent activity across those threads,
	// RFC3339; used to order by recency and to pick the "latest used" dir.
	LastRequestAt string `json:"last_request_at,omitempty"`
}

// ThreadInputData carries user chat input.
type ThreadInputData struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment represents a file attachment sent with user input. Two kinds are
// supported: "image" (carries base64 Data + an image/* MediaType, embedded as a
// vision block) and "file" (a path-only reference to a text or PDF file that the
// daemon reads and converts to text at send time).
type Attachment struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	Path      string `json:"path,omitempty"`
}

// ValidateAttachment checks if an attachment is valid.
func ValidateAttachment(att Attachment) error {
	switch att.Type {
	case "image":
		if !strings.HasPrefix(att.MediaType, "image/") {
			return fmt.Errorf("invalid media type: %s (must start with 'image/')", att.MediaType)
		}
		if _, err := base64.StdEncoding.DecodeString(att.Data); err != nil {
			return fmt.Errorf("invalid base64 data: %w", err)
		}
	case "file":
		if att.Path == "" {
			return fmt.Errorf("file attachment missing path")
		}
	default:
		return fmt.Errorf("invalid attachment type: %s (only 'image' and 'file' supported)", att.Type)
	}
	return nil
}

// ThreadConfirmData carries tool approval/denial.
type ThreadConfirmData struct {
	Approved    bool `json:"approved"`
	PersistDirs bool `json:"persist_dirs,omitempty"` // save approved directories to settings.json
}

// ThreadPlanActionData carries plan review decisions.
type ThreadPlanActionData struct {
	Action string `json:"action"` // "approve", "reject", "modify"
	Text   string `json:"text,omitempty"`
}

// ThreadUserAnswerData carries the user's response to a question.
type ThreadUserAnswerData struct {
	Answer  string            `json:"answer"`
	Text    string            `json:"text,omitempty"`    // user input when has_user_input
	Answers map[string]string `json:"answers,omitempty"` // question ID → answer (batch mode)
}

// ThreadWorkflowData carries a workflow execution request. Name selects a
// workflow already loaded by the thread (from config/workflow.json). Workflow,
// when present, is an inline definition (a workflow.Def) registered into the
// thread's workflow set for this run; the thread looks it up by its own name.
// Carried as raw JSON so the protocol package stays free of a workflow-package
// dependency.
type ThreadWorkflowData struct {
	Name     string          `json:"name"`
	Text     string          `json:"text"`
	Workflow json.RawMessage `json:"workflow,omitempty"`
}

// ThreadWorkflowMessageData carries a user message to inject into the running workflow.
type ThreadWorkflowMessageData struct {
	Text string `json:"text"`
}

// ThreadSetModelData carries a model switch request.
type ThreadSetModelData struct {
	Model string `json:"model"`
}

// ThreadTrimData carries a history trim request.
type ThreadTrimData struct {
	TurnIdx int `json:"turn_idx"` // keep history up to and including this turn (0-based)
}

// --- Daemon → Client event payloads ---

// EventThreadStarted acknowledges thread creation.
type EventThreadStarted struct {
	ThreadID    string `json:"thread_id"`
	StartedAt   string `json:"started_at"` // RFC3339
	ParentID    string `json:"parent_id,omitempty"`
	ForkTurnIdx int    `json:"fork_turn_idx,omitempty"`
	// WhiteboardBase is the local web UI origin (e.g. "http://localhost:1337")
	// used by the client to build whiteboard links for mermaid diagrams. Empty
	// when the web UI is disabled, in which case the client shows no link.
	WhiteboardBase string `json:"whiteboard_base,omitempty"`
}

// EventInitState carries brain init progress.
type EventInitState struct {
	State int    `json:"state"`
	Model string `json:"model,omitempty"` // resolved model spec, set on InitDone
}

// EventStreamChunk carries an LLM text delta.
type EventStreamChunk struct {
	Text string `json:"text"`
}

// EventThinkingChunk carries an LLM extended-thinking delta.
type EventThinkingChunk struct {
	Text string `json:"text"`
}

// EventStreamDone signals LLM turn completion with token stats.
type EventStreamDone struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	ElapsedMs           int64 `json:"elapsed_ms"`
}

// EventCompacted signals that the conversation history was summarized to free
// context. FromTokens is the prompt size before compaction; ToTokens is 0 when
// the post-compaction size is not yet known (recomputed on the next turn).
type EventCompacted struct {
	FromTokens      int64 `json:"from_tokens"`
	ToTokens        int64 `json:"to_tokens"`
	SummarizedTurns int   `json:"summarized_turns"`
	Auto            bool  `json:"auto"` // true = auto-trigger, false = /compact
}

// EventToolCall indicates a tool call is starting.
type EventToolCall struct {
	ToolID string `json:"tool_id"`
	Name   string `json:"name"`
	// Arguments is the raw tool input as issued by the model (structured
	// key/value payload). Included so trajectory consumers (ATIF exporters,
	// SFT/RL pipelines) can round-trip the exact call without re-deriving
	// it from Summary. Summary stays the human-readable one-liner.
	Arguments map[string]any `json:"arguments,omitempty"`
	Summary   string         `json:"summary"`
	Reason    string         `json:"reason,omitempty"`
	// TimeoutSec is the effective tool-call timeout in seconds, after daemon
	// clamping. See daemon.resolveToolTimeout: floor and cap come from the
	// `tool_timeouts` block in settings.json (defaulting to 120s / 600s when
	// absent or invalid), and only bash/glob_files honor the model's
	// `timeout` override.
	TimeoutSec int `json:"timeout_sec,omitempty"`
	// Bash-specific alternative-tool justifications (omitted when empty or "N/A").
	ReasonNotReadFile  string `json:"reason_not_read_file,omitempty"`
	ReasonNotEditFile  string `json:"reason_not_edit_file,omitempty"`
	ReasonNotGlobFiles string `json:"reason_not_glob_files,omitempty"`
	// ReasonToIncreaseTimeout is the model's justification for raising the
	// bash/glob_files timeout above the 120s default. Populated from the
	// `reason_to_increase_timeout` field of the tool input.
	ReasonToIncreaseTimeout string `json:"reason_to_increase_timeout,omitempty"`
}

// EventToolResult carries the result of a tool execution.
type EventToolResult struct {
	ToolID  string `json:"tool_id"`
	Name    string `json:"name"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
	Detail  string `json:"detail,omitempty"` // optional rich detail (e.g. edit diff)
}

// EventConfirmRequest asks the user to approve a tool execution.
type EventConfirmRequest struct {
	ToolName      string         `json:"tool_name"`
	Params        map[string]any `json:"params"`
	RequestedDirs []string       `json:"requested_dirs,omitempty"` // directories outside cwd that require approval
	Detail        string         `json:"detail,omitempty"`         // same format as EventToolResult.Detail — fenced code block or structured diff
}

// EventPlanProposed carries a plan for user review.
type EventPlanProposed struct {
	Plan *Plan `json:"plan"`
}

// EventPlanTaskStart signals a plan task is starting.
type EventPlanTaskStart struct {
	TaskIdx int    `json:"task_idx"`
	Title   string `json:"title"`
	Total   int    `json:"total"`
}

// EventPlanTaskDone signals a plan task has finished.
type EventPlanTaskDone struct {
	TaskIdx int    `json:"task_idx"`
	Title   string `json:"title"`
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// EventPlanComplete signals all plan tasks are done.
type EventPlanComplete struct {
	Plan *Plan `json:"plan"`
}

// QuestionDef defines a single question in a batch.
type QuestionDef struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// EventQuestionOption is a structured option for workflow tool steps.
type EventQuestionOption struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	HasUserInput bool   `json:"has_user_input,omitempty"`
}

// EventUserQuestion asks the user a question with options.
type EventUserQuestion struct {
	// Single-question fields (backward compatible)
	Question    string                `json:"question"`
	Options     []string              `json:"options"`
	RichOptions []EventQuestionOption `json:"rich_options,omitempty"` // structured options (workflow tool steps)
	Placeholder string                `json:"placeholder,omitempty"`
	Category    string                `json:"category,omitempty"`

	// Multi-question batch (if set, overrides single fields)
	Questions []QuestionDef `json:"questions,omitempty"`
}

// EventError carries an error message.
type EventError struct {
	Message string `json:"message"`
	// Code is an optional machine-readable discriminator. Used by the attach
	// flow: "thread_not_found" tells the client a resume target no longer
	// exists on disk so it can orphan the thread (offer /copy) instead of
	// retrying the reconnect forever; "thread_busy" tells the client the
	// thread is already open in another connection (exclusive single-writer
	// ownership) so it should retry later or attach a different thread.
	Code string `json:"code,omitempty"`
}

// --- Thread restore / replay (attach) ---

// ReplayBlock is one content block of a replayed conversation turn, projected
// into a wire-stable shape owned by this package (so neither protocol nor the
// TUI needs to import the daemon's llm types).
type ReplayBlock struct {
	Kind     string         `json:"kind"` // "text" | "thinking" | "tool_use" | "tool_result" | "retry" | "error"
	Text     string         `json:"text,omitempty"`
	ToolID   string         `json:"tool_id,omitempty"`
	ToolName string         `json:"tool_name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
	Output   string         `json:"output,omitempty"`
	IsError  bool           `json:"is_error,omitempty"`
	// Retry-notice fields (Kind == "retry"): a transient API error that was
	// retried during a workflow run, persisted so a reopened thread replays
	// the same notice an interactive run shows live. Text carries the reason.
	Attempt    int `json:"attempt,omitempty"`
	MaxRetries int `json:"max_retries,omitempty"`
	WaitSecs   int `json:"wait_secs,omitempty"`
	// Failure-notice field (Kind == "error"): the workflow step that aborted the
	// run, when known. Text carries the failure reason (including captured step
	// output). Persisted so a reopened run shows why it failed.
	StepID string `json:"step_id,omitempty"`
}

// ReplayMessage is one turn of a replayed conversation.
type ReplayMessage struct {
	Role   string        `json:"role"` // "user" | "assistant"
	Blocks []ReplayBlock `json:"blocks"`
	// Timestamp is when the turn was originally sent (RFC3339), so the
	// replayed viewport shows original send times instead of the relaunch
	// time. Empty for legacy threads persisted before timestamps existed;
	// the TUI omits the "Sent at" line in that case.
	Timestamp string `json:"timestamp,omitempty"`
}

// EventReplay is emitted once, immediately after event.thread_started, when a
// client attaches to a persisted thread. It rebuilds the chat viewport and
// restores the thread's mode/model/todos, plus any restore-time warnings
// (model changed, workflow missing, etc.).
type EventReplay struct {
	Messages       []ReplayMessage `json:"messages"`
	Todos          []TodoItem      `json:"todos,omitempty"`
	ActivePlan     *Plan           `json:"active_plan,omitempty"`
	Model          string          `json:"model,omitempty"`
	Title          string          `json:"title,omitempty"`
	ThreadMode     string          `json:"thread_mode,omitempty"`
	ActiveWorkflow string          `json:"active_workflow,omitempty"`
	// Warnings are human-readable restore notices rendered into the viewport
	// (e.g. "Saved with model X; switched to your current default Y.").
	Warnings []string `json:"warnings,omitempty"`
}

// EventTitleUpdated is emitted on a thread's stream when its display title
// changes (LLM auto-titling after a few turns). The threads list refresh for
// other clients goes through event.threads_changed.
type EventTitleUpdated struct {
	Title string `json:"title"`
}

// EventRetry notifies the UI about an API retry attempt.
type EventRetry struct {
	Attempt    int    `json:"attempt"`
	MaxRetries int    `json:"max_retries"`
	WaitSecs   int    `json:"wait_secs"`
	Reason     string `json:"reason"`
}

// EventThinkingStall notifies the UI that extended thinking exceeded the
// stall timeout. The daemon cancels the stream and nudges the model to
// conclude on the next retry attempt.
type EventThinkingStall struct {
	ElapsedMs    int64 `json:"elapsed_ms"`
	SummaryChars int   `json:"summary_chars"`
}

// --- Workflow info (sent to UI for mode cycling) ---

// WorkflowInfo describes a workflow available for UI mode cycling.
type WorkflowInfo struct {
	Name string `json:"name"`
}

// EventWorkflowsAvailable carries the list of configured workflows to the UI.
type EventWorkflowsAvailable struct {
	Workflows []WorkflowInfo `json:"workflows"`
}

// SkillInfo describes a loaded skill available for slash-command autocomplete.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// EventSkillsAvailable carries the list of loaded skills to the UI so they can
// be offered as slash commands.
type EventSkillsAvailable struct {
	Skills []SkillInfo `json:"skills"`
}

// EventToolBackends reports the search-tool backends resolved by the daemon so
// the Settings tab can display which implementation the grep and glob tools
// actually use. The *Effective fields reflect PATH fallback (e.g. a configured
// "fd" resolves to "builtin" when fd is absent); the *Configured fields carry
// the requested backend so the UI can flag a fallback. Emitted once per thread
// at init.
type EventToolBackends struct {
	GrepEffective  string `json:"grep_effective"`
	GrepConfigured string `json:"grep_configured"`
	GlobEffective  string `json:"glob_effective"`
	GlobConfigured string `json:"glob_configured"`
}

// EventUpdateAvailable informs the UI of the running version versus the latest
// published GitHub release. Emitted once per thread at init. Latest is empty
// when the daemon is up-to-date, the check is disabled, or it could not reach
// GitHub. Method is one of "brew" | "script" | "unknown" and selects the
// in-app upgrade command the TUI offers.
type EventUpdateAvailable struct {
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	URL     string `json:"url,omitempty"`
	Method  string `json:"method,omitempty"`
}

// --- Job events (scheduled jobs engine) ---
// Broadcast to every attached client via BroadcastEvent; payloads are also
// emitted by the scheduler as plain maps with matching keys.

// EventJobRun signals a job lifecycle transition before/without a finished
// run: status is "started", "invalid" (spec failed validation), or
// "auto_disabled" (too many consecutive failures). Error carries detail for
// the non-started statuses.
type EventJobRun struct {
	JobID  string `json:"job_id"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// EventJobDone reports a finished job run. Status is ok | error | timeout
// (skipped runs are silent by design). ThreadID references the persisted run
// thread in the threads list.
type EventJobDone struct {
	JobID    string `json:"job_id"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
}

// EventThreadsChanged tells every attached instance (over the control channel,
// once per window) the persisted threads list changed outside their own
// connection (a job run was persisted or swept), so they should re-fetch
// thread.list.
type EventThreadsChanged struct{}

// EventJobsChanged tells every attached instance (over the control channel, once
// per window) the scheduled jobs or lifecycle hooks changed — a run started or
// finished, a spec was enabled/disabled, or the spec directory was hot-reloaded
// — so the Jobs & Triggers tab should re-fetch job.list and hook.list.
type EventJobsChanged struct{}

// EventMCPChanged tells every attached instance (over the control channel, once
// per window) an MCP server was enabled or disabled, so the MCP tab should
// re-fetch mcp.list.
type EventMCPChanged struct{}

// MCPServerSummary is the lightweight projection of a configured MCP server
// returned by the mcp.list RPC, carrying just enough to populate the MCP tab.
type MCPServerSummary struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // "stdio" or "url"
	Enabled bool   `json:"enabled"`
	// Status is "connected", "error", or "disabled". ToolCount and Error are
	// meaningful only for enabled servers that were probed.
	Status    string `json:"status"`
	ToolCount int    `json:"tool_count"`
	Error     string `json:"error,omitempty"`
	// Auth describes OAuth state for servers configured with `oauth`:
	// "authenticated", "needs_auth", or "" (not an OAuth server).
	Auth string `json:"auth,omitempty"`
}

// JobSummary is the lightweight projection of a scheduled job returned by the
// job.list RPC, carrying just enough to populate the Jobs & Triggers tab.
type JobSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// TriggerType is "cron" or "at". Schedule is a human-readable rendering of
	// the trigger (the cron expression, or "at <time>" for one-shots).
	TriggerType string `json:"trigger_type,omitempty"`
	Schedule    string `json:"schedule,omitempty"`
	NextRunAt   string `json:"next_run_at,omitempty"` // RFC3339, empty when not scheduled
	LastRunAt   string `json:"last_run_at,omitempty"` // RFC3339, empty when never run
	LastStatus  string `json:"last_status,omitempty"` // ok | error | skipped | timeout
	Running     bool   `json:"running,omitempty"`     // a run is currently in flight
	CreatedBy   string `json:"created_by,omitempty"`
}

// HookSummary is the lightweight projection of a lifecycle hook returned by the
// hook.list RPC, carrying just enough to populate the Jobs & Triggers tab.
type HookSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Enabled     bool   `json:"enabled"`
	Event       string `json:"event,omitempty"` // the lifecycle event it subscribes to
	Matcher     string `json:"matcher,omitempty"`
	Mode        string `json:"mode,omitempty"`          // sync | async
	LastFiredAt string `json:"last_fired_at,omitempty"` // RFC3339, empty when never fired
	LastStatus  string `json:"last_status,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
}

// --- Workflow events ---

// WorkflowStepInfo carries static metadata about a single workflow step.
type WorkflowStepInfo struct {
	ID          string `json:"id"`
	Explanation string `json:"explanation,omitempty"`
}

// EventWorkflowStart signals a workflow has started.
type EventWorkflowStart struct {
	WorkflowName string             `json:"workflow_name"`
	TotalSteps   int                `json:"total_steps"`
	Steps        []WorkflowStepInfo `json:"steps,omitempty"`
}

// EventWorkflowStepStart signals a workflow step is starting.
type EventWorkflowStepStart struct {
	StepID      string `json:"step_id"`
	StepIdx     int    `json:"step_idx"`
	Total       int    `json:"total"`
	Agent       string `json:"agent"`
	Explanation string `json:"explanation,omitempty"`
}

// ToolStat summarizes tool usage within a workflow step.
type ToolStat struct {
	Name    string `json:"name"`
	Calls   int    `json:"calls"`
	Summary string `json:"summary"`
}

// EventWorkflowStepDone signals a workflow step has finished.
type EventWorkflowStepDone struct {
	StepID              string     `json:"step_id"`
	StepIdx             int        `json:"step_idx"`
	Total               int        `json:"total"`
	Success             bool       `json:"success"`
	TimedOut            bool       `json:"timed_out,omitempty"` // bash step killed by per-step timeout; workflow continues
	Display             string     `json:"display,omitempty"`
	Command             string     `json:"command,omitempty"`     // bash step: resolved command that was run
	BashOutput          string     `json:"bash_output,omitempty"` // bash step: first 5 lines of output
	Model               string     `json:"model,omitempty"`
	InputTokens         int64      `json:"input_tokens,omitempty"`
	OutputTokens        int64      `json:"output_tokens,omitempty"`
	CacheCreationTokens int64      `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64      `json:"cache_read_tokens,omitempty"`
	ToolStats           []ToolStat `json:"tool_stats,omitempty"`
	DurationMs          int64      `json:"duration_ms,omitempty"`
}

// StepCost summarizes token usage and cost for a single workflow step.
type StepCost struct {
	StepID              string  `json:"step_id"`
	Explanation         string  `json:"explanation,omitempty"`
	Model               string  `json:"model"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	Cost                float64 `json:"cost"`
	DurationMs          int64   `json:"duration_ms,omitempty"`
}

// EventWorkflowStatus signals a workflow run status transition (paused,
// blocked, budget_limited, resumed). Carries the run's live accounting so
// clients can render an indicator without tracking step events themselves.
type EventWorkflowStatus struct {
	WorkflowName   string `json:"workflow_name"`
	Status         string `json:"status"`
	StepID         string `json:"step_id,omitempty"`
	Iteration      int    `json:"iteration,omitempty"`
	TokensUsed     int64  `json:"tokens_used,omitempty"`
	TokenBudget    int64  `json:"token_budget,omitempty"`
	ElapsedSeconds int64  `json:"elapsed_seconds,omitempty"`
	Note           string `json:"note,omitempty"`
}

// EventWorkflowComplete signals a workflow has finished.
type EventWorkflowComplete struct {
	WorkflowName string     `json:"workflow_name"`
	Success      bool       `json:"success"`
	Summary      string     `json:"summary,omitempty"`
	StepCosts    []StepCost `json:"step_costs,omitempty"`
	DurationMs   int64      `json:"duration_ms,omitempty"`
}
