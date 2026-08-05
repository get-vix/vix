package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSpec(t *testing.T, dir, id, body string) {
	t.Helper()
	hookDir := filepath.Join(dir, id)
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "hook.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLoadSpecs(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "ok", `{"id":"ok","enabled":true,"trigger":{"event":"PreToolUse"},"command":"true"}`)
	writeSpec(t, dir, "noid", `{"enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)
	writeSpec(t, dir, "bad", `{not json`)
	writeSpec(t, dir, "invalid", `{"id":"invalid","trigger":{"event":"Nope"},"command":"true"}`)
	// A subdir without a hook.json and a top-level file are both ignored.
	os.MkdirAll(filepath.Join(dir, "not-a-hook"), 0o755)
	os.WriteFile(filepath.Join(dir, "not-a-hook", "script.sh"), []byte("echo hi"), 0o644)
	os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("not a spec"), 0o644)

	st := NewStore(dir)
	specs, invalid := st.LoadSpecs()

	ids := map[string]bool{}
	for _, s := range specs {
		ids[s.ID] = true
	}
	if !ids["ok"] || !ids["noid"] {
		t.Fatalf("expected ok+noid (id from directory name), got %v", ids)
	}
	if len(specs) != 2 {
		t.Fatalf("expected 2 valid specs, got %d", len(specs))
	}
	if _, ok := invalid["bad"]; !ok {
		t.Errorf("expected bad hook reported invalid: %v", invalid)
	}
	if _, ok := invalid["invalid"]; !ok {
		t.Errorf("expected invalid hook reported invalid: %v", invalid)
	}
}

func TestStoreEmptyDir(t *testing.T) {
	specs, invalid := NewStore("").LoadSpecs()
	if len(specs) != 0 || len(invalid) != 0 {
		t.Fatalf("empty path should load nothing")
	}
}

func TestRegistryMatchAndReload(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "sync", `{"id":"sync","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"true"}`)
	writeSpec(t, dir, "async", `{"id":"async","enabled":true,"mode":"async","trigger":{"event":"PreToolUse"},"command":"true"}`)
	writeSpec(t, dir, "disabled", `{"id":"disabled","enabled":false,"trigger":{"event":"PreToolUse"},"command":"true"}`)
	writeSpec(t, dir, "stop", `{"id":"stop","enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)

	r := NewRegistry(NewStore(dir))

	if !r.Has(EventPreToolUse) || !r.Has(EventStop) {
		t.Fatal("expected Has for PreToolUse and Stop")
	}
	if r.Has(EventUserPromptSubmit) {
		t.Fatal("did not expect UserPromptSubmit hooks")
	}

	sync, async := r.Match(EventPreToolUse, "write_file")
	if len(sync) != 1 || sync[0].ID != "sync" {
		t.Fatalf("expected sync hook to match write_file, got %v", sync)
	}
	if len(async) != 1 || async[0].ID != "async" {
		t.Fatalf("expected async hook (match-all), got %v", async)
	}

	// read_file: the sync hook's matcher excludes it, the async match-all stays.
	sync, async = r.Match(EventPreToolUse, "read_file")
	if len(sync) != 0 {
		t.Fatalf("sync hook should not match read_file, got %v", sync)
	}
	if len(async) != 1 {
		t.Fatalf("async match-all should still fire, got %v", async)
	}

	// Hot reload: drop a new hook on disk and reload.
	writeSpec(t, dir, "prompt", `{"id":"p","enabled":true,"trigger":{"event":"UserPromptSubmit"},"command":"true"}`)
	r.Reload()
	if !r.Has(EventUserPromptSubmit) {
		t.Fatal("reload should pick up the new UserPromptSubmit hook")
	}
}

func TestRegistrySpecByID(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "on", `{"id":"on","enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)
	writeSpec(t, dir, "off", `{"id":"off","enabled":false,"trigger":{"event":"Stop"},"command":"true"}`)

	r := NewRegistry(NewStore(dir))

	if s, ok := r.SpecByID("on"); !ok || s.ID != "on" {
		t.Fatalf("SpecByID(on) = %+v, %v; want the enabled hook", s, ok)
	}
	// Disabled hooks are still addressable by id (manual trigger overrides).
	if s, ok := r.SpecByID("off"); !ok || s.ID != "off" || s.Enabled {
		t.Fatalf("SpecByID(off) = %+v, %v; want the disabled hook", s, ok)
	}
	if _, ok := r.SpecByID("ghost"); ok {
		t.Fatal("SpecByID(ghost) should miss")
	}
}
