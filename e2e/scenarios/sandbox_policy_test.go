package scenarios

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Tier 2 — security & policy surface. These pin the deny_list / sandbox
// guarantees. They assert on the wire (a secret never reaches the model) and on
// the tool_result fed back (the refusal), so they're deterministic and
// independent of TUI rendering. All run on the Messages wire (policy is
// wire-agnostic). Deny enforcement lives at thread.go:1227 (checkDenyList) and
// tools.go:1337/1397 (filterOutputAgainstDeny).

// requestsLeak reports whether any request body carried the sentinel — the core
// "secret never reached the model" assertion.
func requestsLeak(h *harness.Harness, sentinel string) bool {
	for _, r := range h.Mock.Requests() {
		if strings.Contains(string(r.Body()), sentinel) {
			return true
		}
	}
	return false
}

// toolResultForUser returns the tool result produced for the turn started by the
// given user text (the first non-empty one), so a scenario can inspect a
// specific command's outcome rather than "any" tool result.
func toolResultForUser(h *harness.Harness, userText string) (string, bool) {
	for _, r := range h.Mock.Requests() {
		if r.LastUserText() == userText {
			if tr := r.LastToolResult(); tr != "" {
				return tr, true
			}
		}
	}
	return "", false
}

// sandboxCannotExec reports whether bash failed to launch under the kernel
// sandbox (bubblewrap can't create a namespace in some containers). When true, a
// command's *output* can't be asserted — only that it wasn't deny-refused.
func sandboxCannotExec(h *harness.Harness) bool {
	for _, r := range h.Mock.Requests() {
		tr := r.LastToolResult()
		if strings.Contains(tr, "Creating new namespace failed") ||
			strings.Contains(tr, "bwrap:") {
			return true
		}
	}
	return false
}

// TestDenyListBlocksPathTools proves the deny_list refuses every path-taking
// file tool before execution — extending the existing bash-only deny test
// (sandbox.deny_list) across read_file, edit_file and delete_file. T2.1.
func TestDenyListBlocksPathTools(t *testing.T) {
	const sentinel = "TOPSECRET_T21_VALUE"

	cases := []struct {
		name    string
		args    string
		toolUse string
		// leakCheck asserts the secret never reached the wire — only meaningful
		// for read ops (edit/delete don't read the content; putting the sentinel
		// in an edit's old_string would itself appear in the tool-call args).
		leakCheck bool
		// after asserts the protected file's invariant after the refusal.
		after func(t *testing.T, h *harness.Harness)
	}{
		{
			name:      "read_file",
			toolUse:   "read_file",
			args:      `{"path":"secret.txt"}`,
			leakCheck: true,
			after:     func(t *testing.T, h *harness.Harness) {}, // content invariant checked via wire
		},
		{
			name:    "edit_file",
			toolUse: "edit_file",
			// old_string deliberately is NOT the sentinel: the deny fires before
			// the edit runs, and we don't want the sentinel echoed in tool args.
			args: `{"path":"secret.txt","old_string":"PLACEHOLDER_NOT_IN_FILE","new_string":"CHANGED"}`,
			after: func(t *testing.T, h *harness.Harness) {
				if got := string(h.FS.Read("secret.txt")); !strings.Contains(got, sentinel) {
					t.Fatalf("secret.txt was edited despite deny_list: %q", got)
				}
			},
		},
		{
			name:    "delete_file",
			toolUse: "delete_file",
			args:    `{"path":"secret.txt"}`,
			after: func(t *testing.T, h *harness.Harness) {
				if !h.FS.Exists("secret.txt") {
					t.Fatal("secret.txt was deleted despite deny_list")
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := harness.Start(t, harness.Meta{
				Category:    "sandbox",
				Subcategory: "sandbox.deny_paths",
				Description: "deny_list refuses " + c.name + " before execution; the secret never reaches the model",
				Wire:        harness.WireMessages,
				Variant:     c.name,
			}, harness.WithDenyPath("secret.txt"))

			if err := h.FS.Write("secret.txt", sentinel+"\n"); err != nil {
				t.Fatalf("seed secret.txt: %v", err)
			}
			h.UI.WaitStable(400 * time.Millisecond)

			h.Mock.Enqueue(
				harness.ToolUse(c.toolUse, c.args),
				harness.Text("The operation was blocked."),
			)
			h.UI.Type("touch the protected file")
			h.UI.Enter()
			h.UI.ResolveToolPrompts("The operation was blocked.")
			h.UI.Shot("after-deny-" + c.name)

			if c.leakCheck && requestsLeak(h, sentinel) {
				t.Fatalf("deny_list breach: secret leaked to the model via %s", c.name)
			}
			if !anyToolResultContains(h, "blocked by deny_list") {
				t.Fatalf("no deny_list refusal returned to the model for %s", c.name)
			}
			c.after(t, h)
		})
	}
}

// TestDenyListRelativeEntryBlocksProjectRoot pins the fix for issue #52: a
// RELATIVE deny_list.paths entry declared in ./.vix/settings.json must block the
// matching file at the PROJECT ROOT — not just the phantom ./.vix/<entry> that
// config-dir-relative resolution produces. Before the fix the deny entry was
// silently anchored under .vix/, so the secret at the workdir root was read and
// leaked to the model despite being "denied".
func TestDenyListRelativeEntryBlocksProjectRoot(t *testing.T) {
	const sentinel = "TOPSECRET_ISSUE52_VALUE"

	h := harness.Start(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.deny_paths",
		Description: "a relative deny_list.paths entry in ./.vix/settings.json blocks the file at the project root (issue #52)",
		Wire:        harness.WireMessages,
		Variant:     "relative-project-root",
	}, harness.WithSettings(`{"deny_list":{"paths":[".envrc.private"]}}`))

	if err := h.FS.Write(".envrc.private", "API_KEY="+sentinel+"\n"); err != nil {
		t.Fatalf("seed .envrc.private: %v", err)
	}
	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("read_file", `{"path":".envrc.private"}`),
		harness.Text("The operation was blocked."),
	)
	h.UI.Type("read the private env file")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("The operation was blocked.")
	h.UI.Shot("after-deny-relative")

	if requestsLeak(h, sentinel) {
		t.Fatal("deny_list breach: secret leaked to the model despite relative deny entry (issue #52)")
	}
	if !anyToolResultContains(h, "blocked by deny_list") {
		t.Fatal("no deny_list refusal returned to the model for the relative entry")
	}
}

// TestDenyListFiltersSearchOutput proves grep/glob silently drop matches that
// live inside a denied directory — the match (and any secret on it) never
// reaches the model, and the result is filtered, not errored. T2.3.
func TestDenyListFiltersSearchOutput(t *testing.T) {
	const sentinel = "LEAKED_T23_SENTINEL"

	t.Run("grep", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category:    "sandbox",
			Subcategory: "sandbox.search_filter",
			Description: "grep matches inside a denied dir are filtered out of the tool result",
			Wire:        harness.WireMessages,
			Variant:     "grep",
		}, harness.WithDenyPath("vault"))

		mustSeed(t, h, "vault/secret.txt", "NEEDLE "+sentinel+"\n")
		mustSeed(t, h, "public.txt", "NEEDLE public-line\n")
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("grep", `{"pattern":"NEEDLE","path":".","reason":"e2e"}`),
			harness.Text("Searched for NEEDLE."),
		)
		h.UI.Type("grep for NEEDLE")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("Searched for NEEDLE.")
		h.UI.Shot("after-grep")

		if requestsLeak(h, sentinel) {
			t.Fatal("deny_list breach: a denied-dir match leaked to the model via grep")
		}
		if !anyToolResultContains(h, "public.txt") {
			t.Fatalf("allowed grep match was lost; requests=%d", len(h.Mock.Requests()))
		}
		if anyToolResultContains(h, "vault") {
			t.Fatal("a denied-dir path appeared in the grep result")
		}
	})

	t.Run("glob", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category:    "sandbox",
			Subcategory: "sandbox.search_filter",
			Description: "glob entries inside a denied dir are filtered out of the tool result",
			Wire:        harness.WireMessages,
			Variant:     "glob",
		}, harness.WithDenyPath("vault"))

		mustSeed(t, h, "vault/secret.txt", "x\n")
		mustSeed(t, h, "public.txt", "y\n")
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("glob_files", `{"pattern":["**/*.txt"],"reason":"e2e"}`),
			harness.Text("Globbed the txt files."),
		)
		h.UI.Type("list all txt files")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("Globbed the txt files.")
		h.UI.Shot("after-glob")

		if !anyToolResultContains(h, "public.txt") {
			t.Fatalf("allowed glob entry was lost; requests=%d", len(h.Mock.Requests()))
		}
		if anyToolResultContains(h, "vault/secret.txt") {
			t.Fatal("a denied-dir path appeared in the glob result")
		}
	})
}

// TestDenyListBlocksURLs proves URL deny entries refuse both web_fetch and a
// bash command referencing the URL, before any network access. T2.2.
func TestDenyListBlocksURLs(t *testing.T) {
	const settings = `{"deny_list":{"urls":["blocked.example.com"]}}`

	t.Run("web_fetch", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category:    "sandbox",
			Subcategory: "sandbox.deny_urls",
			Description: "web_fetch of a denied host is refused before any fetch",
			Wire:        harness.WireMessages,
			Variant:     "web_fetch",
		}, harness.WithSettings(settings))

		h.UI.WaitStable(400 * time.Millisecond)
		h.Mock.Enqueue(
			harness.ToolUse("web_fetch", `{"url":"https://blocked.example.com/secret"}`),
			harness.Text("That URL is blocked."),
		)
		h.UI.Type("fetch the blocked url")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("That URL is blocked.")
		h.UI.Shot("after-webfetch-deny")

		if !anyToolResultContains(h, "blocked by deny_list") {
			t.Fatal("web_fetch of a denied URL was not refused")
		}
	})

	t.Run("bash-curl", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category:    "sandbox",
			Subcategory: "sandbox.deny_urls",
			Description: "a bash command referencing a denied URL is refused",
			Wire:        harness.WireMessages,
			Variant:     "bash-curl",
		}, harness.WithSettings(settings))

		h.UI.WaitStable(400 * time.Millisecond)
		h.Mock.Enqueue(
			harness.ToolUse("bash", `{"command":"curl https://blocked.example.com/secret"}`),
			harness.Text("That URL is blocked."),
		)
		h.UI.Type("curl the blocked url")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("That URL is blocked.")
		h.UI.Shot("after-curl-deny")

		if !anyToolResultContains(h, "blocked by deny_list") {
			t.Fatal("bash referencing a denied URL was not refused")
		}
	})
}

// TestBashDenyTokenHeuristics proves the bash path-token heuristic: prose with
// no path separator is allowed to run, while a token containing a denied path is
// refused. T2.7.
func TestBashDenyTokenHeuristics(t *testing.T) {
	const sentinel = "FILECONTENT_T27_SENTINEL"

	h := harness.Start(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.bash_tokens",
		Description: "bare prose runs; a bash token containing a denied path is refused",
		Wire:        harness.WireMessages,
	}, harness.WithDenyPath("secret.txt"))

	if err := h.FS.Write("secret.txt", sentinel+"\n"); err != nil {
		t.Fatalf("seed secret.txt: %v", err)
	}
	h.UI.WaitStable(400 * time.Millisecond)

	// Turn 1 (positive): prose with no '/' is not a path → runs.
	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo no-secret-here-T27"}`),
		harness.Text("Echoed the line."),
	)
	h.UI.Type("echo a harmless line")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Echoed the line.")

	// Turn 2 (negative): a token containing the denied path → refused.
	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"cat ./secret.txt"}`),
		harness.Text("That path is blocked."),
	)
	h.UI.Type("cat the secret path")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("That path is blocked.")
	h.UI.Shot("after-bash-tokens")

	if !anyToolResultContains(h, "no-secret-here-T27") && !sandboxCannotExec(h) {
		t.Fatal("harmless prose bash command produced no output")
	}
	// The prose command (no path separator) must NOT be refused by the deny
	// heuristic. In this container bash may fail to exec (bwrap can't create a
	// namespace), so the echo surfaces its output OR a sandbox exec error —
	// either way it must not be a deny refusal.
	echoResult, ok := toolResultForUser(h, "echo a harmless line")
	if !ok {
		t.Fatal("echo command never reached execution")
	}
	if strings.Contains(echoResult, "blocked by deny_list") {
		t.Fatalf("harmless prose bash command was wrongly deny-blocked: %q", echoResult)
	}
	// The denied-path token IS refused.
	if !anyToolResultContains(h, "blocked by deny_list") {
		t.Fatal("bash token referencing the denied path was not refused")
	}
	if requestsLeak(h, sentinel) {
		t.Fatal("deny_list breach: secret file content leaked via bash")
	}
}

// TestDenyHomeSubpathOverridesAutoAllow proves a deny_list entry under $HOME
// (e.g. ~/.ssh) wins over HOME's auto-allow: the key can't be read and its
// contents never reach the model. The read_file is scripted live so the path can
// be the per-test absolute HOME path. T2.5.
func TestDenyHomeSubpathOverridesAutoAllow(t *testing.T) {
	const sentinel = "PRIVATE_KEY_T25_SENTINEL"

	h := harness.Start(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.home_subpath",
		Description: "a denied ~/.ssh path overrides HOME auto-allow; the key never reaches the model",
		Wire:        harness.WireMessages,
	},
		harness.WithSettings(`{"deny_list":{"paths":["{{HOME}}/.ssh"]}}`),
		harness.WithHomeFile(".ssh/id_rsa", sentinel+"\n"),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	keyPath := h.HomePath(".ssh", "id_rsa")
	h.UI.Type("read my ssh private key")
	h.UI.Enter()

	// Live-script the turns so the tool path is the absolute per-test HOME path.
	h.Mock.Next()
	h.Mock.Reply(harness.ToolUse("read_file", fmt.Sprintf(`{"path":%q}`, keyPath)))
	h.Mock.Next()
	h.Mock.Reply(harness.Text("Could not read the protected key."))

	h.UI.WaitFor("Could not read the protected key.")
	h.UI.Shot("after-home-deny")

	if requestsLeak(h, sentinel) {
		t.Fatal("deny_list breach: ~/.ssh key leaked to the model despite HOME auto-allow")
	}
	if !anyToolResultContains(h, "blocked by deny_list") {
		t.Fatal("reading the denied ~/.ssh path was not refused")
	}
}

// TestAllowedDirectoriesGrantsAccess is a staged acceptance spec for
// allowed_directories (T2.4). A path outside cwd/HOME but listed in
// allowed_directories should be readable without a prompt. Skipped: it needs the
// external dir to be granted through the kernel sandbox (Landlock) profile, which
// isn't validated here — enable and confirm after a gate run.
func TestAllowedDirectoriesGrantsAccess(t *testing.T) {
	harness.SkipScenario(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.allowed_dirs",
		Description: "allowed_directories grants read access to a path outside cwd/HOME",
		Wire:        harness.WireMessages,
	}, "staged: allowed_directories sandbox-profile grant is unvalidated here; enable after a gate run")

	dir := t.TempDir() // outside the per-test cwd/HOME
	if err := os.WriteFile(dir+"/note.txt", []byte("ALLOWED_DIR_CONTENT_T24"), 0o644); err != nil {
		t.Fatalf("seed external file: %v", err)
	}

	h := harness.Start(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.allowed_dirs",
		Description: "allowed_directories grants read access to a path outside cwd/HOME",
		Wire:        harness.WireMessages,
	}, harness.WithSettings(fmt.Sprintf(`{"allowed_directories":[%q]}`, dir)))

	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Type("read the external note")
	h.UI.Enter()
	h.Mock.Next()
	h.Mock.Reply(harness.ToolUse("read_file", fmt.Sprintf(`{"path":%q}`, dir+"/note.txt")))
	h.Mock.Next()
	h.Mock.Reply(harness.Text("Read the external note."))
	h.UI.WaitFor("Read the external note.")

	if !anyToolResultContains(h, "ALLOWED_DIR_CONTENT_T24") {
		t.Fatal("allowed_directories did not grant read access to the external file")
	}
}

func mustSeed(t *testing.T, h *harness.Harness, rel, content string) {
	t.Helper()
	if err := h.FS.Write(rel, content); err != nil {
		t.Fatalf("seed %s: %v", rel, err)
	}
}
