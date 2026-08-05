package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestMermaidRendersAsDiagramWithLink exercises the whiteboard feature: when the
// model emits a ```mermaid fenced block, the terminal renders it as an
// ASCII/Unicode diagram (not raw source) and appends a "See it on the
// whiteboard" link. Flowcharts route to a positioned ?scenes_z= URL.
//
// asserts screen (diagram + link rendered from a model mermaid block).
func TestMermaidRendersAsDiagramWithLink(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "whiteboard",
		Subcategory: "whiteboard.mermaid",
		Description: "a model ```mermaid block renders as an ASCII diagram with a whiteboard link",
		Wire:        harness.WireMessages,
	}, harness.WithWebUI())

	h.UI.WaitStable(400 * time.Millisecond)

	// The model answers with a short flowchart plus a trailing sentence.
	h.Mock.Enqueue(
		harness.Text("Here is the architecture:\n\n```mermaid\ngraph LR\nA[Web] --> B[API]\nB --> C[(DB)]\n```\n\nThat is the flow."),
	)
	h.UI.Type("draw the architecture")
	h.UI.Enter()

	h.UI.WaitFor("See it on the whiteboard")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("mermaid-rendered")

	// The raw mermaid header must be replaced by the rendered diagram.
	if h.UI.Contains("graph LR") {
		t.Errorf("raw mermaid source still on screen; expected an ASCII diagram")
	}
	// A node label from the flowchart appears in the rendered diagram.
	if !h.UI.Contains("Web") || !h.UI.Contains("API") {
		t.Errorf("expected flowchart node labels in the rendered diagram; screen:\n%s", h.UI.Snapshot())
	}
	// Unicode box-drawing confirms the diagram (not a plain code box) rendered.
	if !strings.ContainsAny(h.UI.Snapshot(), "┌└│─►") {
		t.Errorf("expected box-drawing characters from the ASCII diagram; screen:\n%s", h.UI.Snapshot())
	}
}

// TestMermaidWithStyleRenders covers a styled flowchart: a ```mermaid block that
// carries classDef/class/style directives (used to color fan_out/fan_in/if
// nodes) must still render as an ASCII diagram with a "See it on the whiteboard"
// link — i.e. the parser tolerates styling end to end and never leaks the raw
// classDef/style source onto the screen. Per-node color round-trips are asserted
// in the whiteboard package unit tests (the TUI can't inspect the compressed
// ?scenes_z= payload).
func TestMermaidWithStyleRenders(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "whiteboard",
		Subcategory: "whiteboard.mermaid_style",
		Description: "a styled ```mermaid block (classDef/class/style) renders as an ASCII diagram with a whiteboard link",
		Wire:        harness.WireMessages,
	}, harness.WithWebUI())

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.Text("Pipeline:\n\n```mermaid\ngraph LR\nA[Fetch] --> B[Gate]\nB --> C[Group]\n" +
			"classDef fan fill:#8b5cf6,stroke:#6d28d9\nclass A,C fan\nstyle B fill:#f59e0b\n```\n\nThat is the flow."),
	)
	h.UI.Type("draw the styled pipeline")
	h.UI.Enter()

	h.UI.WaitFor("See it on the whiteboard")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("mermaid-styled")

	// Node labels appear in the rendered diagram.
	if !h.UI.Contains("Fetch") || !h.UI.Contains("Group") {
		t.Errorf("expected flowchart node labels in the rendered diagram; screen:\n%s", h.UI.Snapshot())
	}
	// Neither the raw graph header nor the styling directives leak to the screen.
	for _, leak := range []string{"graph LR", "classDef", "style B"} {
		if h.UI.Contains(leak) {
			t.Errorf("raw mermaid %q still on screen; expected a rendered diagram", leak)
		}
	}
	// Box-drawing confirms the diagram (not a plain code box) rendered.
	if !strings.ContainsAny(h.UI.Snapshot(), "┌└│─►") {
		t.Errorf("expected box-drawing characters from the ASCII diagram; screen:\n%s", h.UI.Snapshot())
	}
}

// TestMermaidSequenceRendersWithLink covers a non-flowchart diagram: a// sequenceDiagram renders as an ASCII lifeline diagram in the terminal and still
// gets a "See it on the whiteboard" link (a ?mermaid_z= fallback URL that
// mermaid.js renders in the browser).
func TestMermaidBrLabelRendersWithoutLiteralBreak(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "whiteboard",
		Subcategory: "whiteboard.mermaid_br",
		Description: "a flowchart node with a <br/> label renders multi-line (no literal <br/>) with a whiteboard link",
		Wire:        harness.WireMessages,
	}, harness.WithWebUI())

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.Text("Here is the pipeline:\n\n```mermaid\ngraph LR\nA[\"inspect codebase once<br/>metrics: churn, coverage\"] --> B[fetch history]\n```\n\nThat is the flow."),
	)
	h.UI.Type("draw the pipeline")
	h.UI.Enter()

	h.UI.WaitFor("See it on the whiteboard")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("mermaid-br-label")

	snap := h.UI.Snapshot()
	// The literal HTML break must never appear on screen.
	if strings.Contains(snap, "<br") {
		t.Errorf("literal <br/> leaked into the rendered diagram; screen:\n%s", snap)
	}
	// Both halves of the split label render (on separate lines in the ASCII box).
	if !h.UI.Contains("inspect codebase once") || !h.UI.Contains("metrics: churn") {
		t.Errorf("expected the multi-line label text in the diagram; screen:\n%s", snap)
	}
	// Raw mermaid header must be replaced by the rendered diagram.
	if h.UI.Contains("graph LR") {
		t.Errorf("raw mermaid source still on screen; expected an ASCII diagram")
	}
}

// TestMermaidSubgraphRenders covers subgraph clustering: a flowchart with
// `subgraph … end` containers (including an edge that targets a subgraph id)
// must still render as an ASCII diagram in the terminal with a "See it on the
// whiteboard" link, exposing member labels and never leaking the raw
// subgraph/graph/end source. The container layout (group nodes + parent_id) is
// asserted in the whiteboard package unit tests — the TUI can't inspect the
// compressed ?scenes_z= payload.
func TestMermaidSubgraphRenders(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "whiteboard",
		Subcategory: "whiteboard.mermaid_subgraph",
		Description: "a flowchart with subgraph containers renders as an ASCII diagram with a whiteboard link",
		Wire:        harness.WireMessages,
	}, harness.WithWebUI())

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.Text("Pipeline:\n\n```mermaid\nflowchart LR\nFetch[Fetch] --> CI\n" +
			"subgraph CI[\"Pipeline\"]\n  lint --> build\nend\n```\n\nThat is the flow."),
	)
	h.UI.Type("draw the pipeline with a container")
	h.UI.Enter()

	h.UI.WaitFor("See it on the whiteboard")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("mermaid-subgraph")

	// Member labels from inside the subgraph (and the node feeding into it) appear
	// in the rendered diagram.
	if !h.UI.Contains("Fetch") || !h.UI.Contains("lint") || !h.UI.Contains("build") {
		t.Errorf("expected subgraph member labels in the rendered diagram; screen:\n%s", h.UI.Snapshot())
	}
	// Neither the raw graph header nor the subgraph keyword leak to the screen.
	for _, leak := range []string{"flowchart LR", "subgraph"} {
		if h.UI.Contains(leak) {
			t.Errorf("raw mermaid %q still on screen; expected a rendered diagram", leak)
		}
	}
	// Box-drawing confirms the diagram (not a plain code box) rendered.
	if !strings.ContainsAny(h.UI.Snapshot(), "┌└│─►") {
		t.Errorf("expected box-drawing characters from the ASCII diagram; screen:\n%s", h.UI.Snapshot())
	}
}

// TestMermaidDecisionNodeRendersAsDoubleBox covers the decision node ({...}).
// Terminal box-drawing cannot form a clean rhombus for realistic labels (the
// single-glyph-per-row diagonals scatter), so a decision node renders as a
// connected double-line box. This asserts the box actually renders (double-line
// box-drawing on screen), the label survives, the scattered-rhombus diagonals
// are gone, and no raw mermaid source leaks.
func TestMermaidDecisionNodeRendersAsDoubleBox(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "whiteboard",
		Subcategory: "whiteboard.mermaid_decision",
		Description: "a flowchart decision node ({...}) renders as a connected double-line box with a whiteboard link",
		Wire:        harness.WireMessages,
	}, harness.WithWebUI())

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.Text("Deploy flow:\n\n```mermaid\ngraph TD\nA[Start] --> B{Deploy ready}\nB --> C[Ship]\nB --> D[Halt]\n```\n\nThat is the flow."),
	)
	h.UI.Type("draw the deploy decision")
	h.UI.Enter()

	h.UI.WaitFor("See it on the whiteboard")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("mermaid-decision")

	snap := h.UI.Snapshot()
	// The decision node renders as a double-line box (its distinctive glyphs).
	if !strings.ContainsAny(snap, "╔╗╚╝║═") {
		t.Errorf("expected double-line box-drawing for the decision node; screen:\n%s", snap)
	}
	// The old scattered-rhombus diagonals must never appear.
	if strings.ContainsAny(snap, "╱╲") {
		t.Errorf("decision node drawn with scattered diagonals; screen:\n%s", snap)
	}
	// The decision label and the branch targets survive into the diagram.
	if !h.UI.Contains("Deploy ready") || !h.UI.Contains("Ship") || !h.UI.Contains("Halt") {
		t.Errorf("expected decision + branch labels in the rendered diagram; screen:\n%s", snap)
	}
	// Raw mermaid source must be replaced by the rendered diagram.
	if h.UI.Contains("graph TD") {
		t.Errorf("raw mermaid source still on screen; expected a rendered diagram")
	}
}

func TestMermaidSequenceRendersWithLink(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "whiteboard",
		Subcategory: "whiteboard.mermaid_sequence",
		Description: "a sequenceDiagram renders as ASCII with a whiteboard (mermaid.js fallback) link",
		Wire:        harness.WireMessages,
	}, harness.WithWebUI())

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.Text("Sequence:\n\n```mermaid\nsequenceDiagram\nAlice->>Bob: Hello\nBob-->>Alice: Hi\n```\n\nThat is the exchange."),
	)
	h.UI.Type("show the handshake")
	h.UI.Enter()

	h.UI.WaitFor("See it on the whiteboard")
	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("mermaid-sequence")

	// Participants appear in the rendered lifeline diagram.
	if !h.UI.Contains("Alice") || !h.UI.Contains("Bob") {
		t.Errorf("expected sequence participants in the rendered diagram; screen:\n%s", h.UI.Snapshot())
	}
	// Raw mermaid header must be replaced by the rendered diagram.
	if h.UI.Contains("sequenceDiagram") {
		t.Errorf("raw mermaid source still on screen; expected an ASCII diagram")
	}
}
