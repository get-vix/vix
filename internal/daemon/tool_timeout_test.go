package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// resolveToolTimeout enforces the configurable tool timeout rule:
// every tool is capped at the floor (defaultTimeout) by default; only bash
// and glob_files can raise that ceiling via `timeout` params, up to the cap
// (maxTimeout). Both bounds are sourced from ProjectConfig.ToolTimeouts,
// which in turn comes from settings.json. This test exercises the historical
// hard-coded defaults (120s / 600s).
func TestResolveToolTimeout(t *testing.T) {
	const (
		def = 120 * time.Second
		max = 600 * time.Second
	)

	tests := []struct {
		name   string
		tool   string
		params map[string]any
		want   time.Duration
	}{
		// Non-overridable tools: always 120s.
		{"read_file default", "read_file", nil, def},
		{"grep default", "grep", nil, def},
		{"write_file default", "write_file", nil, def},
		{"lsp_query default", "lsp_query", nil, def},
		{"web_fetch default", "web_fetch", nil, def},
		{"tool_orchestrator capped", "tool_orchestrator", nil, def},
		{
			"tool_orchestrator ignores timeout param",
			"tool_orchestrator",
			map[string]any{"timeout": float64(400)},
			def,
		},
		{
			"read_file ignores timeout param",
			"read_file",
			map[string]any{"timeout": float64(300)},
			def,
		},

		// bash overrides.
		{"bash nil params", "bash", nil, def},
		{"bash empty timeout", "bash", map[string]any{}, def},
		{"bash zero timeout", "bash", map[string]any{"timeout": float64(0)}, def},
		{"bash negative timeout", "bash", map[string]any{"timeout": float64(-10)}, def},
		{"bash below floor", "bash", map[string]any{"timeout": float64(60)}, def},
		{"bash exact default", "bash", map[string]any{"timeout": float64(120)}, def},
		{"bash mid override", "bash", map[string]any{"timeout": float64(300)}, 300 * time.Second},
		{"bash exact cap", "bash", map[string]any{"timeout": float64(600)}, max},
		{"bash above cap", "bash", map[string]any{"timeout": float64(900)}, max},
		{"bash int param", "bash", map[string]any{"timeout": 300}, 300 * time.Second},
		{"bash int64 param", "bash", map[string]any{"timeout": int64(400)}, 400 * time.Second},

		// glob_files overrides.
		{"glob nil params", "glob_files", nil, def},
		{"glob mid override", "glob_files", map[string]any{"timeout": float64(300)}, 300 * time.Second},
		{"glob above cap", "glob_files", map[string]any{"timeout": float64(1200)}, max},
		{"glob below floor", "glob_files", map[string]any{"timeout": float64(30)}, def},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveToolTimeout(tt.tool, tt.params, def, max)
			if got != tt.want {
				t.Errorf("resolveToolTimeout(%q, %v, %v, %v) = %v, want %v",
					tt.tool, tt.params, def, max, got, tt.want)
			}
		})
	}
}

// TestResolveToolTimeoutConfiguredBounds verifies that the floor/cap bounds
// passed to resolveToolTimeout (sourced from settings.json's tool_timeouts
// block at runtime) actually take effect. If these fail, the settings.json
// knob isn't plumbed through correctly.
func TestResolveToolTimeoutConfiguredBounds(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		params map[string]any
		def    time.Duration
		max    time.Duration
		want   time.Duration
	}{
		// Tight bounds (60s floor, 300s cap): operator wants shorter timeouts.
		{
			"tight bash within bounds",
			"bash",
			map[string]any{"timeout": float64(200)},
			60 * time.Second, 300 * time.Second,
			200 * time.Second,
		},
		{
			"tight bash clamped to configured cap",
			"bash",
			map[string]any{"timeout": float64(500)},
			60 * time.Second, 300 * time.Second,
			300 * time.Second,
		},
		{
			"tight bash raised to configured floor",
			"bash",
			map[string]any{"timeout": float64(30)},
			60 * time.Second, 300 * time.Second,
			60 * time.Second,
		},
		{
			"tight non-overridable uses configured floor",
			"read_file",
			nil,
			60 * time.Second, 300 * time.Second,
			60 * time.Second,
		},

		// Loose bounds (60s floor, 1200s cap): operator wants to allow longer
		// bash timeouts than the historical 600s hard-cap. This proves the old
		// hard-cap is gone — the cap comes from config.
		{
			"loose bash beyond historical cap",
			"bash",
			map[string]any{"timeout": float64(900)},
			60 * time.Second, 1200 * time.Second,
			900 * time.Second,
		},
		{
			"loose bash at configured cap",
			"bash",
			map[string]any{"timeout": float64(1200)},
			60 * time.Second, 1200 * time.Second,
			1200 * time.Second,
		},
		{
			"loose bash above configured cap",
			"bash",
			map[string]any{"timeout": float64(9999)},
			60 * time.Second, 1200 * time.Second,
			1200 * time.Second,
		},

		// Equal bounds (200s == 200s): pathological but valid, the override
		// path collapses to "always return the single value".
		{
			"equal bash no override",
			"bash",
			nil,
			200 * time.Second, 200 * time.Second,
			200 * time.Second,
		},
		{
			"equal bash with override inside band",
			"bash",
			map[string]any{"timeout": float64(200)},
			200 * time.Second, 200 * time.Second,
			200 * time.Second,
		},
		{
			"equal bash with override below floor",
			"bash",
			map[string]any{"timeout": float64(50)},
			200 * time.Second, 200 * time.Second,
			200 * time.Second,
		},
		{
			"equal bash with override above cap",
			"bash",
			map[string]any{"timeout": float64(500)},
			200 * time.Second, 200 * time.Second,
			200 * time.Second,
		},

		// glob_files honours configured bounds just like bash.
		{
			"tight glob clamped to configured cap",
			"glob_files",
			map[string]any{"timeout": float64(500)},
			60 * time.Second, 300 * time.Second,
			300 * time.Second,
		},

		// Historical defaults (120/600) as a regression anchor — confirms
		// the new signature doesn't change behaviour at the old bounds.
		{
			"historical defaults bash mid",
			"bash",
			map[string]any{"timeout": float64(300)},
			120 * time.Second, 600 * time.Second,
			300 * time.Second,
		},
		{
			"historical defaults bash above old cap",
			"bash",
			map[string]any{"timeout": float64(900)},
			120 * time.Second, 600 * time.Second,
			600 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveToolTimeout(tt.tool, tt.params, tt.def, tt.max)
			if got != tt.want {
				t.Errorf("resolveToolTimeout(%q, %v, def=%v, max=%v) = %v, want %v",
					tt.tool, tt.params, tt.def, tt.max, got, tt.want)
			}
		})
	}
}

// Ensure the schema for glob_files exposes the new timeout + justification
// fields so the model can discover them. The dev-mode Required list must
// enforce reason_to_increase_timeout the same way bash does.
func TestGlobFilesTimeoutSchema(t *testing.T) {
	schemas := ToolSchemas()
	var props map[string]any
	for _, s := range schemas {
		if s.Name == "glob_files" {
			if p, ok := s.InputSchema["properties"].(map[string]any); ok {
				props = p
			}
			break
		}
	}
	if props == nil {
		t.Fatal("glob_files schema not found or properties not map[string]any")
	}
	if _, ok := props["timeout"]; !ok {
		t.Error("glob_files schema missing `timeout` property")
	}
	if _, ok := props["reason_to_increase_timeout"]; !ok {
		t.Error("glob_files schema missing `reason_to_increase_timeout` property")
	}
}

// TestToolSchemasWithBoundsRendersDescriptions verifies the bash and
// glob_files description strings get re-rendered with the configured floor
// and cap values, not the hard-coded 120/600 defaults. Without this check
// the LLM would still read "defaults to 120, hard-capped at 600" even when
// settings.json has raised or lowered the actual window, leading it to make
// bad timeout decisions (asking for values that silently get clamped).
func TestToolSchemasWithBoundsRendersDescriptions(t *testing.T) {
	findProp := func(schemas []llm.ToolParam, toolName, propName string) string {
		t.Helper()
		for _, s := range schemas {
			if s.Name != toolName {
				continue
			}
			props, ok := s.InputSchema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s: properties not map[string]any", toolName)
			}
			entry, ok := props[propName].(map[string]any)
			if !ok {
				t.Fatalf("%s.%s: not a map", toolName, propName)
			}
			desc, _ := entry["description"].(string)
			return desc
		}
		t.Fatalf("tool %q not found in schema", toolName)
		return ""
	}
	findToolDesc := func(schemas []llm.ToolParam, toolName string) string {
		t.Helper()
		for _, s := range schemas {
			if s.Name != toolName {
				continue
			}
			return s.Description
		}
		t.Fatalf("tool %q not found in schema", toolName)
		return ""
	}

	// Tight bounds: 60s floor, 300s cap (5 min).
	schemas := ToolSchemasWithBounds(60*time.Second, 300*time.Second)

	bashDesc := findToolDesc(schemas, "bash")
	if !strings.Contains(bashDesc, "60 seconds by default") {
		t.Errorf("bash tool Description should mention 60 seconds default; got: %s", bashDesc)
	}
	if !strings.Contains(bashDesc, "300 seconds") || !strings.Contains(bashDesc, "5 minutes") {
		t.Errorf("bash tool Description should mention 300 seconds / 5 minutes cap; got: %s", bashDesc)
	}
	if strings.Contains(bashDesc, "120 seconds") || strings.Contains(bashDesc, "600 seconds") {
		t.Errorf("bash tool Description should NOT mention the historical 120/600 defaults; got: %s", bashDesc)
	}

	bashTimeoutDesc := findProp(schemas, "bash", "timeout")
	if !strings.Contains(bashTimeoutDesc, "defaults to 60") {
		t.Errorf("bash.timeout should say 'defaults to 60'; got: %s", bashTimeoutDesc)
	}
	if !strings.Contains(bashTimeoutDesc, "Hard-capped at 300") {
		t.Errorf("bash.timeout should say 'Hard-capped at 300'; got: %s", bashTimeoutDesc)
	}

	bashReasonDesc := findProp(schemas, "bash", "reason_to_increase_timeout")
	if !strings.Contains(bashReasonDesc, "exceeds 60 seconds") {
		t.Errorf("bash.reason_to_increase_timeout should trigger at 'exceeds 60 seconds'; got: %s", bashReasonDesc)
	}
	if !strings.Contains(bashReasonDesc, "300 seconds") {
		t.Errorf("bash.reason_to_increase_timeout should mention 300s cap; got: %s", bashReasonDesc)
	}

	globTimeoutDesc := findProp(schemas, "glob_files", "timeout")
	if !strings.Contains(globTimeoutDesc, "defaults to 60") {
		t.Errorf("glob_files.timeout should say 'defaults to 60'; got: %s", globTimeoutDesc)
	}
	if !strings.Contains(globTimeoutDesc, "Hard-capped at 300") {
		t.Errorf("glob_files.timeout should say 'Hard-capped at 300'; got: %s", globTimeoutDesc)
	}

	globReasonDesc := findProp(schemas, "glob_files", "reason_to_increase_timeout")
	if !strings.Contains(globReasonDesc, "exceeds 60 seconds") {
		t.Errorf("glob_files.reason_to_increase_timeout should trigger at 'exceeds 60 seconds'; got: %s", globReasonDesc)
	}

	// Loose bounds: 60s floor, 1200s cap (20 min) — proves the old 600s
	// hard-cap is gone and the schema reflects the new cap.
	loose := ToolSchemasWithBounds(60*time.Second, 1200*time.Second)
	looseBashDesc := findToolDesc(loose, "bash")
	if !strings.Contains(looseBashDesc, "1200 seconds") || !strings.Contains(looseBashDesc, "20 minutes") {
		t.Errorf("loose bash Description should mention 1200s / 20 minutes; got: %s", looseBashDesc)
	}

	// Zero values fall back to defaults (120/600).
	defaults := ToolSchemasWithBounds(0, 0)
	defaultsBashDesc := findToolDesc(defaults, "bash")
	if !strings.Contains(defaultsBashDesc, "120 seconds by default") {
		t.Errorf("zero-bounds bash Description should fall back to 120 default; got: %s", defaultsBashDesc)
	}
	if !strings.Contains(defaultsBashDesc, "600 seconds") {
		t.Errorf("zero-bounds bash Description should fall back to 600 cap; got: %s", defaultsBashDesc)
	}

	// ToolSchemas() (zero-arg) must also return the defaults.
	bare := ToolSchemas()
	bareBashDesc := findToolDesc(bare, "bash")
	if !strings.Contains(bareBashDesc, "120 seconds") || !strings.Contains(bareBashDesc, "600 seconds") {
		t.Errorf("bare ToolSchemas() bash Description should mention 120/600 defaults; got: %s", bareBashDesc)
	}
}

// TestFilterToolSchemasWithBoundsPropagates verifies that the bounded
// filter variant produces descriptions with the configured bounds, not the
// hard-coded defaults.
func TestFilterToolSchemasWithBoundsPropagates(t *testing.T) {
	filtered := FilterToolSchemasWithBounds([]string{"bash"}, 60*time.Second, 300*time.Second)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(filtered))
	}
	tool := filtered[0]
	if tool.Name != "bash" {
		t.Fatalf("expected bash tool, got %+v", filtered[0])
	}
	if !strings.Contains(tool.Description, "60 seconds by default") {
		t.Errorf("filtered bash description should reflect configured floor of 60s; got: %s", tool.Description)
	}
	if !strings.Contains(tool.Description, "300 seconds") {
		t.Errorf("filtered bash description should reflect configured cap of 300s; got: %s", tool.Description)
	}
}
