package scenarios

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Lifecycle-hooks e2e scenarios. Hooks are enabled by default (the harness only
// disables jobs), so each test seeds one or more ~/.vix/hooks/<id>/hook.json
// specs and drives the model into the matching event. Hook commands use
// jq/grep/echo/touch (all present in the e2e image) the way real hooks are
// written.
//
// Coverage: every event (PreToolUse, PostToolUse, UserPromptSubmit, ThreadStart,
// Stop, PreCompact, PostCompact, SubagentStart, SubagentStop, PermissionRequest),
// every decision (deny, modify, context), both modes (sync, async), the
// command and prompt forms, plus matcher scoping, multi-hook combine
// (most-restrictive-wins), the blocking gate, and the kill switch.

// blockWriteHook is a blocking PreToolUse hook: it parses the tool-input path
// with jq and denies (exit 2) any write to "blocked.txt" — the same idiom the
// docs' protect-files example uses.
const blockWriteHook = `{
  "id": "block-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "p=$(jq -r .tool_input.path); case \"$p\" in *blocked.txt*) echo 'blocked by hook' >&2; exit 2;; esac"
}`

// TestHookBlocksWrite proves a blocking PreToolUse hook vetoes a tool call: the
// file is never written and the deny reason is fed back to the model.
func TestHookBlocksWrite(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.pre_tool_deny",
		Description: "a blocking PreToolUse hook denies a write_file; the file never lands and the reason reaches the model",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/block-write/hook.json", blockWriteHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"blocked.txt","content":"nope"}`),
		harness.Text("I could not write that file."),
	)
	h.UI.Type("write blocked.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // denied write turn + final turn
	h.UI.Shot("after-deny")

	if h.FS.Exists("blocked.txt") {
		t.Fatalf("hook deny breached: blocked.txt was written")
	}
	if !anyToolResultContains(h, "blocked by hook") {
		t.Fatalf("deny reason did not reach the model (requests=%d)", len(h.Mock.Requests()))
	}
}

// postContextHook is a non-blocking sync PostToolUse hook on bash: its stdout
// text is injected into the tool result the model sees next.
const postContextHook = `{
  "id": "post-ctx",
  "enabled": true,
  "mode": "sync",
  "trigger": { "event": "PostToolUse", "matcher": "bash" },
  "command": "echo HOOK_SAW_BASH"
}`

// TestHookPostToolUseContext proves a PostToolUse hook can append context to a
// tool result, which then flows to the model on the wire.
func TestHookPostToolUseContext(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.post_tool_context",
		Description: "a PostToolUse hook appends context to a bash result; the note reaches the model",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/post-ctx/hook.json", postContextHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo hi"}`),
		harness.Text("Done."),
	)
	h.UI.Type("run echo hi")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // bash turn + final turn
	h.UI.Shot("after-bash")

	if !anyToolResultContains(h, "HOOK_SAW_BASH") {
		t.Fatalf("PostToolUse context not injected into the tool result (requests=%d)", len(h.Mock.Requests()))
	}
}

// vetoPromptHook is a blocking UserPromptSubmit hook that denies any prompt
// containing "forbidden" (grep over the context JSON on stdin).
const vetoPromptHook = `{
  "id": "veto-prompt",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "UserPromptSubmit" },
  "command": "if grep -q forbidden; then echo 'policy violation' >&2; exit 2; fi"
}`

// TestHookVetoesPrompt proves a blocking UserPromptSubmit hook aborts the turn
// before any LLM request is made.
func TestHookVetoesPrompt(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.prompt_veto",
		Description: "a blocking UserPromptSubmit hook aborts the turn before the model is ever called",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/veto-prompt/hook.json", vetoPromptHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.UI.Type("please do the forbidden thing")
	h.UI.Enter()
	h.UI.WaitFor("blocked by hook")
	h.UI.Shot("prompt-vetoed")

	if n := len(h.Mock.Requests()); n != 0 {
		t.Fatalf("a vetoed prompt must not reach the model, got %d request(s)", n)
	}
}

// TestHookKillSwitch proves VIX_DISABLE_HOOKS=1 turns the engine off: the same
// blocking hook is inert and the write succeeds.
func TestHookKillSwitch(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.kill_switch",
		Description: "VIX_DISABLE_HOOKS=1 disables hooks; a would-be blocking hook does nothing",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/hooks/block-write/hook.json", blockWriteHook),
		harness.WithEnv("VIX_DISABLE_HOOKS", "1"),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"blocked.txt","content":"data"}`),
		harness.Text("Wrote blocked.txt."),
	)
	h.UI.Type("write blocked.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote blocked.txt")
	h.UI.Shot("kill-switch")

	if got := string(h.FS.Read("blocked.txt")); got != "data" {
		t.Fatalf("with hooks disabled the write should land, got %q", got)
	}
}

// ── PreToolUse: modify (rewrite tool input) ─────────────────────────────────

// rewriteWriteHook rewrites a write_file's path to safe.txt via a modify
// decision (jq builds the replacement input from the original tool_input).
const rewriteWriteHook = `{
  "id": "rewrite-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "jq -c '{behavior:\"modify\", input:(.tool_input | .path=\"safe.txt\")}'"
}`

// TestHookRewritesToolInput proves a blocking PreToolUse hook can rewrite a
// tool call: the write lands at the hook-chosen path, not the model's.
func TestHookRewritesToolInput(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.pre_tool_modify",
		Description: "a PreToolUse hook rewrites write_file's path; the write lands at the hook-chosen path",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/rewrite-write/hook.json", rewriteWriteHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"danger.txt","content":"payload"}`),
		harness.Text("Wrote the file."),
	)
	h.UI.Type("write danger.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote the file")
	h.UI.Shot("after-rewrite")

	if h.FS.Exists("danger.txt") {
		t.Fatalf("modify hook failed: original path danger.txt was written")
	}
	if got := string(h.FS.Read("safe.txt")); got != "payload" {
		t.Fatalf("rewritten path safe.txt = %q, want %q", got, "payload")
	}
}

// ── UserPromptSubmit: modify (rewrite prompt) ───────────────────────────────

const rewritePromptHook = `{
  "id": "rewrite-prompt",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "UserPromptSubmit" },
  "command": "jq -c '{behavior:\"modify\", input:{prompt:\"REWRITTEN_BY_HOOK\"}}'"
}`

// TestHookRewritesPrompt proves a blocking UserPromptSubmit hook can rewrite the
// prompt before it reaches the model.
func TestHookRewritesPrompt(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.prompt_modify",
		Description: "a UserPromptSubmit hook rewrites the prompt; the model sees the rewritten text",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/rewrite-prompt/hook.json", rewritePromptHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("ok"))
	h.UI.Type("the original user request")
	h.UI.Enter()
	h.WaitForLLMRequests(1)
	h.UI.Shot("after-prompt-rewrite")

	if !anyRequestBodyContains(h, "REWRITTEN_BY_HOOK") {
		t.Fatalf("rewritten prompt did not reach the model")
	}
	if anyRequestBodyContains(h, "the original user request") {
		t.Fatalf("original prompt leaked to the model despite the rewrite")
	}
}

// ── Blocking gate: a non-blocking sync hook cannot veto ─────────────────────

// warnWriteHook exits 2 (a deny) but is NOT blocking, so its veto is downgraded
// and the tool proceeds.
const warnWriteHook = `{
  "id": "warn-write",
  "enabled": true,
  "mode": "sync",
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "echo 'just a warning' >&2; exit 2"
}`

// TestHookNonBlockingDoesNotVeto proves a sync hook without "blocking": true
// cannot stop a tool — its exit-2 is downgraded and the write lands.
func TestHookNonBlockingDoesNotVeto(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.non_blocking",
		Description: "a non-blocking sync hook that exits 2 cannot veto; the write still lands",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/warn-write/hook.json", warnWriteHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"kept.txt","content":"kept"}`),
		harness.Text("Wrote kept.txt."),
	)
	h.UI.Type("write kept.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote kept.txt")
	h.UI.Shot("non-blocking")

	if got := string(h.FS.Read("kept.txt")); got != "kept" {
		t.Fatalf("non-blocking hook should not veto; kept.txt = %q", got)
	}
}

// ── Matcher scoping: a hook only fires for its matched tool ──────────────────

// editOnlyBlockHook blocks edit_file only; a write_file must slip past it.
const editOnlyBlockHook = `{
  "id": "edit-only-block",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "edit_file" },
  "command": "exit 2"
}`

// TestHookMatcherScopesToTool proves the matcher scopes a hook to specific
// tools: an edit_file-scoped block leaves write_file untouched.
func TestHookMatcherScopesToTool(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.matcher_scope",
		Description: "a PreToolUse hook scoped to edit_file does not fire for write_file",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/edit-only-block/hook.json", editOnlyBlockHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"unscoped.txt","content":"ok"}`),
		harness.Text("Wrote unscoped.txt."),
	)
	h.UI.Type("write unscoped.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote unscoped.txt")
	h.UI.Shot("matcher-scope")

	if got := string(h.FS.Read("unscoped.txt")); got != "ok" {
		t.Fatalf("write_file should be unaffected by an edit_file-only hook; got %q", got)
	}
}

// ── Combine: most-restrictive-wins across multiple matching hooks ────────────

const allowWriteHook = `{
  "id": "allow-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "exit 0"
}`

const denyWriteHook = `{
  "id": "deny-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "echo 'denied by policy' >&2; exit 2"
}`

// TestHookMostRestrictiveWins proves that when two hooks match one tool, a deny
// beats an allow.
func TestHookMostRestrictiveWins(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.combine",
		Description: "two PreToolUse hooks match one write; the deny wins over the allow",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/hooks/allow-write/hook.json", allowWriteHook),
		harness.WithHomeFile(".vix/hooks/deny-write/hook.json", denyWriteHook),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"combined.txt","content":"x"}`),
		harness.Text("Could not write."),
	)
	h.UI.Type("write combined.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(2)
	h.UI.Shot("combine-deny-wins")

	if h.FS.Exists("combined.txt") {
		t.Fatalf("most-restrictive-wins failed: combined.txt was written despite a deny")
	}
	if !anyToolResultContains(h, "denied by policy") {
		t.Fatalf("deny reason did not reach the model")
	}
}

// ── async mode: fire-and-forget out of band ─────────────────────────────────

const asyncPostHook = `{
  "id": "async-post",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "PostToolUse", "matcher": "bash" },
  "command": "touch hooked-async.flag"
}`

// TestHookAsyncFireAndForget proves an async hook runs out of band: the turn
// completes and the hook's side effect appears shortly after.
func TestHookAsyncFireAndForget(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.async",
		Description: "an async PostToolUse hook runs fire-and-forget; its side effect lands out of band",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/async-post/hook.json", asyncPostHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo hi"}`),
		harness.Text("Done."),
	)
	h.UI.Type("run echo hi")
	h.UI.Enter()
	h.WaitForLLMRequests(2)
	h.UI.Shot("after-async")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("hooked-async.flag") }) {
		t.Fatalf("async hook side effect (hooked-async.flag) never appeared")
	}
}

// ── ThreadStart event ──────────────────────────────────────────────────────

const threadStartHook = `{
  "id": "thread-start",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "ThreadStart" },
  "command": "touch threadstart.flag"
}`

// TestHookThreadStartFires proves a ThreadStart hook runs when the thread
// begins. Under the draft-thread model a thread starts on the first message,
// so sending one commits the draft and fires the hook.
func TestHookThreadStartFires(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.thread_start",
		Description: "a ThreadStart hook fires when the thread begins",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/thread-start/hook.json", threadStartHook))

	h.UI.WaitStable(400 * time.Millisecond)
	startThread(h)
	h.UI.Shot("thread-start")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("threadstart.flag") }) {
		t.Fatalf("ThreadStart hook never fired (threadstart.flag missing)")
	}
}

// ── Stop event ──────────────────────────────────────────────────────────────

const stopHook = `{
  "id": "stop",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "Stop" },
  "command": "touch stop.flag"
}`

// TestHookStopFires proves a Stop hook fires when a turn finishes.
func TestHookStopFires(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.stop",
		Description: "a Stop hook fires when a turn completes",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/stop/hook.json", stopHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("All done."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("All done.")
	h.UI.Shot("after-stop")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("stop.flag") }) {
		t.Fatalf("Stop hook never fired (stop.flag missing)")
	}
}

// ── recent-run history (state.json) ──────────────────────────────────────────

// recentRunsHook is a sync PostToolUse hook on bash; each fire is appended to
// the hook's recent-run history (~/.vix/hooks/<id>/state.json).
const recentRunsHook = `{
  "id": "rec-runs",
  "enabled": true,
  "mode": "sync",
  "trigger": { "event": "PostToolUse", "matcher": "bash" },
  "command": "echo noted"
}`

// TestHookRecentRunsHistory proves a hook fire is recorded in its per-hook
// state.json under recent_runs, carrying the event and async flag.
func TestHookRecentRunsHistory(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.recent_runs",
		Description: "a hook fire is appended to its state.json recent_runs history",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/rec-runs/hook.json", recentRunsHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo hi"}`),
		harness.Text("Done."),
	)
	h.UI.Type("run echo hi")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // bash turn + final turn

	statePath := h.HomePath(".vix/hooks/rec-runs/state.json")
	var state string
	if !pollUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		if err != nil {
			return false
		}
		state = string(b)
		return strings.Contains(state, `"recent_runs"`) &&
			strings.Contains(state, `"event": "PostToolUse"`) &&
			strings.Contains(state, `"async": false`)
	}) {
		t.Fatalf("hook recent_runs missing or incomplete at %s; got:\n%s\nvixd log:\n%s",
			statePath, state, h.Daemon.LogTail(80))
	}

	h.UI.Shot("hook-recent-runs")
}

// promptFormHook is a sync blocking PreToolUse hook whose decision comes from an
// LLM turn (run in an isolated thread). The model answers with the BLOCK:
// sentinel, which the engine reads as a deny.
const promptFormHook = `{
  "id": "prompt-form",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "timeout": "30s",
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "prompt": "Reply with exactly: BLOCK: not allowed"
}`

// TestHookPromptFormBlocks proves the prompt/workflow execution form: a sync
// blocking hook runs an isolated LLM turn whose BLOCK: output vetoes the tool.
// The mock serves the hook turn's reply between the two main-turn requests.
func TestHookPromptFormBlocks(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.prompt_form",
		Description: "a sync blocking prompt-form hook runs an LLM turn whose BLOCK: output vetoes the write",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/prompt-form/hook.json", promptFormHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"llm-blocked.txt","content":"x"}`), // main turn 1
		harness.Text("BLOCK: not allowed"),                                        // hook turn
		harness.Text("Could not write."),                                          // main turn 2
	)
	h.UI.Type("write llm-blocked.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(3)
	h.UI.Shot("prompt-form-block")

	if h.FS.Exists("llm-blocked.txt") {
		t.Fatalf("prompt-form hook failed to block: llm-blocked.txt was written")
	}
}

// ── SubagentStop event ───────────────────────────────────────────────────────

// subagentStopHook touches a marker when a "helper" subagent finishes.
const subagentStopHook = `{
  "id": "subagent-stop",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "SubagentStop", "matcher": "helper" },
  "command": "touch subagent-stopped.flag"
}`

// helperAgent is a minimal custom agent the model can delegate to via spawn_agent.
const helperAgent = `---
name: helper
tools: bash
---
You are a helper agent.`

// TestHookSubagentStopFires proves a SubagentStop hook fires when a spawned
// subagent finishes, scoped by agent type.
func TestHookSubagentStopFires(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.subagent_stop",
		Description: "a SubagentStop hook fires when a spawned subagent finishes",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/hooks/subagent-stop/hook.json", subagentStopHook),
		harness.WithHomeFile(".vix/agents/helper.md", helperAgent),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("spawn_agent", `{"agent_type":"helper","prompt":"do a small task"}`), // parent turn 1
		harness.Text("Subagent finished its work."),                                          // subagent turn
		harness.Text("All done."),                                                            // parent turn 2
	)
	h.UI.Type("delegate to the helper agent")
	h.UI.Enter()
	h.UI.WaitFor("All done.")
	h.UI.Shot("after-subagent")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("subagent-stopped.flag") }) {
		t.Fatalf("SubagentStop hook never fired (subagent-stopped.flag missing)")
	}
}

// ── PreCompact / PostCompact events ──────────────────────────────────────────

const preCompactHook = `{
  "id": "pre-compact",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "PreCompact" },
  "command": "touch precompact.flag"
}`

const postCompactHook = `{
  "id": "post-compact",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "PostCompact" },
  "command": "touch postcompact.flag"
}`

// TestHookCompactionFires proves PreCompact and PostCompact hooks fire around a
// manual /compact (which summarizes the dropped prefix in one LLM call).
func TestHookCompactionFires(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.compact",
		Description: "PreCompact and PostCompact hooks fire around a /compact",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/hooks/pre-compact/hook.json", preCompactHook),
		harness.WithHomeFile(".vix/hooks/post-compact/hook.json", postCompactHook),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("Turn one."))
	h.UI.Type("first message")
	h.UI.Enter()
	h.UI.WaitFor("Turn one.")

	h.Mock.Enqueue(harness.Text("Turn two."))
	h.UI.Type("second message")
	h.UI.Enter()
	h.UI.WaitFor("Turn two.")
	h.UI.WaitStable(300 * time.Millisecond)

	// /compact 1 drops the first turn and summarizes it (one summarization call).
	h.Mock.Enqueue(harness.Text("COMPACTION_SUMMARY"))
	h.UI.Type("/compact 1")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Key("esc")
	h.UI.WaitStable(200 * time.Millisecond)
	h.UI.Enter()
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-compact")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("precompact.flag") }) {
		t.Fatalf("PreCompact hook never fired (precompact.flag missing)")
	}
	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("postcompact.flag") }) {
		t.Fatalf("PostCompact hook never fired (postcompact.flag missing)")
	}
}

// ── PermissionRequest event ──────────────────────────────────────────────────

// permDenyHook is a blocking PermissionRequest hook that denies any write_file
// confirmation, so the user is never prompted and the tool is rejected.
const permDenyHook = `{
  "id": "perm-deny",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PermissionRequest", "matcher": "write_file" },
  "command": "echo 'writes require a ticket' >&2; exit 2"
}`

// TestHookPermissionRequestDenies proves a blocking PermissionRequest hook
// short-circuits the confirmation prompt: the write never lands and the deny
// reason reaches the model.
func TestHookPermissionRequestDenies(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.permission_request",
		Description: "a blocking PermissionRequest hook denies a write confirmation; the file never lands and the reason reaches the model",
		Wire:        harness.WireMessages,
	},
		// write_file must surface a confirmation prompt for a PermissionRequest
		// hook to fire; the harness default runs with automatic write permission.
		harness.WithVixArgs("-disable-automatic-write-permission"),
		harness.WithHomeFile(".vix/hooks/perm-deny/hook.json", permDenyHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"needs-perm.txt","content":"x"}`),
		harness.Text("I could not write that file."),
	)
	h.UI.Type("write needs-perm.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // denied write turn + final turn
	h.UI.Shot("perm-denied")

	if h.FS.Exists("needs-perm.txt") {
		t.Fatalf("permission hook deny breached: needs-perm.txt was written")
	}
	if !anyToolResultContains(h, "writes require a ticket") {
		t.Fatalf("permission deny reason did not reach the model (requests=%d)", len(h.Mock.Requests()))
	}
}
