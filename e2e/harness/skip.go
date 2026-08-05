package harness

import (
	"os"
	"runtime"
	"testing"

	"github.com/get-vix/vix/e2e/artifact"
)

// SkipScenario records a "skipped" result in the report and then skips the test.
//
// Skips that happen before Start (staged/pending acceptance specs, disabled
// tests) otherwise write no artifact at all, so they vanish from the HTML report
// rather than showing as skipped. Call this at the top of such a scenario with
// the same Meta you would pass to Start, e.g.
//
//	func TestPending(t *testing.T) {
//	    meta := harness.Meta{Category: "creds", Subcategory: "creds.env_files", ...}
//	    harness.SkipScenario(t, meta, "acceptance spec for #29/#30 — enable when fixed")
//	    h := harness.Start(t, meta, ...) // unreachable; documents the spec
//	    ...
//	}
//
// The reason is surfaced as the test's diagnostics in the report. Only writes an
// artifact under a real e2e run (VIX_E2E=1); a plain `go test` just skips.
func SkipScenario(t *testing.T, meta Meta, reason string) {
	t.Helper()
	if meta.Wire == "" {
		meta.Wire = WireMessages
	}
	if os.Getenv("VIX_E2E") == "1" {
		_, file, line, _ := runtime.Caller(1)
		writeSkipArtifact(t, meta, file, line, reason)
	}
	t.Skip(reason)
}

// writeSkipArtifact emits the start + final (skipped) records so the renderer
// shows the scenario as skipped, with reason as its diagnostics.
func writeSkipArtifact(t *testing.T, meta Meta, file string, line int, reason string) {
	r := artifact.Result{
		Category:    meta.Category,
		Subcategory: meta.Subcategory,
		Name:        t.Name(),
		Description: meta.Description,
		Wire:        string(meta.Wire),
		Variant:     meta.Variant,
		SourceFile:  file,
		SourceLine:  line,
		Source:      captureSource(file, t.Name()),
		Status:      artifact.StatusSkipped,
		Diagnostics: reason,
	}
	root := reportRoot()
	_ = artifact.WriteStart(root, r)
	_ = artifact.WriteFinal(root, r)
}
