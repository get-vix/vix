package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// navigateModelsToText is a best-effort driver: open the F3 Models tab and press
// "down" until want appears on screen (bounded). The Models tab probes a local
// provider when the cursor lands on it, and renders the model grid for the
// focused provider — so this surfaces either a local model or a provider's
// models. Returns true if want appeared.
func navigateModelsToText(h *harness.Harness, want string, maxSteps int) bool {
	h.UI.Key("f3")
	h.UI.WaitStable(300 * time.Millisecond)
	if h.UI.Contains(want) {
		return true
	}
	for range maxSteps {
		h.UI.Key("down")
		h.UI.WaitStable(250 * time.Millisecond)
		if h.UI.Contains(want) {
			return true
		}
	}
	return false
}

// TestLocalProviderDetectedFromEnv is a staged acceptance spec for issue #35
// (LLAMACPP_BASE_URL ignored). The mock now serves the llama.cpp discovery
// endpoints, and WithEnv expands {{MOCK_URL}}, so the harness side is ready. It
// is skipped because (a) #35 is open and (b) the F3 keystroke navigation below
// is unvalidated — enable and refine the navigation after the first gate run.
//
// When enabled it proves: with LLAMACPP_BASE_URL pointing at a reachable server,
// the discovered local model appears in the F3 model picker. T1.5 · asserts
// screen (the local model id renders in the picker).
func TestLocalProviderDetectedFromEnv(t *testing.T) {
	meta := harness.Meta{
		Category:    "providers",
		Subcategory: "providers.local_detection",
		Description: "a llama.cpp model surfaces in the picker when LLAMACPP_BASE_URL is set (#35)",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "acceptance spec for #35 (open) + unvalidated F3 navigation; enable when fixed/validated")

	h := harness.Start(t, meta, harness.WithEnv("LLAMACPP_BASE_URL", "{{MOCK_URL}}/v1"))

	h.Mock.SetLocalModel("my-local-model", 4096)
	h.UI.WaitStable(400 * time.Millisecond)

	if !navigateModelsToText(h, "my-local-model", 15) {
		t.Fatalf("local model not shown in the F3 picker; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("local-model-listed")
}

// TestLocalLANHTTPBoots is the regression guard for the reported init-time
// panic: with LLAMACPP_BASE_URL pointing at a non-loopback host over plain HTTP
// (a self-hosted llama.cpp box on the LAN), vix used to panic at startup —
// `providers: embedded providers.json invalid: provider "llamacpp" base_url:
// non-HTTPS URL "http://freyr.local:8080"` — before main() could run. Local
// providers may now use plain HTTP on any host, and the embedded-defaults load
// no longer interpolates the environment, so the daemon boots and a normal turn
// runs on the default model. The LAN endpoint is unreachable under
// `--network none`; reachability is irrelevant here because the original crash
// was at config validation, not at probe time.
//
// T · asserts the daemon boots clean (no panic in the vixd log) and a turn
// completes over the wire.
func TestLocalLANHTTPBoots(t *testing.T) {
	meta := harness.Meta{
		Category:    "providers",
		Subcategory: "providers.local_lan_http",
		Description: "vixd boots and runs a turn with a non-loopback plain-HTTP LLAMACPP_BASE_URL (no startup panic)",
		Wire:        harness.WireMessages,
	}

	h := harness.Start(t, meta, harness.WithEnv("LLAMACPP_BASE_URL", "http://freyr.local:8080/v1"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("booted-with-lan-llamacpp")

	if log := h.Daemon.LogTail(200); strings.Contains(log, "panic:") {
		t.Fatalf("vixd panicked at startup with a LAN llama.cpp base URL; log:\n%s", log)
	}

	h.Mock.Enqueue(harness.Text("Booted fine with a LAN llama.cpp endpoint."))
	h.UI.Type("are you up?")
	h.UI.Enter()
	h.UI.WaitFor("Booted fine with a LAN llama.cpp endpoint.")
	h.UI.Shot("turn-completed")
}

// TestLemonadeLocalProviderBoots guards the Lemonade local provider added to the
// embedded providers.json. With LEMONADE_BASE_URL pointing at a reachable
// OpenAI-compatible server (here the mock, on loopback), vixd must boot without
// tripping the providers-config validation (the new provider defaults to the
// plain-HTTP loopback endpoint http://localhost:13305/v1) and run a normal turn.
// The mock serves the local-provider discovery shape, so the advertised model is
// discoverable, but this scenario asserts only the robust signals — clean boot
// and a completed turn on the default model — leaving F3 picker navigation to
// the (skipped) detection spec above.
//
// T · asserts the daemon boots clean (no panic in the vixd log) and a turn
// completes over the wire.
func TestLemonadeLocalProviderBoots(t *testing.T) {
	meta := harness.Meta{
		Category:    "providers",
		Subcategory: "providers.lemonade",
		Description: "vixd boots and runs a turn with LEMONADE_BASE_URL set (new local provider, no startup panic)",
		Wire:        harness.WireMessages,
	}

	h := harness.Start(t, meta, harness.WithEnv("LEMONADE_BASE_URL", "{{MOCK_URL}}/v1"))

	h.Mock.SetLocalModel("Qwen3-Coder-30B-A3B-Instruct-GGUF", 32768)
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("booted-with-lemonade")

	if log := h.Daemon.LogTail(200); strings.Contains(log, "panic:") {
		t.Fatalf("vixd panicked at startup with LEMONADE_BASE_URL set; log:\n%s", log)
	}

	h.Mock.Enqueue(harness.Text("Booted fine with a Lemonade endpoint."))
	h.UI.Type("are you up?")
	h.UI.Enter()
	h.UI.WaitFor("Booted fine with a Lemonade endpoint.")
	h.UI.Shot("turn-completed")
}

// (a valid credential is wrongly reported missing on switch). The harness
// injects OpenAI creds (key + base URL → mock) by default, so a switch to an
// OpenAI model should succeed. Skipped because #26 is open and the F3 picker
// keystroke choreography is stateful and unvalidated here.
//
// When enabled it proves: switching to a model whose credential is present does
// not raise "no credential", and a subsequent turn runs on the new model.
// T1.6 · asserts screen (no error) + wire (a turn reaches the mock after switch).
func TestModelSwitchWithValidCredential(t *testing.T) {
	meta := harness.Meta{
		Category:    "models",
		Subcategory: "models.switch_credential",
		Description: "switching to a model with a valid credential succeeds (#26)",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "acceptance spec for #26 (open) + unvalidated F3 picker navigation; enable when fixed/validated")

	h := harness.Start(t, meta)

	h.UI.WaitStable(400 * time.Millisecond)

	// Best-effort: open the picker, move into the model grid, filter for the
	// OpenAI model, and confirm. (The exact focus transitions are stateful; this
	// is the part to refine once runnable.)
	h.UI.Key("f3")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Key("tab") // providers -> auth
	h.UI.Key("tab") // auth -> model grid
	h.UI.Type("gpt-4o")
	h.UI.WaitStable(250 * time.Millisecond)
	h.UI.Key("enter")
	h.UI.WaitStable(400 * time.Millisecond)

	if h.UI.Contains("no credential") || h.UI.Contains("Cannot switch") {
		t.Fatalf("model switch wrongly reported a missing credential; screen:\n%s", h.UI.Snapshot())
	}

	// Back to chat; a turn should run on the newly selected model.
	h.UI.Key("f2")
	h.UI.WaitStable(300 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("Running on the switched model."))
	h.UI.Type("hello on the new model")
	h.UI.Enter()
	h.UI.WaitFor("Running on the switched model.")
}
