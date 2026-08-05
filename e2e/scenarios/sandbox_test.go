package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestSandboxEnabled drives one real bash command (forcing vix's lazy sandbox
// detection), then inspects the daemon log for the selected backend. It works
// purely at the daemon level — no dependence on the TUI rendering. When the
// host kernel offers no real sandbox (e.g. Docker Desktop's LinuxKit lacks
// Landlock and bwrap can't unshare), it SKIPS rather than fails, since that's
// an environment capability, not a vix defect. On real Linux CI it asserts.
func TestSandboxEnabled(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.mode",
		Description: "vix selects a real sandbox backend (Landlock on Linux CI) for bash execution",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo sandbox-probe"}`),
		harness.Text("Ran the probe."),
	)
	h.UI.Type("run echo sandbox-probe")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // daemon ran bash (detecting the sandbox) + final turn
	h.UI.Shot("after-probe")

	mode := h.Daemon.SandboxMode()
	switch mode {
	case "landlock", "bubblewrap", "macOS sandbox-exec":
		// A real backend is enforcing — good.
	default:
		t.Skipf("no real sandbox available in this environment (mode=%q) — "+
			"meaningful only on a Landlock-capable Linux host", mode)
	}
}

// TestDenyListBlocksSecret proves the deny_list prevents the agent from reading
// a protected file: the secret's contents must never reach the model. It
// asserts on the wire (request bodies), so it's independent of TUI rendering.
func TestDenyListBlocksSecret(t *testing.T) {
	const secret = "TOPSECRET_E2E_VALUE_42"

	h := harness.Start(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.deny_list",
		Description: "a deny_list path can't be read via bash; its contents never reach the model",
		Wire:        harness.WireMessages,
		Variant:     "bash-cat",
	}, harness.WithDenyPath("secret.txt"))

	if err := h.FS.Write("secret.txt", secret+"\n"); err != nil {
		t.Fatalf("seed secret.txt: %v", err)
	}

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"cat secret.txt"}`),
		harness.Text("I could not read the protected file."),
	)
	h.UI.Type("show me the contents of secret.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // bash attempt + final turn
	h.UI.Shot("after-deny")

	for i, r := range h.Mock.Requests() {
		if strings.Contains(string(r.Body()), secret) {
			t.Fatalf("deny_list breach: secret leaked to the model in request %d", i)
		}
	}
}
