package harness

import "testing"

// AllWires is the set of provider wire dialects the mock can serve.
var AllWires = []Wire{WireMessages, WireResponses, WireChatCompletions}

// wireModel maps a wire to a model spec that routes a thread through it. The
// matching provider's base URL is pointed at the mock by daemonEnv:
//   - messages       → anthropic (daemon default; no spec needed)
//   - responses      → openai (OPENAI_BASE_URL → mock)
//   - chat_completions → minimax (MINIMAX_BASE_URL → mock)
func wireModel(w Wire) string {
	switch w {
	case WireResponses:
		return "openai/gpt-4o"
	default:
		// messages: anthropic default. chat_completions: see WireOptions/Start —
		// it currently skips (routing an http loopback through a cloud provider
		// trips the providers HTTPS validation).
		return ""
	}
}

// WireOptions returns the Start options that route a thread through wire w.
func WireOptions(w Wire) []Option {
	if spec := wireModel(w); spec != "" {
		return []Option{WithModel(spec)}
	}
	return nil
}

// EachWire runs fn as a subtest for every wire dialect, passing the per-wire
// routing options and a Meta with Wire/Variant set. Use it to assert one
// scenario behaves identically across providers:
//
//	harness.EachWire(t, baseMeta, func(t *testing.T, w harness.Wire, opts ...harness.Option) {
//	    h := harness.Start(t, baseMeta /* Wire filled in */, opts...)
//	    ...
//	})
func EachWire(t *testing.T, fn func(t *testing.T, w Wire, opts ...Option)) {
	t.Helper()
	for _, w := range AllWires {
		w := w
		t.Run(string(w), func(t *testing.T) {
			fn(t, w, WireOptions(w)...)
		})
	}
}
