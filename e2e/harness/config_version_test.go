package harness

import (
	"strings"
	"testing"
)

func TestWithConfigVersion(t *testing.T) {
	// Adds version when absent, preserving placeholders inside string values.
	got := withConfigVersion(`{"deny_list":{"paths":["{{HOME}}/.ssh"]}}`)
	if !strings.Contains(got, `"version":1`) {
		t.Errorf("missing version stamp: %s", got)
	}
	if !strings.Contains(got, "{{HOME}}/.ssh") {
		t.Errorf("placeholder not preserved: %s", got)
	}

	// Leaves an existing version untouched.
	got = withConfigVersion(`{"version":1,"features":{"jobs":false}}`)
	if strings.Count(got, "version") != 1 {
		t.Errorf("duplicated version: %s", got)
	}

	// Non-JSON is returned unchanged.
	if got := withConfigVersion("not json"); got != "not json" {
		t.Errorf("non-JSON mutated: %q", got)
	}
}
