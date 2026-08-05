package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Tier 3 — skills (progressive disclosure). A skill is seeded as a SKILL.md
// under .vix/skills/<name>/; the model invokes it via the `skill` tool (implicit)
// or the user via /<name> (explicit). Assertions are on the wire (the loaded
// body reaches the model), so they're deterministic.

func skillMD(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body + "\n"
}

func anyRequestBodyContains(h *harness.Harness, sub string) bool {
	for _, r := range h.Mock.Requests() {
		if strings.Contains(string(r.Body()), sub) {
			return true
		}
	}
	return false
}

// TestSkillImplicitInvocation proves the model can load a skill via the `skill`
// tool and receives its body. skills.implicit
func TestSkillImplicitInvocation(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "skills", Subcategory: "skills.implicit",
		Description: "the model calls the skill tool and gets the skill body back",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/skills/greet/SKILL.md",
		skillMD("greet", "A greeting skill for e2e.", "GREET_BODY_MARKER do the greeting steps")))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("skill", `{"name":"greet"}`),
		harness.Text("Loaded the greet skill."),
	)
	h.UI.Type("use the greet skill")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Loaded the greet skill.")

	if !anyToolResultContains(h, "GREET_BODY_MARKER") {
		t.Fatalf("skill body did not reach the model; requests=%d", len(h.Mock.Requests()))
	}
}

// TestSkillExplicitInvocation proves a user /<skill> renders the skill body into
// the turn. skills.explicit
func TestSkillExplicitInvocation(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "skills", Subcategory: "skills.explicit",
		Description: "typing /greet renders the skill body into the user turn",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/skills/greet/SKILL.md",
		skillMD("greet", "A greeting skill for e2e.", "GREET_EXPLICIT_MARKER greeting body")))

	h.UI.WaitStable(500 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("Acknowledged the greet skill."))
	// Typing "/greet" opens the slash menu, which would consume the next Enter
	// (it inserts the command rather than submitting). Press Esc to close the
	// menu, leaving "/greet" in the input, then Enter submits it — the daemon
	// expands a leading /<skill> into the skill body.
	h.UI.Type("/greet")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Key("esc")
	h.UI.WaitStable(200 * time.Millisecond)
	h.UI.Enter()
	h.UI.WaitFor("Acknowledged the greet skill.")

	if !anyRequestBodyContains(h, "GREET_EXPLICIT_MARKER") {
		t.Fatal("explicit /greet did not render the skill body into the request")
	}
}

// TestSkillProjectOverridesUser proves a project skill wins over a same-named
// user skill. skills.override
func TestSkillProjectOverridesUser(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "skills", Subcategory: "skills.override",
		Description: "a project skill overrides a same-named user skill",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/skills/greet/SKILL.md",
			skillMD("greet", "User greet skill.", "GREET_FROM_USER_HOME")),
		harness.WithWorkdirFile(".vix/skills/greet/SKILL.md",
			skillMD("greet", "Project greet skill.", "GREET_FROM_PROJECT")),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("skill", `{"name":"greet"}`),
		harness.Text("Loaded greet."),
	)
	h.UI.Type("use the greet skill")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Loaded greet.")

	if !anyToolResultContains(h, "GREET_FROM_PROJECT") {
		t.Fatal("project skill did not override the user skill")
	}
	if anyToolResultContains(h, "GREET_FROM_USER_HOME") {
		t.Fatal("user skill body leaked despite project override")
	}
}
