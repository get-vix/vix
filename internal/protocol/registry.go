package protocol

// Wire-type registry.
//
// These maps are the single source of truth for the daemon⇄client protocol
// surface: every message the daemon emits (EventTypes) and every command a
// client may send (CommandTypes), keyed by the exact discriminator string that
// travels in ThreadEvent.Type / ThreadCommand.Type.
//
// The registry drives protocol-schema generation (cmd/protoschema reflects over
// these payload types to emit vix-protocol.schema.json) and is guarded by tests
// in this package that assert it stays exhaustive as new events/commands are
// added. When you add a new wire message, add it here.
//
// A nil value means the message carries no data payload — ThreadEvent.Data /
// ThreadCommand.Data is null on the wire (e.g. event.agent_done,
// thread.close). A non-nil value is a zero value of the payload struct.

// EventTypes maps each daemon→client event discriminator to a zero value of its
// payload struct (nil = no payload).
var EventTypes = map[string]any{
	"event.thread_started":      EventThreadStarted{},
	"event.init_state":          EventInitState{},
	"event.stream_chunk":        EventStreamChunk{},
	"event.thinking_chunk":      EventThinkingChunk{},
	"event.stream_done":         EventStreamDone{},
	"event.compacted":           EventCompacted{},
	"event.tool_call":           EventToolCall{},
	"event.tool_result":         EventToolResult{},
	"event.confirm_request":     EventConfirmRequest{},
	"event.plan_proposed":       EventPlanProposed{},
	"event.plan_task_start":     EventPlanTaskStart{},
	"event.plan_task_done":      EventPlanTaskDone{},
	"event.plan_complete":       EventPlanComplete{},
	"event.user_question":       EventUserQuestion{},
	"event.error":               EventError{},
	"event.replay":              EventReplay{},
	"event.replay_ready":        EventReplayReady{},
	"event.title_updated":       EventTitleUpdated{},
	"event.retry":               EventRetry{},
	"event.thinking_stall":      EventThinkingStall{},
	"event.workflows_available": EventWorkflowsAvailable{},
	"event.skills_available":    EventSkillsAvailable{},
	"event.tool_backends":       EventToolBackends{},
	"event.update_available":    EventUpdateAvailable{},
	"event.job_run":             EventJobRun{},
	"event.job_done":            EventJobDone{},
	"event.threads_changed":     EventThreadsChanged{},
	"event.jobs_changed":        EventJobsChanged{},
	"event.mcp_changed":         EventMCPChanged{},
	"event.workflow_start":      EventWorkflowStart{},
	"event.workflow_step_start": EventWorkflowStepStart{},
	"event.workflow_step_done":  EventWorkflowStepDone{},
	"event.workflow_status":     EventWorkflowStatus{},
	"event.workflow_complete":   EventWorkflowComplete{},
	"event.todo_list_updated":   EventTodoListUpdated{},

	// Payload-less events (Data == null on the wire).
	"event.agent_done": nil,
	"event.clear":      nil,
	"event.quit":       nil,
}

// CommandTypes maps each client→daemon command discriminator to a zero value of
// its payload struct (nil = no payload).
var CommandTypes = map[string]any{
	"thread.start":            ThreadStartData{},
	"thread.input":            ThreadInputData{},
	"thread.workflow":         ThreadWorkflowData{},
	"thread.workflow_message": ThreadWorkflowMessageData{},
	"thread.confirm":          ThreadConfirmData{},
	"thread.plan_action":      ThreadPlanActionData{},
	"thread.user_answer":      ThreadUserAnswerData{},
	"thread.set_model":        ThreadSetModelData{},
	"thread.trim":             ThreadTrimData{},
	"thread.rename":           ThreadRenameData{},
	"instance.register":       InstanceRegisterData{},

	// Payload-less commands (Data == null on the wire).
	"thread.mark_read": nil,
	"thread.cancel":    nil,
	"thread.close":     nil,
	"update.quit":      nil,
}

// RPCTypes are the projection structs returned by one-shot RPCs (thread.list,
// job.list, hook.list). Unlike EventTypes/CommandTypes these are not envelope
// payloads keyed by a wire discriminator — they are keyed by their own type
// name — but they are part of the client-facing contract, so they are generated
// into the schema + Swift models and drift-gated alongside the wire types.
var RPCTypes = map[string]any{
	"ThreadSummary":    ThreadSummary{},
	"DirUsage":         DirUsage{},
	"JobSummary":       JobSummary{},
	"HookSummary":      HookSummary{},
	"MCPServerSummary": MCPServerSummary{},
}
