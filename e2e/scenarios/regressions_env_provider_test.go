package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// customEnvProvider is a ~/.vix/providers.json overlay defining a custom
// provider whose credential uses only the legacy env_var schema field. Vix
// maps that name to daz-secrets account acme-env-api-key without reading the
// process environment.
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
// custom provider using that legacy field must be recognised as configured
// from the provider-backed account pre-seeded by the e2e image.
func TestLegacyEnvFieldMapsToSecretProviderAccount(t *testing.T) {
	meta := harness.Meta{
		Category:    "regressions",
		Subcategory: "models.secret_provider_account",
		Description: "legacy env_var metadata maps to a daz-secrets account without reading env (#57)",
		Wire:        harness.WireMessages,
	}
	h := harness.Start(t, meta,
		harness.WithProviders(customEnvProvider),
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
	// The custom provider must sit in the "Logged in:" group (the mapped provider
	// account resolved), i.e. between the two group headers.
	if !(iLoggedIn < iAcme && iAcme < iAvailable) {
		t.Fatalf("Acme Env not under \"Logged in:\" (loggedIn=%d acme=%d available=%d); screen:\n%s",
			iLoggedIn, iAcme, iAvailable, s)
	}
}
