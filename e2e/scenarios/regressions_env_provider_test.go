package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// customEnvProvider is a ~/.vix/providers.json overlay defining a custom
// provider whose only credential method is an env_var (no keyring) — the exact
// shape from issue #57. Its base_url is never contacted; the scenario only
// exercises credential *availability*, not a request.
const customEnvProvider = `{
  "schema_version": 1,
  "providers": [
    { "id": "acmeenv", "display_name": "Acme Env", "model_prefix": "acmeenv",
      "wire_format": "chat_completions",
      "inference": { "base_url": "https://acme.invalid/v1", "auth_scheme": "bearer" },
      "credential_methods": [ { "kind": "api_key", "env_var": "ACME_ENV_API_KEY" } ],
      "models": [ { "spec": "acmeenv/fast", "display_name": "Acme Fast" } ] }
  ]
}`

// TestEnvOnlyCustomProviderIsConfigured is the live regression for issue #57: a
// custom provider defined in providers.json with an env_var and no keyring must
// be recognised as configured when the env var is set, even with no usable OS
// keychain (the offline e2e container has none). Before the fix, the Models tab
// gated a provider's "configured" state on a keychain entry, so a custom
// env_var-only provider was wrongly listed under "Available:" (no credential)
// instead of "Logged in:".
func TestEnvOnlyCustomProviderIsConfigured(t *testing.T) {
	meta := harness.Meta{
		Category:    "regressions",
		Subcategory: "models.env_provider",
		Description: "custom providers.json provider with env_var (no keyring) resolves without a keychain (#57)",
		Wire:        harness.WireMessages,
	}
	h := harness.Start(t, meta,
		harness.WithProviders(customEnvProvider),
		harness.WithEnv("ACME_ENV_API_KEY", "acme-env-secret"),
	)
	h.UI.WaitStable(400 * time.Millisecond)

	// Open the Models tab and let the credential status load.
	h.UI.Key("f3")
	h.UI.WaitFor("Acme Env")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("models-tab")

	s := h.UI.Snapshot()
	iLoggedIn := strings.Index(s, "Logged in:")
	iAvailable := strings.Index(s, "Available:")
	iAcme := strings.Index(s, "Acme Env")
	if iLoggedIn < 0 || iAvailable < 0 || iAcme < 0 {
		t.Fatalf("Models tab missing expected sections; screen:\n%s", s)
	}
	// The custom provider must sit in the "Logged in:" group (env var resolved a
	// credential), i.e. between the two group headers — not under "Available:".
	if !(iLoggedIn < iAcme && iAcme < iAvailable) {
		t.Fatalf("Acme Env not under \"Logged in:\" (loggedIn=%d acme=%d available=%d); screen:\n%s",
			iLoggedIn, iAcme, iAvailable, s)
	}
}
