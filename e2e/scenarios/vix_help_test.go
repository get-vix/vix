package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// The bundled `vix-help` skill (shipped in internal/config/defaults/skills and
// seeded into ~/.vix by vixd's first-run bootstrap) answers questions about vix
// itself. This scenario exercises the offline path end-to-end: the model loads
// the skill, then reads its bundled manual snapshot with read_file — no network,
// so it works under the container's `--network none`. Assertions are on the wire
// (skill body + manual content reach the model), so they're deterministic.
// skills.vix_help
func TestVixHelpSkillOfflineFallback(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "skills", Subcategory: "skills.vix_help",
		Description: "the shipped vix-help skill loads and its bundled offline manual reaches the model",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// The bundled offline snapshot, seeded by the daemon's first-run bootstrap.
	manualPath := h.HomePath(".vix", "skills", "vix-help", "references", "vix-manual.md")

	h.Mock.Enqueue(
		harness.ToolUse("skill", `{"name":"vix-help"}`),
		harness.ToolUse("read_file", `{"path":"`+manualPath+`","reason":"answer a question about vix from the bundled manual"}`),
		harness.Text("F1 opens the Sessions tab."),
	)

	h.UI.Type("what does the F1 key do in vix?")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("F1 opens the Sessions tab.")

	// The skill body loaded and pointed at its bundled manual fallback.
	if !anyToolResultContains(h, "vix-manual.md") {
		t.Fatalf("vix-help skill body did not reach the model; requests=%d", len(h.Mock.Requests()))
	}
	// The bundled manual content itself reached the model.
	if !anyToolResultContains(h, "Vix Manual (offline snapshot)") {
		t.Fatal("bundled vix manual content did not reach the model")
	}
}
