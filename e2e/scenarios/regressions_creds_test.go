package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestEnvFileCredentialResolution is a staged acceptance spec for issues #29 and
// #30. On current main, resolveKey searches the CWD .env for API keys but NOT
// ~/.vix/.env (#30), and resolveMethodBaseURL reads ANTHROPIC_BASE_URL via
// os.Getenv only — bypassing the .env search (#29). Until both land, this test
// can't route to the mock through a .env, so it is skipped.
//
// When enabled it proves: with no API-key/base-URL env vars, both the key and
// the base URL resolve from a .env (CWD and ~/.vix variants), so a turn reaches
// the mock. T1.3 · asserts wire (request reached the mock via .env resolution).
func TestEnvFileCredentialResolution(t *testing.T) {
	harness.SkipScenario(t, harness.Meta{
		Category:    "creds",
		Subcategory: "creds.env_files",
		Description: "API key + base URL resolve from a .env so the request reaches the mock (#29/#30)",
		Wire:        harness.WireMessages,
	}, "acceptance spec for #29/#30 (open): ANTHROPIC_BASE_URL and ~/.vix/.env are not yet resolved from .env files; enable when fixed")

	cases := []struct {
		name string
		opts []harness.Option
	}{
		{
			name: "cwd-dotenv",
			opts: []harness.Option{
				harness.WithoutDefaultCreds(),
				harness.WithWorkdirFile(".env", "ANTHROPIC_API_KEY=test\nANTHROPIC_BASE_URL={{MOCK_URL}}\n"),
			},
		},
		{
			name: "home-vix-dotenv",
			opts: []harness.Option{
				harness.WithoutDefaultCreds(),
				harness.WithHomeFile(".vix/.env", "ANTHROPIC_API_KEY=test\nANTHROPIC_BASE_URL={{MOCK_URL}}\n"),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := harness.Start(t, harness.Meta{
				Category:    "creds",
				Subcategory: "creds.env_files",
				Description: "API key + base URL resolve from a .env so the request reaches the mock (#29/#30)",
				Wire:        harness.WireMessages,
				Variant:     c.name,
			}, c.opts...)

			h.UI.WaitStable(400 * time.Millisecond)
			h.Mock.Enqueue(harness.Text("Resolved credentials from .env."))
			h.UI.Type("say hello")
			h.UI.Enter()
			h.UI.WaitFor("Resolved credentials from .env.")

			if len(h.Mock.Requests()) == 0 {
				t.Fatalf("no request reached the mock — .env credentials were not resolved")
			}
		})
	}
}

// TestApiKeyHelperResolvesKey is a staged acceptance spec for issue #31
// (apiKeyHelper). There is currently no apiKeyHelper support in the codebase, so
// this is skipped until the feature lands.
//
// When enabled it proves: with no API-key env var, settings.json's apiKeyHelper
// command supplies the key (via `sh -c`), and the base URL comes from a .env, so
// a turn reaches the mock. T1.4 · asserts wire (request reached the mock using
// the helper-provided key).
func TestApiKeyHelperResolvesKey(t *testing.T) {
	meta := harness.Meta{
		Category:    "creds",
		Subcategory: "creds.api_key_helper",
		Description: "apiKeyHelper command supplies the API key (#31)",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "acceptance spec for #31 (open): settings.json apiKeyHelper is not implemented; enable when the feature lands")

	h := harness.Start(t, meta,
		harness.WithoutDefaultCreds(),
		// Base URL from .env (so the request is offline); key from the helper.
		harness.WithHomeFile(".vix/.env", "ANTHROPIC_BASE_URL={{MOCK_URL}}\n"),
		harness.WithSettings(`{"apiKeyHelper":"echo test"}`),
	)

	h.UI.WaitStable(400 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("Used the apiKeyHelper key."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("Used the apiKeyHelper key.")

	if len(h.Mock.Requests()) == 0 {
		t.Fatalf("no request reached the mock — apiKeyHelper key was not used")
	}
}
