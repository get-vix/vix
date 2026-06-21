//go:build linux

package daemon

import (
	"path/filepath"
	"testing"
)

func TestBuildLandlockRules_IncludesResolvedBashDir(t *testing.T) {
	bashPath := resolveBashPath()
	bashDir := filepath.Dir(bashPath)
	rules := buildLandlockRules(t.TempDir(), nil)

	for _, p := range rules.RO {
		if p == bashDir || p == bashPath {
			return
		}
	}

	t.Fatalf("buildLandlockRules missing resolved bash path; bash=%q dir=%q ro=%v", bashPath, bashDir, rules.RO)
}
