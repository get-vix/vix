package whiteboard

import (
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

// decompressParam inverts compress(): base64url-decode then inflate.
func decompressParam(t *testing.T, v string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		t.Fatalf("base64url decode: %v", err)
	}
	out, err := io.ReadAll(flate.NewReader(strings.NewReader(string(raw))))
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	return string(out)
}

func paramValue(url, key string) string {
	i := strings.Index(url, key+"=")
	if i < 0 {
		return ""
	}
	rest := url[i+len(key)+1:]
	if amp := strings.IndexByte(rest, '&'); amp >= 0 {
		rest = rest[:amp]
	}
	return rest
}

func TestParseFlowchartBasic(t *testing.T) {
	g, ok := parseFlowchart("graph TD\n  A[Start] --> B{OK?}\n  B -->|yes| C[(Store)]\n  B -->|no| A")
	if !ok {
		t.Fatal("expected flowchart to parse")
	}
	if g.direction != "TD" {
		t.Errorf("direction = %q, want TD", g.direction)
	}
	if len(g.order) != 3 {
		t.Fatalf("nodes = %d, want 3 (%v)", len(g.order), g.order)
	}
	if g.nodes["A"].label != "Start" || g.nodes["A"].shape != shapeRectangle {
		t.Errorf("A = %+v", g.nodes["A"])
	}
	if g.nodes["B"].shape != shapeDiamond {
		t.Errorf("B shape = %q, want diamond", g.nodes["B"].shape)
	}
	if g.nodes["C"].shape != shapeDatabase {
		t.Errorf("C shape = %q, want database", g.nodes["C"].shape)
	}
	// Edge labels.
	var labels []string
	for _, e := range g.edges {
		labels = append(labels, e.label)
	}
	if len(g.edges) != 3 {
		t.Fatalf("edges = %d, want 3 (%v)", len(g.edges), g.edges)
	}
	if !contains(labels, "yes") || !contains(labels, "no") {
		t.Errorf("edge labels = %v, want yes/no present", labels)
	}
}

func TestParseFlowchartChainAndAmp(t *testing.T) {
	g, ok := parseFlowchart("flowchart LR\nA --> B & C\nB --> D")
	if !ok {
		t.Fatal("expected parse")
	}
	if g.direction != "LR" {
		t.Errorf("direction = %q", g.direction)
	}
	// A->B, A->C, B->D
	if len(g.edges) != 3 {
		t.Fatalf("edges = %d want 3: %v", len(g.edges), g.edges)
	}
}

func TestParseInlineLabelForm(t *testing.T) {
	g, ok := parseFlowchart("graph LR\nA -- request --> B")
	if !ok {
		t.Fatal("expected parse")
	}
	if len(g.edges) != 1 || g.edges[0].label != "request" {
		t.Fatalf("edges = %v, want single labelled 'request'", g.edges)
	}
}

func TestParseNonFlowchartRejected(t *testing.T) {
	for _, src := range []string{
		"sequenceDiagram\nAlice->>Bob: hi",
		"classDiagram\nAnimal <|-- Dog",
		"not a diagram at all",
	} {
		if _, ok := parseFlowchart(src); ok {
			t.Errorf("expected %q to be rejected as non-flowchart", strings.SplitN(src, "\n", 2)[0])
		}
	}
}

func TestToSceneLayoutNoOverlap(t *testing.T) {
	scene, ok := ToScene("graph TD\nA-->B\nA-->C\nB-->D\nC-->D", "Flow")
	if !ok {
		t.Fatal("expected scene")
	}
	if scene.Name != "Flow" {
		t.Errorf("name = %q", scene.Name)
	}
	if len(scene.Nodes) != 4 {
		t.Fatalf("nodes = %d want 4", len(scene.Nodes))
	}
	// No two nodes share the same coordinate.
	seen := map[[2]int]string{}
	for _, n := range scene.Nodes {
		key := [2]int{n.X, n.Y}
		if other, dup := seen[key]; dup {
			t.Errorf("nodes %s and %s overlap at %v", other, n.ID, key)
		}
		seen[key] = n.ID
	}
	// TD layout: edges exit bottom, enter top.
	for _, e := range scene.Edges {
		if e.FromHandle != "bottom" || e.ToHandle != "top" {
			t.Errorf("edge %s handles = %s/%s, want bottom/top", e.ID, e.FromHandle, e.ToHandle)
		}
	}
	// D should rank below B and C (longest path = 2).
	y := map[string]int{}
	for _, n := range scene.Nodes {
		y[n.ID] = n.Y
	}
	if !(y["D"] > y["B"] && y["D"] > y["A"]) {
		t.Errorf("expected D below B and A, got y=%v", y)
	}
}

func TestLayoutCycleTerminates(t *testing.T) {
	scene, ok := ToScene("graph LR\nA-->B\nB-->C\nC-->A", "cycle")
	if !ok {
		t.Fatal("expected scene")
	}
	if len(scene.Nodes) != 3 || len(scene.Edges) != 3 {
		t.Fatalf("nodes=%d edges=%d", len(scene.Nodes), len(scene.Edges))
	}
}

// TestLayoutBackEdgeDoesNotStrandNode covers the loop-back regression: a
// flowchart with a back-edge (C -->|No| B forms the cycle B->C->B) must not
// inflate the longest-path ranks. Before the cycle-breaking fix, every node
// downstream of the cycle was shoved ~20 ranks down while the source node A
// stayed pinned at rank 0, stranding it far above the rest.
func TestLayoutBackEdgeDoesNotStrandNode(t *testing.T) {
	src := "graph TD\n" +
		"A[Customer places order] --> B{Payment OK?}\n" +
		"B -->|No| C[Ask for another card]\n" +
		"C --> B\n" +
		"B -->|Yes| D[Barista queue]\n" +
		"D --> E{Drink type?}\n" +
		"E --> F\nE --> G\nE --> H\n" +
		"F --> I\nG --> I\nH --> I\n" +
		"I --> J\nJ --> K"
	scene, ok := ToScene(src, "coffee")
	if !ok {
		t.Fatal("expected scene")
	}
	// The back-edge must be preserved as an edge (we break it for ranking only).
	if len(scene.Edges) != 13 {
		t.Fatalf("edges = %d, want 13 (back-edge kept)", len(scene.Edges))
	}
	y := map[string]int{}
	for _, n := range scene.Nodes {
		y[n.ID] = n.Y
	}
	// A sits above B — not stranded 20+ ranks up.
	if y["A"] >= y["B"] {
		t.Errorf("A (%d) should be above B (%d)", y["A"], y["B"])
	}
	// Ranks stay compact: the graph has 8 ranks (0..7), so nodes occupy exactly 8
	// distinct Y rows (a blown-up cycle would create far more). Rank strides are
	// size-aware, so assert positions relative to the sorted rows rather than a
	// fixed spacing.
	rowSet := map[int]bool{}
	for _, yv := range y {
		rowSet[yv] = true
	}
	rows := make([]int, 0, len(rowSet))
	for yv := range rowSet {
		rows = append(rows, yv)
	}
	sort.Ints(rows)
	if len(rows) != 8 {
		t.Errorf("distinct rank rows = %d, want 8 (compact ranks)", len(rows))
	}
	// A on the top row, B exactly one rank below it, K on the bottom row.
	if y["A"] != rows[0] {
		t.Errorf("A.Y = %d, want top row %d", y["A"], rows[0])
	}
	if len(rows) > 1 && y["B"] != rows[1] {
		t.Errorf("B.Y = %d, want one rank below A (%d)", y["B"], rows[1])
	}
	if y["K"] != rows[len(rows)-1] {
		t.Errorf("K.Y = %d, want bottom row %d (compact ranks)", y["K"], rows[len(rows)-1])
	}
}

// TestLayoutCentersBranches checks the horizontal-centering fix: a diamond that
// fans out to three siblings must sit centered over them, and the siblings must
// spread symmetrically (left / center / right) rather than all to one side.
func TestLayoutCentersBranches(t *testing.T) {
	scene, ok := ToScene("graph TD\nE{pick} --> F\nE --> G\nE --> H", "fan")
	if !ok {
		t.Fatal("expected scene")
	}
	x := map[string]int{}
	for _, n := range scene.Nodes {
		x[n.ID] = n.X
	}
	// Siblings ordered left to right and distinct.
	if !(x["F"] < x["G"] && x["G"] < x["H"]) {
		t.Fatalf("expected F<G<H, got x=%v", x)
	}
	// Parent centered over the fan: aligned with the middle child and equal to
	// the mean of the three, and the fan is symmetric about the parent.
	if x["E"] != x["G"] {
		t.Errorf("parent E (%d) not aligned with middle child G (%d)", x["E"], x["G"])
	}
	if mean := (x["F"] + x["G"] + x["H"]) / 3; x["E"] != mean {
		t.Errorf("parent E (%d) not centered over children (mean %d)", x["E"], mean)
	}
	if x["F"]+x["H"] != 2*x["E"] {
		t.Errorf("fan not symmetric about E: F=%d H=%d E=%d", x["F"], x["H"], x["E"])
	}
}

func TestLinkForFlowchartUsesScenes(t *testing.T) {
	link, err := LinkFor("http://localhost:1337", "abc-123", "graph TD\nA-->B")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "http://localhost:1337/thread/abc-123/whiteboard?scenes_z=") {
		t.Fatalf("link = %q", link)
	}
	// base64url payload must contain no '%' (so terminals don't re-encode it).
	if strings.Contains(link, "%") {
		t.Errorf("compressed link should have no percent-encoding: %q", link)
	}
	// The scenes payload decompresses to valid scene JSON with our nodes.
	dec := decompressParam(t, paramValue(link, "scenes_z"))
	var scenes []Scene
	if err := json.Unmarshal([]byte(dec), &scenes); err != nil {
		t.Fatalf("scenes JSON: %v (%s)", err, dec)
	}
	if len(scenes) != 1 || len(scenes[0].Nodes) != 2 {
		t.Fatalf("decoded scenes = %+v", scenes)
	}
}

func TestLinkForNonFlowchartUsesMermaid(t *testing.T) {
	link, err := LinkFor("http://localhost:1337", "t1", "sequenceDiagram\nAlice->>Bob: hi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "/thread/t1/whiteboard?mermaid_z=") {
		t.Fatalf("link = %q", link)
	}
	if got := decompressParam(t, paramValue(link, "mermaid_z")); !strings.Contains(got, "sequenceDiagram") {
		t.Errorf("mermaid payload = %q", got)
	}
}

func TestEdgeLabelSurvivesRoundTrip(t *testing.T) {
	scene, ok := ToScene("graph LR\nA -->|payload| B", "")
	if !ok {
		t.Fatal("expected scene")
	}
	raw, _ := json.Marshal(scene)
	var back Scene
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Edges) != 1 || back.Edges[0].Label != "payload" {
		t.Fatalf("edge label lost: %+v", back.Edges)
	}
}

func TestAppendWhiteboardLinks(t *testing.T) {
	text := "Here is a diagram:\n\n```mermaid\ngraph TD\nA-->B\n```\n\nDone."
	out := AppendWhiteboardLinks(text, "http://localhost:1337", "th1")
	if strings.Count(out, "See it on the whiteboard") != 1 {
		t.Fatalf("expected exactly one link, got:\n%s", out)
	}
	// Idempotent: running again must not add a second link.
	again := AppendWhiteboardLinks(out, "http://localhost:1337", "th1")
	if strings.Count(again, "See it on the whiteboard") != 1 {
		t.Fatalf("link duplicated on re-run:\n%s", again)
	}
	// The link comes right after the closing fence, before "Done."
	if !strings.Contains(out, "```\n\n[See it on the whiteboard]") {
		t.Errorf("link not placed after fence:\n%s", out)
	}
}

func TestAppendWhiteboardLinksDisabled(t *testing.T) {
	text := "```mermaid\ngraph TD\nA-->B\n```"
	if got := AppendWhiteboardLinks(text, "", "th1"); got != text {
		t.Errorf("expected unchanged when base empty, got %q", got)
	}
}

func TestWhiteboardBase(t *testing.T) {
	if WhiteboardBase(0) != "" {
		t.Error("port 0 should disable the whiteboard")
	}
	if WhiteboardBase(1337) != "http://localhost:1337" {
		t.Errorf("base = %q", WhiteboardBase(1337))
	}
}

func TestRenderASCIIFlowchart(t *testing.T) {
	out, err := RenderASCII("graph LR\nA-->B-->C", 0)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "A") || !strings.Contains(out, "C") {
		t.Errorf("ascii missing node labels:\n%s", out)
	}
}

// TestRenderASCIIDecisionDoubleBox guards decision-node ({...}) rendering.
// Terminal box-drawing cannot form a clean rhombus for realistic (wide, short)
// labels — a single glyph per row leaves the diagonal edges several columns
// apart, so they scatter instead of connecting. A decision node is therefore
// drawn as a connected double-line box, visually distinct from plain single-line
// rectangles. This is the regression guard for the scattered-diamond bug.
func TestRenderASCIIDecisionDoubleBox(t *testing.T) {
	out, err := RenderASCII("graph TD\nA[Start] --> B{vix MCP auth}\nB --> C[Done]", 0)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The decision node renders as a double-line box (corners + both borders).
	for _, want := range []string{"╔", "╗", "╚", "╝", "═", "║"} {
		if !strings.Contains(out, want) {
			t.Errorf("decision box missing %q:\n%s", want, out)
		}
	}

	// The old scattered-rhombus diagonals must be gone entirely.
	for _, bad := range []string{"╱", "╲"} {
		if strings.Contains(out, bad) {
			t.Errorf("decision node still drawn with diagonal %q (scattered rhombus):\n%s", bad, out)
		}
	}

	// The label sits intact on a single row flanked by the double side borders.
	var labelRow string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "vix MCP auth") {
			labelRow = line
			break
		}
	}
	if labelRow == "" {
		t.Fatalf("decision label row not found:\n%s", out)
	}
	if !strings.Contains(labelRow, "║") {
		t.Errorf("decision label row not flanked by a double border:\n%q", labelRow)
	}

	// Edges attach to the double border via a merged junction (not a raw
	// single-line tee overwriting the border): the outgoing edge here merges
	// the bottom border into ╤.
	if !strings.Contains(out, "╤") {
		t.Errorf("outgoing edge did not merge into the double border (expected ╤):\n%s", out)
	}
}

// TestRenderASCIISubgraphDoesNotError guards that a flowchart carrying subgraph
// containers still renders in the terminal (or is handled) without erroring — the
// terminal path is intentionally left unchanged by the whiteboard-clustering work.
func TestRenderASCIISubgraphDoesNotError(t *testing.T) {
	src := "flowchart TB\n" +
		"A --> B\n" +
		"subgraph CI[\"CI Pipeline\"]\n" +
		"  lint --> build\n" +
		"end\n" +
		"B --> lint"
	out, err := RenderASCII(src, 0)
	if err != nil {
		t.Fatalf("render subgraph flowchart: %v", err)
	}
	// Member labels still appear in the rendered diagram.
	if !strings.Contains(out, "lint") || !strings.Contains(out, "build") {
		t.Errorf("ascii missing subgraph member labels:\n%s", out)
	}
}

// TestRenderASCIINeverPanics guards the recover() in RenderASCII: the vendored
// mermaid-ascii renderer panics on some diagram geometries (an observed
// index-out-of-range while drawing edges), which previously crashed the whole
// TUI during message replay. RenderASCII must turn any such panic into an error
// so callers can fall back to the raw source, never propagate the panic.
func TestRenderASCIINeverPanics(t *testing.T) {
	inputs := []string{
		"",
		"graph LR",
		"flowchart TB\n" + strings.Repeat("A --> B\nB --> A\n", 50),
		"graph TD\n" + strings.Repeat("X-->Y\nY-->Z\nZ-->X\n", 30),
		"not a diagram at all",
	}
	for _, src := range inputs {
		for _, w := range []int{0, 20, 80, 140} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("RenderASCII panicked (width %d): %v", w, r)
					}
				}()
				_, _ = RenderASCII(src, w) // error is fine; a panic is not
			}()
		}
	}
}

func TestRenderASCIISequence(t *testing.T) {
	out, err := RenderASCII("sequenceDiagram\nAlice->>Bob: Hello", 0)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "Alice") || !strings.Contains(out, "Bob") {
		t.Errorf("ascii sequence missing participants:\n%s", out)
	}
}

func TestRenderASCIISanitizesNonASCII(t *testing.T) {
	out, err := RenderASCII("graph LR\nA[png → FEN] --> B[eval move …N]", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Labels must be transliterated (the box-drawing glyphs are legitimately
	// Unicode; only label mojibake — a stray "â" from a split rune — is a bug).
	if strings.Contains(out, "â") {
		t.Errorf("expected no multibyte mojibake in labels, got:\n%s", out)
	}
	if !strings.Contains(out, "png -> FEN") {
		t.Errorf("expected arrow transliterated to ->:\n%s", out)
	}
	if !strings.Contains(out, "eval move ...N") {
		t.Errorf("expected ellipsis transliterated to ...:\n%s", out)
	}
}

// TestRenderASCIIClassStatementNoPhantomNode guards the parser fix for mermaid
// `class` statements: `class a,b name` assigns a style class to existing nodes
// and must never leak a phantom node labelled "class" into the diagram (which
// happened when the statement fell through to the edge-chain parser).
func TestRenderASCIIClassStatementNoPhantomNode(t *testing.T) {
	src := "graph TD\n" +
		"A[Alpha] --> B[Beta]\n" +
		"classDef good fill:#1b5e20,stroke:#66bb6a,color:#fff;\n" +
		"class A,B good;\n"
	out, err := RenderASCII(src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Alpha") || !strings.Contains(out, "Beta") {
		t.Errorf("expected real nodes to render:\n%s", out)
	}
	if strings.Contains(out, "class") {
		t.Errorf("phantom 'class' node leaked into diagram:\n%s", out)
	}
}

// TestRenderASCIIEmojiPreserved checks that emoji (and other non-punctuation
// Unicode) survive rendering intact rather than being replaced with '?' or
// shredded into mojibake.
func TestRenderASCIIEmojiPreserved(t *testing.T) {
	out, err := RenderASCII("graph LR\nA[\"🚀 launch\"] --> B[\"done ✅\"]", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "🚀") || !strings.Contains(out, "✅") {
		t.Errorf("expected emoji preserved in output:\n%s", out)
	}
	if strings.Contains(out, "?") {
		t.Errorf("emoji were sanitized to '?':\n%s", out)
	}
	if strings.Contains(out, "â") {
		t.Errorf("multibyte mojibake in output:\n%s", out)
	}
}

// TestRenderASCIIEmojiWidthAligned verifies the width-aware fix: because
// double-width glyphs occupy two grid cells (glyph + empty continuation), every
// rendered row must have the same terminal display width, keeping boxes aligned.
func TestRenderASCIIEmojiWidthAligned(t *testing.T) {
	out, err := RenderASCII("graph LR\nA[\"🚀 launch\"] --> B[\"plain\"]", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := -1
	for _, line := range strings.Split(out, "\n") {
		w := uniseg.StringWidth(line)
		if want == -1 {
			want = w
			continue
		}
		if w != want {
			t.Errorf("row display width %d != %d (misaligned):\n%q\nfull:\n%s", w, want, line, out)
		}
	}
}

func TestScenesFromMermaid(t *testing.T) {
	items := []MermaidScene{
		{Name: "Arch", Context: "the flow", Mermaid: "graph LR\nA[Client]-->B[(DB)]"},
		{Name: "Empty", Context: "n/a", Mermaid: "not a flowchart"},
	}
	scenes := ScenesFromMermaid(items)
	if len(scenes) != 2 {
		t.Fatalf("scenes = %d, want 2", len(scenes))
	}
	if scenes[0].Name != "Arch" || scenes[0].Context != "the flow" || len(scenes[0].Nodes) != 2 {
		t.Errorf("scene[0] = %+v", scenes[0])
	}
	// A non-flowchart scene keeps name/context with an empty (non-nil) canvas.
	if scenes[1].Name != "Empty" || scenes[1].Nodes == nil || len(scenes[1].Nodes) != 0 {
		t.Errorf("scene[1] = %+v (nodes should be empty, non-nil)", scenes[1])
	}
	// CompressScenes round-trips through base64url+inflate back to valid JSON.
	q, err := CompressScenes(scenes)
	if err != nil {
		t.Fatal(err)
	}
	var back []Scene
	if err := json.Unmarshal([]byte(decompressParam(t, q)), &back); err != nil {
		t.Fatalf("compressed scenes not valid JSON: %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("decoded scenes = %d", len(back))
	}
}

func TestLinkForIsCompactAndPercentFree(t *testing.T) {
	// A large graph must still yield a short URL (deflate) with no '%' so
	// terminals don't re-encode the OSC-8 hyperlink and browsers don't strip the
	// referrer / hit URL-length limits.
	var sb strings.Builder
	sb.WriteString("graph LR\n")
	for i := 0; i < 16; i++ {
		sb.WriteString("N")
		sb.WriteByte(byte('a' + i))
		sb.WriteString("[a fairly long node label with words] --> M\n")
	}
	link, err := LinkFor("http://localhost:1337", "dcd5d12e-3186-4b3b-96d3-c70cc38d6203", sb.String())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(link, "%") {
		t.Errorf("URL must be percent-free (base64url); got %q", link)
	}
	if len(link) > 2000 {
		t.Errorf("compressed URL too long (%d chars): %q", len(link), link)
	}
}

func TestParseStyleStatement(t *testing.T) {
	g, ok := parseFlowchart("graph TD\nA[Start] --> B\nstyle A fill:#f96,stroke:#333")
	if !ok {
		t.Fatal("expected parse")
	}
	if g.nodes["A"].color != "#f96" || g.nodes["A"].borderColor != "#333" {
		t.Errorf("A style = %+v, want fill #f96 / stroke #333", g.nodes["A"])
	}
	// An unstyled node keeps no explicit color (falls back to the shape default).
	if g.nodes["B"].color != "" || g.nodes["B"].borderColor != "" {
		t.Errorf("B should have no explicit color, got %+v", g.nodes["B"])
	}
}

func TestParseClassDefAndClass(t *testing.T) {
	src := "graph LR\nA --> B\nA --> C\n" +
		"classDef fan fill:#8b5cf6,stroke:#6d28d9\n" +
		"class A,B fan"
	g, ok := parseFlowchart(src)
	if !ok {
		t.Fatal("expected parse")
	}
	for _, id := range []string{"A", "B"} {
		if g.nodes[id].color != "#8b5cf6" || g.nodes[id].borderColor != "#6d28d9" {
			t.Errorf("%s class color = %+v, want fill #8b5cf6 / stroke #6d28d9", id, g.nodes[id])
		}
	}
	if g.nodes["C"].color != "" {
		t.Errorf("C was not in the class; should be uncolored, got %+v", g.nodes["C"])
	}
}

func TestStyleOverridesClass(t *testing.T) {
	src := "graph TD\nA --> B\n" +
		"classDef base fill:#111,stroke:#222\n" +
		"class A base\n" +
		"style A fill:#fff"
	g, ok := parseFlowchart(src)
	if !ok {
		t.Fatal("expected parse")
	}
	// Direct style wins on fill; stroke (only set by the class) is retained.
	if g.nodes["A"].color != "#fff" {
		t.Errorf("style should win: fill = %q, want #fff", g.nodes["A"].color)
	}
	if g.nodes["A"].borderColor != "#222" {
		t.Errorf("class stroke should remain: %q, want #222", g.nodes["A"].borderColor)
	}
}

func TestStyleForwardReference(t *testing.T) {
	// The style line precedes the edge that introduces the node.
	g, ok := parseFlowchart("graph TD\nstyle A fill:#0f0\nA --> B")
	if !ok {
		t.Fatal("expected parse")
	}
	if g.nodes["A"] == nil || g.nodes["A"].color != "#0f0" {
		t.Errorf("forward style not applied: %+v", g.nodes["A"])
	}
}

func TestToSceneCarriesColor(t *testing.T) {
	src := "graph TD\nA --> B\nstyle A fill:#f96,stroke:#c00"
	scene, ok := ToScene(src, "styled")
	if !ok {
		t.Fatal("expected scene")
	}
	byID := map[string]Node{}
	for _, n := range scene.Nodes {
		byID[n.ID] = n
	}
	if byID["A"].Color != "#f96" || byID["A"].BorderColor != "#c00" {
		t.Errorf("A canvas color = %s/%s, want #f96/#c00", byID["A"].Color, byID["A"].BorderColor)
	}
	// The unstyled node keeps the shape default (rectangle blue).
	def := shapeColors[shapeRectangle]
	if byID["B"].Color != def[0] || byID["B"].BorderColor != def[1] {
		t.Errorf("B color = %s/%s, want shape default %s/%s", byID["B"].Color, byID["B"].BorderColor, def[0], def[1])
	}
	// Colors survive the compressed scenes round-trip.
	q, err := CompressScenes([]Scene{scene})
	if err != nil {
		t.Fatal(err)
	}
	var back []Scene
	if err := json.Unmarshal([]byte(decompressParam(t, q)), &back); err != nil {
		t.Fatal(err)
	}
	if back[0].Nodes[0].Color == "" {
		t.Errorf("color lost through round-trip: %+v", back[0].Nodes)
	}
}

func TestSanitizeColorRejectsGarbage(t *testing.T) {
	// Accepted forms.
	for _, ok := range []string{"#fff", "#ffaa00", "#ffaa00cc", "rgb(1,2,3)", "rgba(1,2,3,0.5)", "red"} {
		if sanitizeColor(ok) == "" {
			t.Errorf("expected %q accepted", ok)
		}
	}
	// Rejected: CSS/URL injection, malformed hex, overlong values.
	for _, bad := range []string{"url(javascript:alert(1))", "#xyz", "#12", "expression(x)", strings.Repeat("a", 40), "; background:url(x)"} {
		if got := sanitizeColor(bad); got != "" {
			t.Errorf("expected %q rejected, got %q", bad, got)
		}
	}
	// A garbage fill leaves the node uncolored (shape default preserved).
	g, ok := parseFlowchart("graph TD\nA --> B\nstyle A fill:url(javascript:alert(1))")
	if !ok {
		t.Fatal("expected parse")
	}
	if g.nodes["A"].color != "" {
		t.Errorf("garbage fill should be ignored, got %q", g.nodes["A"].color)
	}
}

func TestCleanLabelConvertsBreaks(t *testing.T) {
	cases := map[string]string{
		`"inspect once<br/>metrics: churn"`: "inspect once\nmetrics: churn",
		"a<br>b":                            "a\nb",
		"a<BR />b":                          "a\nb",
		"a<br  />b":                         "a\nb",
		"line1<br/> line2<br/>line3":        "line1\nline2\nline3",
		"no breaks here":                    "no breaks here",
		"<br/>leading":                      "leading",
		"trailing<br/>":                     "trailing",
	}
	for in, want := range cases {
		if got := cleanLabel(in); got != want {
			t.Errorf("cleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNodeSizeGrowsWithContent(t *testing.T) {
	// Short labels keep the per-shape minimum so existing diagrams are unchanged.
	if w, h := nodeSize("Hi", shapeRectangle); w != 150 || h != 80 {
		t.Errorf("short rectangle = %dx%d, want min 150x80", w, h)
	}
	if w, h := nodeSize("Q", shapeDiamond); w != 128 || h != 128 {
		t.Errorf("short diamond = %dx%d, want min 128x128", w, h)
	}
	if w, h := nodeSize("DB", shapeDatabase); w != 128 || h != 140 {
		t.Errorf("short database = %dx%d, want min 128x140", w, h)
	}
	// A long single line grows the width past the minimum; height stays one line.
	long := "a fairly long single line label that should not fit in 150px"
	wLong, hLong := nodeSize(long, shapeRectangle)
	if wLong <= 150 {
		t.Errorf("long label width = %d, want > 150", wLong)
	}
	if hLong != 80 {
		t.Errorf("single-line height = %d, want min 80", hLong)
	}
	// Multiple lines grow the height; more lines => taller.
	_, h2 := nodeSize("one\ntwo\nthree\nfour", shapeRectangle)
	if h2 <= 80 {
		t.Errorf("multi-line height = %d, want > 80", h2)
	}
	_, h1 := nodeSize("one\ntwo", shapeRectangle)
	if !(h2 > h1) {
		t.Errorf("four lines (%d) should be taller than two lines (%d)", h2, h1)
	}
}

// TestLayoutBoxesDoNotOverlap guards the size-aware placement: even with long
// and multi-line (<br/>) labels that grow boxes well past the defaults, no two
// node rectangles may overlap.
func TestLayoutBoxesDoNotOverlap(t *testing.T) {
	src := "graph TD\n" +
		`A["inspect codebase once<br/>metrics: churn, coverage"] --> B[fetch commit history]` + "\n" +
		"A --> C[a considerably longer sibling label here]\n" +
		"B --> D[done]\nC --> D"
	scene, ok := ToScene(src, "sizes")
	if !ok {
		t.Fatal("expected scene")
	}
	overlap := func(a, b Node) bool {
		return a.X < b.X+b.Width && b.X < a.X+a.Width &&
			a.Y < b.Y+b.Height && b.Y < a.Y+a.Height
	}
	for i := 0; i < len(scene.Nodes); i++ {
		if scene.Nodes[i].Width <= 0 || scene.Nodes[i].Height <= 0 {
			t.Errorf("node %s has non-positive size %dx%d", scene.Nodes[i].ID, scene.Nodes[i].Width, scene.Nodes[i].Height)
		}
		for j := i + 1; j < len(scene.Nodes); j++ {
			if overlap(scene.Nodes[i], scene.Nodes[j]) {
				t.Errorf("nodes %s and %s overlap: %+v vs %+v",
					scene.Nodes[i].ID, scene.Nodes[j].ID, scene.Nodes[i], scene.Nodes[j])
			}
		}
	}
}

// TestScenesCarrySizeAndBreaks verifies the browser-facing scene JSON carries
// per-node width/height and that <br/> in a label becomes a real newline
// (not literal "<br/>" text) through the compressed round-trip.
func TestScenesCarrySizeAndBreaks(t *testing.T) {
	items := []MermaidScene{
		{Name: "Arch", Mermaid: `graph TD` + "\n" + `A["step one<br/>step two<br/>step three<br/>step four"] --> B[next]`},
	}
	scenes := ScenesFromMermaid(items)
	q, err := CompressScenes(scenes)
	if err != nil {
		t.Fatal(err)
	}
	var back []Scene
	if err := json.Unmarshal([]byte(decompressParam(t, q)), &back); err != nil {
		t.Fatalf("scenes JSON: %v", err)
	}
	if len(back) != 1 || len(back[0].Nodes) != 2 {
		t.Fatalf("decoded scenes = %+v", back)
	}
	var a Node
	for _, n := range back[0].Nodes {
		if n.ID == "A" {
			a = n
		}
		if n.Width <= 0 || n.Height <= 0 {
			t.Errorf("node %s missing size: %+v", n.ID, n)
		}
	}
	if strings.Contains(a.Label, "<br") {
		t.Errorf("label kept literal <br>: %q", a.Label)
	}
	if a.Label != "step one\nstep two\nstep three\nstep four" {
		t.Errorf("label = %q, want four newline-separated lines", a.Label)
	}
	// The four-line label must be taller than a one-line rectangle minimum.
	if a.Height <= 80 {
		t.Errorf("four-line node height = %d, want > 80", a.Height)
	}
}

func TestBuiltinClassesNoClassDef(t *testing.T) {
	// No classDef line: the built-in fanOut/fanIn/decision palette applies.
	src := "graph LR\nA --> B --> C\nclass A fanOut\nclass C fanIn\nclass B decision"
	g, ok := parseFlowchart(src)
	if !ok {
		t.Fatal("expected parse")
	}
	want := map[string][2]string{
		"A": {"#8b5cf6", "#6d28d9"},
		"C": {"#0ea5e9", "#0369a1"},
		"B": {"#f59e0b", "#b45309"},
	}
	for id, w := range want {
		if g.nodes[id].color != w[0] || g.nodes[id].borderColor != w[1] {
			t.Errorf("%s = %s/%s, want built-in %s/%s", id, g.nodes[id].color, g.nodes[id].borderColor, w[0], w[1])
		}
	}
}

func TestExplicitClassDefOverridesBuiltin(t *testing.T) {
	// A user classDef of the same name wins over the built-in palette.
	src := "graph LR\nA --> B\nclassDef fanOut fill:#123456,stroke:#654321\nclass A fanOut"
	g, ok := parseFlowchart(src)
	if !ok {
		t.Fatal("expected parse")
	}
	if g.nodes["A"].color != "#123456" || g.nodes["A"].borderColor != "#654321" {
		t.Errorf("override failed: A = %s/%s, want #123456/#654321", g.nodes["A"].color, g.nodes["A"].borderColor)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestParseSubgraphMembershipAndNesting(t *testing.T) {
	src := "flowchart TB\n" +
		"A --> B\n" +
		"subgraph CI[\"CI Pipeline\"]\n" +
		"  direction LR\n" +
		"  lint --> build\n" +
		"  subgraph Inner\n" +
		"    scan\n" +
		"  end\n" +
		"end\n" +
		"B --> lint"
	g, ok := parseFlowchart(src)
	if !ok {
		t.Fatal("expected parse")
	}
	// Two clusters recorded, in declaration order (outer before inner).
	if len(g.clusterOrder) != 2 || g.clusterOrder[0] != "CI" || g.clusterOrder[1] != "Inner" {
		t.Fatalf("clusterOrder = %v, want [CI Inner]", g.clusterOrder)
	}
	if g.clusters["CI"].label != "CI Pipeline" {
		t.Errorf("CI label = %q, want %q", g.clusters["CI"].label, "CI Pipeline")
	}
	if g.clusters["CI"].direction != "LR" {
		t.Errorf("CI direction = %q, want LR", g.clusters["CI"].direction)
	}
	// Nesting: Inner's parent is CI; CI is top level.
	if g.clusters["Inner"].parent != "CI" {
		t.Errorf("Inner.parent = %q, want CI", g.clusters["Inner"].parent)
	}
	if g.clusters["CI"].parent != "" {
		t.Errorf("CI.parent = %q, want top level", g.clusters["CI"].parent)
	}
	// Membership (innermost wins).
	if g.nodes["lint"].cluster != "CI" || g.nodes["build"].cluster != "CI" {
		t.Errorf("lint/build cluster = %q/%q, want CI", g.nodes["lint"].cluster, g.nodes["build"].cluster)
	}
	if g.nodes["scan"].cluster != "Inner" {
		t.Errorf("scan cluster = %q, want Inner", g.nodes["scan"].cluster)
	}
	// Top-level nodes carry no cluster.
	if g.nodes["A"].cluster != "" || g.nodes["B"].cluster != "" {
		t.Errorf("A/B cluster = %q/%q, want top level", g.nodes["A"].cluster, g.nodes["B"].cluster)
	}
}

func TestParseEdgeToSubgraphIDNoPhantomNode(t *testing.T) {
	// An edge targeting a subgraph id (declared before or after the edge) must not
	// create a phantom leaf node — it resolves to the cluster.
	for _, src := range []string{
		"flowchart TB\nsubgraph CI\n  lint\nend\nA --> CI", // declared before
		"flowchart TB\nA --> CI\nsubgraph CI\n  lint\nend", // forward reference
	} {
		g, ok := parseFlowchart(src)
		if !ok {
			t.Fatalf("expected parse for %q", src)
		}
		if _, isNode := g.nodes["CI"]; isNode {
			t.Errorf("CI registered as a phantom node for src:\n%s", src)
		}
		if contains(g.order, "CI") {
			t.Errorf("CI leaked into node order for src:\n%s", src)
		}
		// The edge is preserved and still references the cluster id.
		found := false
		for _, e := range g.edges {
			if e.from == "A" && e.to == "CI" {
				found = true
			}
		}
		if !found {
			t.Errorf("edge A->CI missing for src:\n%s", src)
		}
	}
}

func TestParseSubgraphStyling(t *testing.T) {
	src := "flowchart TB\n" +
		"subgraph CI\n  lint\nend\n" +
		"classDef pipe fill:#123456,stroke:#654321\n" +
		"class CI pipe"
	g, ok := parseFlowchart(src)
	if !ok {
		t.Fatal("expected parse")
	}
	if g.clusters["CI"].color != "#123456" || g.clusters["CI"].borderColor != "#654321" {
		t.Errorf("CI style = %s/%s, want #123456/#654321", g.clusters["CI"].color, g.clusters["CI"].borderColor)
	}
	// The cluster id must not have become a node via the class assignment.
	if _, isNode := g.nodes["CI"]; isNode {
		t.Error("styling a subgraph id must not create a node")
	}
}

// sceneByID indexes a scene's nodes by id for assertions.
func sceneByID(s Scene) map[string]Node {
	m := make(map[string]Node, len(s.Nodes))
	for _, n := range s.Nodes {
		m[n.ID] = n
	}
	return m
}

func TestLayoutSubgraphEmitsGroupAndNestsChildren(t *testing.T) {
	src := "flowchart TB\n" +
		"A[Start] --> B[Build]\n" +
		"subgraph CI[\"CI Pipeline\"]\n" +
		"  lint --> unit\n" +
		"  unit --> pkg\n" +
		"end\n" +
		"B --> lint\n" +
		"CI --> D[Deploy]"
	scene, ok := ToScene(src, "flow")
	if !ok {
		t.Fatal("expected scene")
	}
	by := sceneByID(scene)

	// The container is emitted as a group node with its title and no parent.
	ci, ok := by["CI"]
	if !ok {
		t.Fatal("group node CI not emitted")
	}
	if ci.Shape != shapeGroup {
		t.Errorf("CI shape = %q, want %q", ci.Shape, shapeGroup)
	}
	if ci.Label != "CI Pipeline" {
		t.Errorf("CI label = %q, want %q", ci.Label, "CI Pipeline")
	}
	if ci.ParentID != "" {
		t.Errorf("CI parent = %q, want top level", ci.ParentID)
	}
	// A group node must precede its children in the emitted order (React Flow
	// requires a parent to be defined before its children).
	ciIdx, lintIdx := -1, -1
	for i, n := range scene.Nodes {
		if n.ID == "CI" {
			ciIdx = i
		}
		if n.ID == "lint" {
			lintIdx = i
		}
	}
	if !(ciIdx >= 0 && lintIdx > ciIdx) {
		t.Errorf("group CI (idx %d) must be emitted before child lint (idx %d)", ciIdx, lintIdx)
	}

	// Members reference the container and are nested within its box (relative
	// coords, below the title band, inside all four edges).
	for _, id := range []string{"lint", "unit", "pkg"} {
		n := by[id]
		if n.ParentID != "CI" {
			t.Errorf("%s parent = %q, want CI", id, n.ParentID)
		}
		if n.X < 0 || n.Y < clusterTitleH {
			t.Errorf("%s at (%d,%d) not below the title band / inside left edge", id, n.X, n.Y)
		}
		if n.X+n.Width > ci.Width || n.Y+n.Height > ci.Height {
			t.Errorf("%s box (%d,%d %dx%d) exceeds group CI bounds %dx%d",
				id, n.X, n.Y, n.Width, n.Height, ci.Width, ci.Height)
		}
	}

	// Top-level nodes carry no parent.
	for _, id := range []string{"A", "B", "D"} {
		if by[id].ParentID != "" {
			t.Errorf("%s parent = %q, want top level", id, by[id].ParentID)
		}
	}

	// Cross-cluster and cluster-targeted edges survive with their endpoints.
	hasEdge := func(from, to string) bool {
		for _, e := range scene.Edges {
			if e.From == from && e.To == to {
				return true
			}
		}
		return false
	}
	if !hasEdge("B", "lint") {
		t.Error("cross-cluster edge B->lint missing")
	}
	if !hasEdge("CI", "D") {
		t.Error("cluster-source edge CI->D missing")
	}
}

func TestLayoutNestedSubgraphBoundsNested(t *testing.T) {
	src := "flowchart TB\n" +
		"subgraph Outer\n" +
		"  o1 --> o2\n" +
		"  subgraph Inner\n" +
		"    i1 --> i2\n" +
		"  end\n" +
		"  o2 --> i1\n" +
		"end"
	scene, ok := ToScene(src, "nested")
	if !ok {
		t.Fatal("expected scene")
	}
	by := sceneByID(scene)
	outer, inner := by["Outer"], by["Inner"]
	if outer.Shape != shapeGroup || inner.Shape != shapeGroup {
		t.Fatalf("expected group shapes, got Outer=%q Inner=%q", outer.Shape, inner.Shape)
	}
	if inner.ParentID != "Outer" {
		t.Errorf("Inner.ParentID = %q, want Outer", inner.ParentID)
	}
	// Inner's box (relative to Outer) sits fully inside Outer's box.
	if inner.X < 0 || inner.Y < clusterTitleH || inner.X+inner.Width > outer.Width || inner.Y+inner.Height > outer.Height {
		t.Errorf("Inner box (%d,%d %dx%d) not nested within Outer %dx%d",
			inner.X, inner.Y, inner.Width, inner.Height, outer.Width, outer.Height)
	}
	// Inner's members reference Inner and sit inside it.
	for _, id := range []string{"i1", "i2"} {
		n := by[id]
		if n.ParentID != "Inner" {
			t.Errorf("%s parent = %q, want Inner", id, n.ParentID)
		}
		if n.X+n.Width > inner.Width || n.Y+n.Height > inner.Height {
			t.Errorf("%s exceeds Inner bounds", id)
		}
	}
}

func TestLayoutBackCompatNoParentIDs(t *testing.T) {
	// A subgraph-free flowchart emits no group nodes and no ParentID, and its
	// JSON omits parent_id entirely (byte-compatible with pre-subgraph links).
	scene, ok := ToScene("graph TD\nA-->B\nA-->C\nB-->D\nC-->D", "flat")
	if !ok {
		t.Fatal("expected scene")
	}
	for _, n := range scene.Nodes {
		if n.Shape == shapeGroup {
			t.Errorf("unexpected group node %q in a subgraph-free flowchart", n.ID)
		}
		if n.ParentID != "" {
			t.Errorf("node %q has ParentID %q, want none", n.ID, n.ParentID)
		}
	}
	raw, _ := json.Marshal(scene.Nodes[0])
	if strings.Contains(string(raw), "parent_id") {
		t.Errorf("parent_id must be omitted for parent-less nodes; got %s", raw)
	}
}

func TestLayoutGroupSurvivesCompressedRoundTrip(t *testing.T) {
	src := "flowchart TB\nsubgraph CI[\"CI Pipeline\"]\n lint --> build\nend\nA --> lint"
	scene, ok := ToScene(src, "rt")
	if !ok {
		t.Fatal("expected scene")
	}
	q, err := CompressScenes([]Scene{scene})
	if err != nil {
		t.Fatal(err)
	}
	var back []Scene
	if err := json.Unmarshal([]byte(decompressParam(t, q)), &back); err != nil {
		t.Fatalf("compressed scenes not valid JSON: %v", err)
	}
	by := sceneByID(back[0])
	if by["CI"].Shape != shapeGroup || by["CI"].Label != "CI Pipeline" {
		t.Errorf("group node lost through round-trip: %+v", by["CI"])
	}
	if by["lint"].ParentID != "CI" || by["build"].ParentID != "CI" {
		t.Errorf("membership lost through round-trip: lint=%q build=%q", by["lint"].ParentID, by["build"].ParentID)
	}
}

// TestLayoutSourceStaysOnTopRow guards decision 2: an entry node whose only
// incoming edge is feedback from downstream (here from inside a subgraph) must
// stay on the top row rather than sinking below its successors.
func TestLayoutSourceStaysOnTopRow(t *testing.T) {
	src := "flowchart TB\n" +
		"dev --> pr\n" +
		"subgraph CI\n  lint --> build\nend\n" +
		"pr --> lint\n" +
		"build --> dev" // feedback edge into the entry node
	scene, ok := ToScene(src, "feedback")
	if !ok {
		t.Fatal("expected scene")
	}
	by := sceneByID(scene)
	// Among the top-level items (dev, pr, CI group), dev must be the highest.
	dev, pr, ci := by["dev"], by["pr"], by["CI"]
	if !(dev.Y < pr.Y && dev.Y <= ci.Y) {
		t.Errorf("entry dev (y=%d) should sit above pr (y=%d) and group CI (y=%d)", dev.Y, pr.Y, ci.Y)
	}
}
