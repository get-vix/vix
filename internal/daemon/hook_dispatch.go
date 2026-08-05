package daemon

import (
	"context"
	"time"

	"github.com/get-vix/vix/internal/daemon/hooks"
)

// hooksReg returns the server's hook registry, or nil when hooks are disabled.
func (s *Thread) hooksReg() *hooks.Registry {
	if s.server == nil {
		return nil
	}
	return s.server.hookRegistry
}

// hooksActive reports whether any enabled hook listens for event in a context
// where firing is allowed. vix-initiated threads (jobs and hook runs, marked
// origin "vix") never fire hooks — this is the recursion guard that stops a
// hook's own tool calls from re-triggering hooks.
func (s *Thread) hooksActive(event string) bool {
	r := s.hooksReg()
	if r == nil {
		return false
	}
	if s.origin == "vix" {
		return false
	}
	return r.Has(event)
}

// buildHookContext assembles the common envelope every hook receives, plus the
// event-specific extras. Used as command-hook stdin JSON and as the text passed
// to workflow/prompt hooks.
func (s *Thread) buildHookContext(event string, extra map[string]any) map[string]any {
	m := map[string]any{
		"thread_id":       s.id,
		"hook_event_name": event,
		"cwd":             s.cwd,
		"model":           s.model,
		"permission_mode": s.permissionMode(),
		"origin":          s.originLabel(),
		"headless":        s.headless,
		"thread_mode":     s.threadMode,
		"agent":           s.chatAgent,
		"turn_count":      s.turnCount,
	}
	if s.server != nil {
		// Let a hook call back into this daemon (e.g. `vix thread create`)
		// without guessing the binary path or socket.
		m["vix_bin"] = s.server.vixBin
		m["socket_path"] = s.server.sockPath
	}
	if s.parentID != "" {
		m["parent_thread_id"] = s.parentID
	}
	if s.activeWorkflow != "" {
		m["active_workflow"] = s.activeWorkflow
	}
	if s.trigger != nil {
		m["trigger_type"] = s.trigger.Type
		if s.trigger.Ref != "" {
			m["trigger_ref"] = s.trigger.Ref
		}
	}
	if !s.startTime.IsZero() {
		m["started_at"] = s.startTime.UTC().Format(time.RFC3339)
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// originLabel renders the thread's provenance for hooks: user-started threads
// report "user", daemon-initiated ones report their origin ("vix").
func (s *Thread) originLabel() string {
	if s.origin == "" {
		return "user"
	}
	return s.origin
}

// permissionMode derives a Claude-Code-style permission mode from the thread's
// automatic-permission flags, so hooks can gate on how autonomous the run is.
func (s *Thread) permissionMode() string {
	s.mu.Lock()
	plan := s.activePlan != nil
	s.mu.Unlock()
	switch {
	case plan:
		return "plan"
	case s.headless && s.enableAutomaticWritePermission && s.enableAutomaticDirectoryAccess:
		return "bypass"
	case s.enableAutomaticWritePermission:
		return "acceptEdits"
	default:
		return "default"
	}
}

// preToolUseHook fires PreToolUse hooks before a tool runs. It returns the
// rewritten input (modify), a deny reason (when a blocking hook vetoes), and a
// denied flag. Async hooks are fired fire-and-forget.
func (s *Thread) preToolUseHook(ctx context.Context, name string, input map[string]any) (newInput map[string]any, denyReason string, denied bool) {
	if !s.hooksActive(hooks.EventPreToolUse) {
		return nil, "", false
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventPreToolUse, name)
	if len(syncHooks)+len(asyncHooks) == 0 {
		return nil, "", false
	}
	base := s.buildHookContext(hooks.EventPreToolUse, map[string]any{
		"tool_name":  name,
		"tool_input": snapshotInput(input),
	})
	for _, h := range asyncHooks {
		s.server.fireAsyncHook(h, base)
	}
	var decisions []hooks.Decision
	for _, h := range syncHooks {
		decisions = append(decisions, s.server.runSyncHook(ctx, h, base))
	}
	dec := hooks.Combine(decisions)
	switch dec.Behavior {
	case hooks.BehaviorDeny:
		reason := dec.Reason
		if reason == "" {
			reason = "blocked by hook"
		}
		return nil, reason, true
	case hooks.BehaviorModify:
		return dec.Input, "", false
	}
	return nil, "", false
}

// postToolUseHook fires PostToolUse hooks after a tool completes. Sync hooks may
// append context to the tool result shown to the model; async hooks fire
// fire-and-forget. Side effects of the tool cannot be undone here.
func (s *Thread) postToolUseHook(ctx context.Context, name string, input map[string]any, result *ToolResult) {
	if result == nil || !s.hooksActive(hooks.EventPostToolUse) {
		return
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventPostToolUse, name)
	if len(syncHooks)+len(asyncHooks) == 0 {
		return
	}
	base := s.buildHookContext(hooks.EventPostToolUse, map[string]any{
		"tool_name":     name,
		"tool_input":    snapshotInput(input),
		"tool_response": result.Output,
		"is_error":      result.IsError,
	})
	for _, h := range asyncHooks {
		s.server.fireAsyncHook(h, base)
	}
	var decisions []hooks.Decision
	for _, h := range syncHooks {
		decisions = append(decisions, s.server.runSyncHook(ctx, h, base))
	}
	if dec := hooks.Combine(decisions); dec.Context != "" {
		result.Output = result.Output + "\n\n[hook] " + dec.Context
	}
}

// userPromptSubmitHook fires UserPromptSubmit hooks before a prompt is added to
// the conversation. It returns the (possibly rewritten) text, a deny reason
// when a blocking hook vetoes, and a denied flag.
func (s *Thread) userPromptSubmitHook(ctx context.Context, text string) (newText, denyReason string, denied bool) {
	if !s.hooksActive(hooks.EventUserPromptSubmit) {
		return text, "", false
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventUserPromptSubmit, "")
	if len(syncHooks)+len(asyncHooks) == 0 {
		return text, "", false
	}
	base := s.buildHookContext(hooks.EventUserPromptSubmit, map[string]any{"prompt": text})
	for _, h := range asyncHooks {
		s.server.fireAsyncHook(h, base)
	}
	var decisions []hooks.Decision
	for _, h := range syncHooks {
		decisions = append(decisions, s.server.runSyncHook(ctx, h, base))
	}
	dec := hooks.Combine(decisions)
	switch dec.Behavior {
	case hooks.BehaviorDeny:
		reason := dec.Reason
		if reason == "" {
			reason = "blocked by hook"
		}
		return text, reason, true
	case hooks.BehaviorModify:
		if np, ok := dec.Input["prompt"].(string); ok {
			return np, "", false
		}
	}
	return text, "", false
}

// announceStart rebuilds the resumed client's viewport and fires ThreadStart
// hooks, classified by source. The resume check must be captured before
// emitReplay runs, because emitReplay clears attachRecord — reordering these
// would misclassify every resumed thread as "startup" (and inflate any
// startup-gated hook, e.g. the feedback counter).
func (s *Thread) announceStart() {
	resumed := s.attachRecord != nil
	s.emitReplay()
	source := "startup"
	if resumed {
		source = "resume"
	}
	s.fireThreadStart(source)
}

// fireThreadStart fires ThreadStart hooks (observational, fire-and-forget).
func (s *Thread) fireThreadStart(source string) {
	if !s.hooksActive(hooks.EventThreadStart) {
		return
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventThreadStart, source)
	base := s.buildHookContext(hooks.EventThreadStart, map[string]any{"source": source})
	for _, h := range append(syncHooks, asyncHooks...) {
		s.server.fireAsyncHook(h, base)
	}
}

// fireStop fires Stop hooks when a turn finishes (observational, fire-and-forget).
func (s *Thread) fireStop() {
	if !s.hooksActive(hooks.EventStop) {
		return
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventStop, "")
	base := s.buildHookContext(hooks.EventStop, nil)
	for _, h := range append(syncHooks, asyncHooks...) {
		s.server.fireAsyncHook(h, base)
	}
}

// fireSubagentStart fires SubagentStart hooks when a subagent is spawned
// (observational, fire-and-forget). agentID correlates this fire with the
// matching SubagentStop; agentType is the matcher field.
func (s *Thread) fireSubagentStart(agentType, agentID, prompt string) {
	if !s.hooksActive(hooks.EventSubagentStart) {
		return
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventSubagentStart, agentType)
	base := s.buildHookContext(hooks.EventSubagentStart, map[string]any{
		"agent_type": agentType,
		"agent_id":   agentID,
		"prompt":     prompt,
	})
	for _, h := range append(syncHooks, asyncHooks...) {
		s.server.fireAsyncHook(h, base)
	}
}

// fireSubagentStop fires SubagentStop hooks when a subagent finishes
// (observational, fire-and-forget). result is nil when the subagent produced
// none (e.g. an early error).
func (s *Thread) fireSubagentStop(agentType, agentID string, result *SubagentResult) {
	if !s.hooksActive(hooks.EventSubagentStop) {
		return
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventSubagentStop, agentType)
	extra := map[string]any{
		"agent_type": agentType,
		"agent_id":   agentID,
	}
	if result != nil {
		extra["result"] = result.Output
		extra["is_error"] = result.IsError
	}
	base := s.buildHookContext(hooks.EventSubagentStop, extra)
	for _, h := range append(syncHooks, asyncHooks...) {
		s.server.fireAsyncHook(h, base)
	}
}

// firePreCompact fires PreCompact hooks just before the conversation prefix is
// summarized (observational, fire-and-forget). trigger is "auto" or "manual".
func (s *Thread) firePreCompact(trigger string) {
	if !s.hooksActive(hooks.EventPreCompact) {
		return
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventPreCompact, trigger)
	base := s.buildHookContext(hooks.EventPreCompact, map[string]any{"trigger": trigger})
	for _, h := range append(syncHooks, asyncHooks...) {
		s.server.fireAsyncHook(h, base)
	}
}

// firePostCompact fires PostCompact hooks after a successful compaction
// (observational, fire-and-forget). trigger is "auto" or "manual".
func (s *Thread) firePostCompact(trigger string, summarizedTurns int, fromTokens int64) {
	if !s.hooksActive(hooks.EventPostCompact) {
		return
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventPostCompact, trigger)
	base := s.buildHookContext(hooks.EventPostCompact, map[string]any{
		"trigger":          trigger,
		"summarized_turns": summarizedTurns,
		"from_tokens":      fromTokens,
	})
	for _, h := range append(syncHooks, asyncHooks...) {
		s.server.fireAsyncHook(h, base)
	}
}

// permissionRequestHook fires PermissionRequest hooks just before the user is
// asked to confirm a tool call. A blocking hook may deny (skip the prompt and
// reject the tool); allow / no-opinion falls through to the normal prompt.
// Async hooks fire fire-and-forget. Returns a deny reason and a denied flag.
func (s *Thread) permissionRequestHook(ctx context.Context, name string, input map[string]any, requestedDirs []string) (denyReason string, denied bool) {
	if !s.hooksActive(hooks.EventPermissionRequest) {
		return "", false
	}
	syncHooks, asyncHooks := s.hooksReg().Match(hooks.EventPermissionRequest, name)
	if len(syncHooks)+len(asyncHooks) == 0 {
		return "", false
	}
	extra := map[string]any{
		"tool_name":  name,
		"tool_input": snapshotInput(input),
	}
	if len(requestedDirs) > 0 {
		extra["requested_dirs"] = requestedDirs
	}
	base := s.buildHookContext(hooks.EventPermissionRequest, extra)
	for _, h := range asyncHooks {
		s.server.fireAsyncHook(h, base)
	}
	var decisions []hooks.Decision
	for _, h := range syncHooks {
		decisions = append(decisions, s.server.runSyncHook(ctx, h, base))
	}
	if dec := hooks.Combine(decisions); dec.Behavior == hooks.BehaviorDeny {
		reason := dec.Reason
		if reason == "" {
			reason = "blocked by hook"
		}
		return reason, true
	}
	return "", false
}
