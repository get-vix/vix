package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// activateOAuthCreateToken opens the F3 Models tab, focuses the credential
// column of the first (logged-in) provider, and walks its credential-method
// rows activating each row's primary button. An OAuth "Create token" row starts
// startProviderLogin — the code path that deadlocked in issue #53. API-key rows
// open a key-entry dialog we immediately cancel (esc) before moving on. Returns
// true once the login status surfaces (proving the event loop processed the
// result rather than freezing).
func activateOAuthCreateToken(h *harness.Harness) bool {
	h.UI.Key("f3")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Key("right") // providers -> auth (first credential-method row)
	h.UI.WaitStable(200 * time.Millisecond)

	for r := 0; r < 5; r++ {
		h.UI.Key("enter")
		h.UI.WaitStable(300 * time.Millisecond)
		s := h.UI.Snapshot()
		if strings.Contains(s, "keychain") ||
			strings.Contains(s, "Login failed") ||
			strings.Contains(s, "plaintext auth.json") ||
			strings.Contains(s, "Starting") {
			return true
		}
		// Not the OAuth row: dismiss any key-entry dialog and try the next row.
		h.UI.Key("esc")
		h.UI.WaitStable(150 * time.Millisecond)
		h.UI.Key("down")
		h.UI.WaitStable(150 * time.Millisecond)
	}
	return false
}

// TestOAuthCreateTokenWithoutKeychainDoesNotFreeze is the live regression for
// issue #53: on a machine with no usable OS keychain, activating an OAuth
// "Create token" button must NOT deadlock the TUI. Before the fix,
// startProviderLogin emitted its error synchronously via Program.Send from
// inside Model.Update, blocking the Bubble Tea event loop forever. The fix
// returns a tea.Cmd so the error is delivered through the loop.
//
// This scenario only runs where no keychain exists (the offline e2e container);
// on a developer machine with a keychain it skips, since activating the button
// there would start a real browser OAuth flow.
func TestOAuthCreateTokenWithoutKeychainDoesNotFreeze(t *testing.T) {
	meta := harness.Meta{
		Category:    "regressions",
		Subcategory: "models.oauth_no_keychain",
		Description: "activating OAuth Create token without a keychain surfaces an error instead of freezing the TUI (#53)",
		Wire:        harness.WireMessages,
	}
	h := harness.Start(t, meta)
	h.UI.WaitStable(400 * time.Millisecond)

	if !strings.Contains(h.Daemon.LogTail(400), "OS keyring unavailable") {
		t.Skip("requires an environment without an OS keychain (the e2e container); a keychain is present here")
	}

	if !activateOAuthCreateToken(h) {
		t.Fatalf("could not reach an OAuth Create token button; screen:\n%s", h.UI.Snapshot())
	}

	// The event loop must have processed the login result: with no keychain and
	// the fallback disabled (default), the status reports the keychain error.
	// If the loop were deadlocked this would never render and WaitFor times out.
	h.UI.WaitFor("keychain")
	h.UI.Shot("oauth-keychain-error")

	// Decisive liveness proof: the TUI still responds — switch back to chat and
	// run a full turn against the mock.
	h.UI.Key("f2")
	h.UI.WaitStable(300 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("Still responsive after the OAuth error."))
	h.UI.Type("are you still there?")
	h.UI.Enter()
	h.UI.WaitFor("Still responsive after the OAuth error.")

	if len(h.Mock.Requests()) == 0 {
		t.Fatalf("no request reached the mock — the TUI did not recover after the OAuth error")
	}
}
