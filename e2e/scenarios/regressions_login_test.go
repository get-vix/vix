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
// activation rather than freezing).
func activateOAuthCreateToken(h *harness.Harness) bool {
	h.UI.Key("f3")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Key("right") // providers -> auth (first credential-method row)
	h.UI.WaitStable(200 * time.Millisecond)

	for r := 0; r < 5; r++ {
		h.UI.Key("enter")
		h.UI.WaitStable(300 * time.Millisecond)
		s := h.UI.Snapshot()
		if strings.Contains(s, "plaintext auth.json") ||
			strings.Contains(s, "Opened your browser") ||
			strings.Contains(s, "keychain") ||
			strings.Contains(s, "Login failed") ||
			strings.Contains(s, "login…") {
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

// TestOAuthLoginWithoutKeychainFallsBackToPlaintext is the live regression for
// issues #53 and #56. On a machine with no usable OS keychain:
//
//   - #53: activating an OAuth "Create token" must NOT deadlock the TUI. The
//     login result travels through the Bubble Tea event loop (a tea.Cmd), never
//     emitted synchronously via Program.Send from inside Model.Update.
//   - #56: the login must NOT be refused with "OS keychain unavailable". OAuth
//     tokens now fall back to the plaintext auth.json automatically — no opt-in
//     flag — so the flow proceeds past the storage gate to the browser step,
//     surfacing the "token will be stored in plaintext auth.json" disclosure.
//
// This scenario only runs where no keychain exists (the offline e2e container);
// on a developer machine with a keychain it skips, since activating the button
// there would start a real browser OAuth flow.
func TestOAuthLoginWithoutKeychainFallsBackToPlaintext(t *testing.T) {
	meta := harness.Meta{
		Category:    "regressions",
		Subcategory: "models.oauth_plaintext_fallback",
		Description: "without a keychain, OAuth login falls back to plaintext auth.json instead of being refused, and the TUI stays live (#53, #56)",
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

	// #56: the login proceeds past the storage gate to the browser step instead
	// of being refused. Before the fix (opt-in fallback off by default), keyless
	// activation immediately reported the "OS keychain unavailable" error.
	h.UI.WaitFor("Opened your browser")
	if s := h.UI.Snapshot(); strings.Contains(s, "keychain unavailable") || strings.Contains(s, "OS keychain") {
		t.Fatalf("login was refused for lack of a keychain; automatic fallback did not engage:\n%s", s)
	}
	h.UI.Shot("oauth-plaintext-fallback")

	// #53: decisive liveness proof — the TUI still responds. Switch back to chat
	// and run a full turn against the mock. If the loop were deadlocked this
	// would never complete and WaitFor would time out.
	h.UI.Key("f2")
	h.UI.WaitStable(300 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("Still responsive after starting the OAuth login."))
	h.UI.Type("are you still there?")
	h.UI.Enter()
	h.UI.WaitFor("Still responsive after starting the OAuth login.")

	if len(h.Mock.Requests()) == 0 {
		t.Fatalf("no request reached the mock — the TUI did not recover after starting the OAuth login")
	}
}
