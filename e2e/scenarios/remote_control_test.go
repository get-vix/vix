package scenarios

import (
	"testing"

	"github.com/get-vix/vix/e2e/harness"
)

// TestRemoteControlUnattendedPolicyAcceptance is a staged acceptance spec for
// remote-control daemon sessions. The current harness can drive TUI sessions and
// CLI-triggered jobs/hooks, but it has no provider ingress/reply primitive for
// Telegram or WhatsApp.
//
// When enabled it should inject a trusted remote message and assert that the
// resulting vix-initiated session denies tool confirmation requests, returns an
// error for user questions and plan proposals, and replies to the provider with
// the final text or remote-control error.
func TestRemoteControlUnattendedPolicyAcceptance(t *testing.T) {
	meta := harness.Meta{
		Category:    "remote_control",
		Subcategory: "remote_control.unattended_policy",
		Description: "remote-control runs deny confirmations and fail closed for interactive question/plan events",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "remote-control provider ingress/reply injection is not exposed by the current e2e harness; daemon policy is covered by internal/daemon unit tests")
}
