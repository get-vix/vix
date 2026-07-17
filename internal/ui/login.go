package ui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	tea "charm.land/bubbletea/v2"

	"github.com/get-vix/vix/internal/auth"
	"github.com/get-vix/vix/internal/config"
)

// loginStatusMsg updates the Settings login status line with progress text.
type loginStatusMsg struct {
	provider string
	text     string
}

// loginDoneMsg signals an OAuth login attempt finished (err == nil on success).
type loginDoneMsg struct {
	provider string
	err      error
}

// oauthLoginID maps a UI provider column name to its auth-subsystem login id and
// reports whether that provider supports interactive OAuth login. The mapping
// lives in the config layer (derived from the provider auth methods) so the UI
// and credential-status helpers agree on which keychain entry holds the token.
func oauthLoginID(uiProvider string) (string, bool) {
	id := config.OAuthLoginID(uiProvider)
	return id, id != ""
}

// ProviderSupportsLogin reports whether a UI provider offers OAuth login.
func ProviderSupportsLogin(uiProvider string) bool {
	_, ok := oauthLoginID(uiProvider)
	return ok
}

// startProviderLogin runs the OAuth login flow for a UI provider in the
// background, pushing status updates to the program. On success the credential
// is stored by the auth subsystem (OS keychain, or the plaintext auth.json when
// the opt-in fallback is enabled). It returns a tea.Cmd so any immediate error
// is delivered through the Bubble Tea event loop; emitting it synchronously via
// sendToProgram from within Update would deadlock Program.Send (issue #53).
func startProviderLogin(uiProvider string) tea.Cmd {
	loginID, ok := oauthLoginID(uiProvider)
	if !ok {
		return nil
	}
	if !auth.CanPersistLogin() {
		return func() tea.Msg {
			return loginDoneMsg{provider: uiProvider, err: auth.ErrKeychainUnavailable}
		}
	}
	go func() {
		cb := auth.LoginCallbacks{
			OnAuth: func(info auth.AuthInfo) {
				openLoginBrowser(info.URL)
				sendToProgram(loginStatusMsg{uiProvider, "Opened your browser — complete login there…"})
			},
			OnDeviceCode: func(info auth.DeviceCodeInfo) {
				sendToProgram(loginStatusMsg{uiProvider, fmt.Sprintf("Visit %s and enter code %s", info.VerificationURI, info.UserCode)})
			},
			OnProgress: func(s string) { sendToProgram(loginStatusMsg{uiProvider, s}) },
			// The TUI cannot prompt mid-flow, so auto-select the default option
			// (browser login for Codex) and rely on the local callback server.
			OnSelect: func(p auth.SelectPrompt) (string, error) {
				if len(p.Options) > 0 {
					return p.Options[0].ID, nil
				}
				return "", nil
			},
		}
		err := auth.DefaultStorage().Login(context.Background(), loginID, cb)
		sendToProgram(loginDoneMsg{provider: uiProvider, err: err})
	}()
	return nil
}

// sendToProgram delivers a message to the running Bubble Tea program, if any.
func sendToProgram(msg tea.Msg) {
	if teaProgram != nil {
		teaProgram.Send(msg)
	}
}

// openLoginBrowser best-effort opens url in the user's default browser.
func openLoginBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
