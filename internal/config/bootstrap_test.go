package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
	} {
		if !exists(filepath.Join(dir, rel)) {
			t.Errorf("first run should seed %s", rel)
		}
	}
	if got := readVersionMarker(dir); got != "v0.4.3" {
		t.Errorf("marker = %q, want v0.4.3", got)
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

	// Customize the managed files and a managed prompt.
	customSettings := `{"version":1,"custom":"mine"}`
	customWorkflow := `{"workflows":[]}`
	customPrompt := "my custom pursue prompt"
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(customSettings), 0o644)
	os.WriteFile(filepath.Join(dir, "config", "workflow.json"), []byte(customWorkflow), 0o644)
	os.WriteFile(filepath.Join(dir, "prompts", "goal", "pursue.md"), []byte(customPrompt), 0o644)
	// Customize an agent file — must NOT be touched (model persistence).
	customAgent := "---\nmodel: openai/gpt-5.1\n---\nagent body"
	os.WriteFile(filepath.Join(dir, "agents", "general.md"), []byte(customAgent), 0o644)

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("upgrade bootstrap: %v", err)
	}

	// Managed files replaced with defaults, old content in .bak.
	cases := []struct {
		rel    string
		custom string
	}{
		{"settings.json", customSettings},
		{"config/workflow.json", customWorkflow},
		{"prompts/goal/pursue.md", customPrompt},
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

	// Agents untouched.
	if got := readFileT(t, filepath.Join(dir, "agents", "general.md")); got != customAgent {
		t.Error("agents/*.md must never be overwritten by a version refresh")
	}
	if got := readVersionMarker(dir); got != "v0.4.3" {
		t.Errorf("marker = %q, want v0.4.3", got)
	}
}

func TestBootstrap_MissingMarkerOnExistingInstallTriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	// Simulate a pre-marker install: settings.json exists, no .version file.
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"version":1}`), 0o644)

	if err := BootstrapHomeVixDir(dir, "v0.4.3"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if got, want := readFileT(t, filepath.Join(dir, "settings.json")), embeddedDefault(t, "settings.json"); got != want {
		t.Error("existing install without marker should receive refreshed defaults")
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
	if got := readVersionMarker(dir); got != "v0.4.3" {
		t.Errorf("marker = %q, want v0.4.3", got)
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
