package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestWriteFileAcrossWires runs the same write-a-file scenario across all three
// provider wire dialects (Anthropic Messages, OpenAI Responses, OpenAI-compatible
// Chat Completions), proving vix behaves identically regardless of provider.
// Each wire becomes its own subtest + report entry (distinguished by Variant).
func TestWriteFileAcrossWires(t *testing.T) {
	harness.EachWire(t, func(t *testing.T, w harness.Wire, opts ...harness.Option) {
		h := harness.Start(t, harness.Meta{
			Category:    "wires",
			Subcategory: "wires.write",
			Description: "model writes a file; identical behaviour across every provider wire",
			Wire:        w,
			Variant:     string(w),
		}, opts...)

		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("write_file", `{"path":"x.txt","content":"yo"}`),
			harness.Text("Wrote x.txt successfully."),
		)
		h.UI.Type("write x.txt containing yo")
		h.UI.Enter()

		h.UI.ResolveToolPrompts("Wrote x.txt")
		h.UI.WaitStable(300 * time.Millisecond)
		h.UI.Shot("after-write")

		if got := string(h.FS.Read("x.txt")); got != "yo" {
			t.Fatalf("[%s] x.txt = %q, want %q", w, got, "yo")
		}
	})
}
