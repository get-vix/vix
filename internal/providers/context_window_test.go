package providers

import "testing"

// TestContextWindowLookup verifies the lookup mechanism, not specific token
// counts: the catalogue is regenerated from live provider APIs and its windows
// drift. Two invariants hold regardless of the data:
//   - every catalogued model resolves to exactly its own stored window, and
//   - anything not catalogued (or malformed/unprefixed) resolves to 0 (unknown).
func TestContextWindowLookup(t *testing.T) {
	r := Default()

	// Each catalogued spec resolves to its own recorded context_window.
	anyWindow := false
	for _, p := range r.All() {
		for _, m := range p.Models {
			if got := r.ContextWindow(m.Spec); got != m.ContextWindow {
				t.Errorf("ContextWindow(%q) = %d, want %d (catalogue value)",
					m.Spec, got, m.ContextWindow)
			}
			if m.ContextWindow > 0 {
				anyWindow = true
			}
		}
	}
	if !anyWindow {
		t.Fatal("no catalogued model carried a context_window; lookup can't be exercised")
	}

	// Unknown inputs all resolve to 0, exercising each miss path.
	for _, spec := range []string{
		"anthropic/this-model-does-not-exist", // valid prefix, absent model
		"claude-sonnet-4-6",                   // unprefixed legacy name
		"unknownprovider/whatever",            // unknown provider prefix
		"",
		"garbage",
	} {
		if got := r.ContextWindow(spec); got != 0 {
			t.Errorf("ContextWindow(%q) = %d, want 0", spec, got)
		}
	}
}
