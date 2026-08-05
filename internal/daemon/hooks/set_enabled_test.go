package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreSetEnabledSurgical verifies SetEnabled rewrites only the enabled
// value of hook.json, preserving other keys, their order, and unknown fields.
func TestStoreSetEnabledSurgical(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "id": "guard",
  "enabled": true,
  "custom_note": "keep me",
  "trigger": { "event": "PreToolUse", "matcher": "bash" },
  "command": "true"
}`
	writeSpec(t, dir, "guard", body)
	st := NewStore(dir)

	if err := st.SetEnabled("guard", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "guard", "hook.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, `"enabled": false`) {
		t.Fatalf("enabled not flipped:\n%s", s)
	}
	if !strings.Contains(s, `"custom_note": "keep me"`) {
		t.Fatalf("unknown field dropped:\n%s", s)
	}
	if strings.Index(s, `"id"`) >= strings.Index(s, `"enabled"`) ||
		strings.Index(s, `"enabled"`) >= strings.Index(s, `"custom_note"`) {
		t.Fatalf("field order not preserved:\n%s", s)
	}
}

func TestStoreSetEnabledMissing(t *testing.T) {
	if err := NewStore(t.TempDir()).SetEnabled("nope", false); err == nil {
		t.Fatal("want error setting enabled on a missing hook.json")
	}
	if err := NewStore("").SetEnabled("x", false); err == nil {
		t.Fatal("want error with no spec dir")
	}
}

// TestRegistrySetEnabled verifies SetEnabled flips the spec on disk and reloads
// the index so the hook stops/starts matching its event.
func TestRegistrySetEnabled(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "guard", `{"id":"guard","enabled":true,"trigger":{"event":"PreToolUse"},"command":"true"}`)
	r := NewRegistry(NewStore(dir))

	if !r.Has("PreToolUse") {
		t.Fatal("precondition: enabled hook should match its event")
	}
	if err := r.SetEnabled("guard", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if r.Has("PreToolUse") {
		t.Fatal("disabled hook must no longer match its event")
	}
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].Enabled {
		t.Fatalf("snapshot after disable = %+v", snap)
	}

	if err := r.SetEnabled("guard", true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if !r.Has("PreToolUse") {
		t.Fatal("re-enabled hook must match its event again")
	}
}

func TestRegistrySetEnabledUnknown(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(NewStore(dir))
	if err := r.SetEnabled("ghost", false); err == nil {
		t.Fatal("want error toggling an unknown hook id")
	}
}
