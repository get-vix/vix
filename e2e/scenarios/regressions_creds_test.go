package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestCredentialEnvironmentAndDotenvAreIgnored proves credential-like process
// variables and dotenv files cannot shadow the daz-secrets provider. The
// harness provider holds the real test credential while both legacy channels
// carry deliberately wrong values.
func TestCredentialEnvironmentAndDotenvAreIgnored(t *testing.T) {
	meta := harness.Meta{
		Category:    "creds",
		Subcategory: "creds.provider_only",
		Description: "credential env and dotenv decoys are ignored while daz-secrets reaches the real LLM endpoint",
		Wire:        harness.WireMessages,
	}
	h := harness.Start(t, meta,
		harness.WithEnv("ANTHROPIC_API_KEY", "wrong-environment-key"),
		harness.WithWorkdirFile(".env", "ANTHROPIC_API_KEY=wrong-dotenv-key\nANTHROPIC_BASE_URL=https://wrong.invalid\n"),
		harness.WithHomeFile(".vix/.env", "ANTHROPIC_API_KEY=another-wrong-key\n"),
	)

	h.UI.WaitStable(400 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("Resolved credentials through daz-secrets."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("Resolved credentials through daz-secrets.")
	if len(h.Mock.Requests()) == 0 {
		t.Fatal("no request reached the real HTTP test endpoint through daz-secrets")
	}
}

// TestLegacyAPIKeyHelperCannotExecute proves a settings entry cannot reinstate
// command-based secret injection. Vix still completes through daz-secrets and
// the helper side effect never appears.
func TestLegacyAPIKeyHelperCannotExecute(t *testing.T) {
	meta := harness.Meta{
		Category:    "creds",
		Subcategory: "creds.no_helper",
		Description: "legacy apiKeyHelper cannot execute or inject credentials",
		Wire:        harness.WireMessages,
	}
	h := harness.Start(t, meta,
		harness.WithSettings(`{"apiKeyHelper":"touch {{WORKDIR}}/helper-ran"}`),
	)

	h.UI.WaitStable(400 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("Used only the configured secret provider."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("Used only the configured secret provider.")
	if h.FS.Exists("helper-ran") {
		t.Fatal("legacy apiKeyHelper executed")
	}
}
