package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestNoDefaultReferencesLegacySessionIDVar guards the sessions->threads rename:
// the workflow template variable is now $(thread.id), so no shipped default
// (workflow.json, skills, prompts) may reference the legacy $(session.id) token,
// which would silently fail to resolve for users who never authored it.
func TestNoDefaultReferencesLegacySessionIDVar(t *testing.T) {
	err := fs.WalkDir(defaultFiles, "defaults", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := defaultFiles.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), "$(session.id)") {
			t.Errorf("%s references the renamed workflow variable $(session.id); use $(thread.id)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded defaults: %v", err)
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func embeddedDefault(t *testing.T, rel string) string {
	t.Helper()
	data, err := defaultFiles.ReadFile("defaults/" + rel)
	if err != nil {
		t.Fatalf("embedded default %s: %v", rel, err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ── first run ──

func TestBootstrap_FirstRunSeedsEverythingAndStampsMarker(t *testing.T) {
	dir := t.TempDir()

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	for _, rel := range []string{
		"settings.json",
		"config/workflow.json",
		"config/languages.json",
		"agents/general.md",
		"prompts/goal/pursue.md",
		"prompts/summarization.md",
	} {
		if !exists(filepath.Join(dir, rel)) {
			t.Errorf("first run should seed %s", rel)
		}
	}
	if got := defaultsVersion(dir); got != "v0.4.3" {
		t.Errorf("defaults version = %q, want v0.4.3", got)
	}
	if exists(filepath.Join(dir, "settings.json.bak")) {
		t.Error("first run must not create .bak files")
	}
}

// ── version change ──

func TestBootstrap_VersionChangeOverwritesManagedFilesWithBak(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "v0.4.2"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Customize the managed files, a managed prompt, and a managed agent.
	customSettings := `{"version":1,"custom":"mine"}`
	customWorkflow := `{"workflows":[]}`
	customPrompt := "my custom pursue prompt"
	customAgent := "---\nname: general\n---\nagent body"
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(customSettings), 0o644)
	os.WriteFile(filepath.Join(dir, "config", "workflow.json"), []byte(customWorkflow), 0o644)
	os.WriteFile(filepath.Join(dir, "prompts", "goal", "pursue.md"), []byte(customPrompt), 0o644)
	os.WriteFile(filepath.Join(dir, "agents", "general.md"), []byte(customAgent), 0o644)

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("upgrade bootstrap: %v", err)
	}

	// Fully-managed files (workflow, prompts, agents) are replaced with the
	// defaults, old content preserved in .bak.
	cases := []struct {
		rel    string
		custom string
	}{
		{"config/workflow.json", customWorkflow},
		{"prompts/goal/pursue.md", customPrompt},
		{"agents/general.md", customAgent},
	}
	for _, tc := range cases {
		p := filepath.Join(dir, filepath.FromSlash(tc.rel))
		if got, want := readFileT(t, p), embeddedDefault(t, tc.rel); got != want {
			t.Errorf("%s should be reset to the embedded default after a version change", tc.rel)
		}
		if got := readFileT(t, p+".bak"); got != tc.custom {
			t.Errorf("%s.bak should hold the replaced content, got %q", tc.rel, got)
		}
	}

	// settings.json is user-editable: the refresh MERGES the default under the
	// user's file, so the user's custom key survives and the default keys are
	// added. The original is preserved as .bak.
	var merged map[string]any
	if err := json.Unmarshal([]byte(readFileT(t, filepath.Join(dir, "settings.json"))), &merged); err != nil {
		t.Fatalf("settings.json is not valid JSON after refresh: %v", err)
	}
	if merged["custom"] != "mine" {
		t.Errorf("user key 'custom' must survive the refresh, got %v", merged["custom"])
	}
	var def map[string]any
	json.Unmarshal([]byte(embeddedDefault(t, "settings.json")), &def)
	for k := range def {
		if _, ok := merged[k]; !ok {
			t.Errorf("default key %q missing after merge", k)
		}
	}
	if got := readFileT(t, filepath.Join(dir, "settings.json.bak")); got != customSettings {
		t.Errorf("settings.json.bak should hold the replaced content, got %q", got)
	}

	if got := defaultsVersion(dir); got != "v0.4.3" {
		t.Errorf("defaults version = %q, want v0.4.3", got)
	}
}

// stampDefaultsVersion must not clobber the other state.json fields (the
// user's chosen model, update-check bookkeeping).
func TestStampDefaultsVersionPreservesState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := WriteState(statePath, State{Model: "openai/gpt-5.1", LastUpdateCheck: "2026-06-10"}); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	stampDefaultsVersion(dir, "v0.4.6")

	st := ReadState(statePath)
	if st.DefaultsVersion != "v0.4.6" {
		t.Errorf("DefaultsVersion = %q, want v0.4.6", st.DefaultsVersion)
	}
	if st.Model != "openai/gpt-5.1" {
		t.Errorf("Model = %q, want openai/gpt-5.1 (must survive the stamp)", st.Model)
	}
	if st.LastUpdateCheck != "2026-06-10" {
		t.Errorf("LastUpdateCheck = %q, want 2026-06-10", st.LastUpdateCheck)
	}
}

func TestBootstrap_MissingMarkerOnExistingInstallTriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	// Simulate a pre-marker install: settings.json exists, no .version file.
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"version":1}`), 0o644)

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// The merge adds the default keys to the minimal seed. Compare semantically
	// since the merged output is canonically (alphabetically) ordered.
	var got, want map[string]any
	json.Unmarshal([]byte(readFileT(t, filepath.Join(dir, "settings.json"))), &got)
	json.Unmarshal([]byte(embeddedDefault(t, "settings.json")), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("existing install without marker should receive the default keys\n got=%v\nwant=%v", got, want)
	}
	if got := readFileT(t, filepath.Join(dir, "settings.json.bak")); got != `{"version":1}` {
		t.Errorf("old settings should be preserved as .bak, got %q", got)
	}
	if !exists(filepath.Join(dir, "config", "workflow.json")) {
		t.Error("refresh should seed config/workflow.json on installs that lack it")
	}
	if !exists(filepath.Join(dir, "prompts", "goal", "pursue.md")) {
		t.Error("refresh should seed missing managed prompts")
	}
}

// ── same version ──

// TestBootstrap_VersionChangePreservesUserSettingsKeys guards the durability of
// user-owned settings.json keys (mcp_servers, tools, deny_list, etc.) across a
// vix upgrade: the managed-defaults refresh must merge, not clobber.
func TestBootstrap_VersionChangePreservesUserSettingsKeys(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "v0.4.2"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	userSettings := `{
      "version": 1,
      "mcp_servers": [{"name": "postgres", "type": "stdio", "command": "npx"}],
      "tools": {"grep": {"backend": "rg"}},
      "features": {"telemetry": false}
    }`
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(userSettings), 0o644)

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(readFileT(t, filepath.Join(dir, "settings.json"))), &got); err != nil {
		t.Fatalf("settings.json invalid after upgrade: %v", err)
	}
	if _, ok := got["mcp_servers"]; !ok {
		t.Error("mcp_servers must survive a vix upgrade")
	}
	if _, ok := got["tools"]; !ok {
		t.Error("tools config must survive a vix upgrade")
	}
	// A user-overridden managed key wins over the default.
	feats, _ := got["features"].(map[string]any)
	if feats == nil || feats["telemetry"] != false {
		t.Errorf("user's features.telemetry=false must be preserved, got %v", got["features"])
	}
}

func TestBootstrap_SameVersionTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	custom := `{"version":1,"custom":"mine"}`
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(custom), 0o644)

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("rerun: %v", err)
	}

	if got := readFileT(t, filepath.Join(dir, "settings.json")); got != custom {
		t.Error("same-version bootstrap must not overwrite user customizations")
	}
	if exists(filepath.Join(dir, "settings.json.bak")) {
		t.Error("same-version bootstrap must not create .bak files")
	}
}

func TestBootstrap_SameVersionReseedsDeletedConfigFiles(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	os.Remove(filepath.Join(dir, "config", "workflow.json"))

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !exists(filepath.Join(dir, "config", "workflow.json")) {
		t.Error("deleted config/workflow.json should be re-seeded on the same version")
	}
}

// ── identical content / dev builds ──

func TestBootstrap_VersionChangeSkipsIdenticalFilesWithoutBak(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "v0.4.2"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// No customizations: contents already equal the defaults.
	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if exists(filepath.Join(dir, "settings.json.bak")) {
		t.Error("identical content must not produce a .bak")
	}
	if got := defaultsVersion(dir); got != "v0.4.3" {
		t.Errorf("defaults version = %q, want v0.4.3", got)
	}
}

func TestBootstrap_DevToDevDoesNotRefresh(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "dev"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	custom := `{"version":1,"custom":"mine"}`
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(custom), 0o644)

	if err := BootstrapHomeVixDir(dir, "dev"); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if got := readFileT(t, filepath.Join(dir, "settings.json")); got != custom {
		t.Error("dev -> dev restart must not refresh defaults")
	}
}

func TestBootstrap_BakIsReplacedOnNextVersionChange(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "v1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("custom-v1-era"), 0o644)
	if err := BootstrapHomeVixDir(dir, "v2"); err != nil {
		t.Fatalf("v2: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("custom-v2-era"), 0o644)
	if err := BootstrapHomeVixDir(dir, "v3"); err != nil {
		t.Fatalf("v3: %v", err)
	}

	if got := readFileT(t, filepath.Join(dir, "settings.json.bak")); got != "custom-v2-era" {
		t.Errorf(".bak should hold the most recently replaced content, got %q", got)
	}
}

// ── bundled vix-help skill ──

// The vix-help skill (SKILL.md + its bundled offline manual) must be seeded on
// first run and refreshed like any other managed default on a version change,
// so existing installs pick it up on upgrade.
func TestBootstrap_VixHelpSkillSeededAndManaged(t *testing.T) {
	dir := t.TempDir()
	if err := BootstrapHomeVixDir(dir, "v1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const skill = "skills/vix-help/SKILL.md"
	const ref = "skills/vix-help/references/vix-manual.md"

	for _, rel := range []string{skill, ref} {
		if !exists(filepath.Join(dir, filepath.FromSlash(rel))) {
			t.Fatalf("first run should seed %s", rel)
		}
	}

	// The seeded SKILL.md must carry valid frontmatter naming the skill.
	if got := readFileT(t, filepath.Join(dir, filepath.FromSlash(skill))); !strings.HasPrefix(got, "---\nname: vix-help") {
		t.Errorf("vix-help SKILL.md should start with frontmatter naming the skill, got prefix %q", got[:min(40, len(got))])
	}

	// A user edit is refreshed back to the embedded default on version change,
	// preserving the old copy as .bak.
	skillPath := filepath.Join(dir, filepath.FromSlash(skill))
	os.WriteFile(skillPath, []byte("my local tweak"), 0o644)

	if err := BootstrapHomeVixDir(dir, "v2"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if got, want := readFileT(t, skillPath), embeddedDefault(t, skill); got != want {
		t.Error("vix-help SKILL.md should be refreshed to the embedded default on version change")
	}
	if got := readFileT(t, skillPath+".bak"); got != "my local tweak" {
		t.Errorf("replaced SKILL.md should be backed up as .bak, got %q", got)
	}
}
