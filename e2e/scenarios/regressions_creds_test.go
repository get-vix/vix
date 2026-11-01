package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestEnvFileCredentialResolution is the acceptance spec for issues #29 and
// #30: with no API-key/base-URL env vars, both the key and the base URL
// (ANTHROPIC_BASE_URL, declared as the method's base_url_env) resolve from a
// .env (CWD and ~/.vix variants), so a turn reaches the mock. T1.3 · asserts
// wire (request reached the mock via .env resolution).
func TestEnvFileCredentialResolution(t *testing.T) {
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

// TestProjectDotEnvCannotRedirectTrustedKey is the security counterpart of
// TestEnvFileCredentialResolution: the key and base URL come from ~/.vix/.env
// (pointing at the mock) while the project .env in the workdir tries to
// redirect ANTHROPIC_BASE_URL to an unreachable address. The project file must
// be ignored for a key it did not supply, so the turn still reaches the mock.
// Asserts wire (request reached the trusted endpoint).
func TestProjectDotEnvCannotRedirectTrustedKey(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "creds",
		Subcategory: "creds.env_files",
		Description: "a project .env cannot redirect ANTHROPIC_BASE_URL for a key from ~/.vix/.env",
		Wire:        harness.WireMessages,
	},
		harness.WithoutDefaultCreds(),
		harness.WithHomeFile(".vix/.env", "ANTHROPIC_API_KEY=test\nANTHROPIC_BASE_URL={{MOCK_URL}}\n"),
		harness.WithWorkdirFile(".env", "ANTHROPIC_BASE_URL=http://127.0.0.1:9/\n"),
	)

	h.UI.WaitStable(400 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("Trusted endpoint used."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("Trusted endpoint used.")

	if len(h.Mock.Requests()) == 0 {
		t.Fatalf("no request reached the mock — the project .env redirected the base URL")
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
