package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDaemonPathIncludesUserToolDirs(t *testing.T) {
	home := t.TempDir()
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v99.0.0", "bin")
	goBin := filepath.Join(home, "go", "bin")
	cargoBin := filepath.Join(home, ".cargo", "bin")
	localBin := filepath.Join(home, ".local", "bin")
	for _, dir := range []string{nvmBin, goBin, cargoBin, localBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	customBin := filepath.Join(home, "custom", "bin")
	existing := strings.Join([]string{customBin, "/usr/bin"}, string(os.PathListSeparator))

	got := buildDaemonPath(existing, home)
	entries := filepath.SplitList(got)

	if len(entries) == 0 || entries[0] != customBin {
		t.Fatalf("first PATH entry = %q, want %q (full PATH: %q)", entries, customBin, got)
	}
	for _, want := range []string{nvmBin, goBin, cargoBin, localBin, "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		if !containsString(entries, want) {
			t.Fatalf("PATH missing %q (full PATH: %q)", want, got)
		}
	}
	if countString(entries, "/usr/bin") != 1 {
		t.Fatalf("PATH should dedupe /usr/bin; got %d occurrences in %q", countString(entries, "/usr/bin"), got)
	}
}

func TestDaemonEnvUpsertsPATHAndAPIKey(t *testing.T) {
	t.Setenv("PATH", "/old/bin")
	t.Setenv("ANTHROPIC_API_KEY", "old")

	env := daemonEnv("new")

	pathValues := valuesForEnvKey(env, "PATH")
	if len(pathValues) != 1 {
		t.Fatalf("PATH entries = %#v, want exactly one", pathValues)
	}
	if !strings.Contains(pathValues[0], "/old/bin") {
		t.Fatalf("PATH = %q, want it to preserve existing entries", pathValues[0])
	}

	apiKeyValues := valuesForEnvKey(env, "ANTHROPIC_API_KEY")
	if len(apiKeyValues) != 1 || apiKeyValues[0] != "new" {
		t.Fatalf("ANTHROPIC_API_KEY entries = %#v, want [new]", apiKeyValues)
	}
}

func TestDaemonServiceSearchPathDoesNotCaptureCallerPATH(t *testing.T) {
	home := t.TempDir()
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v99.0.0", "bin")
	if err := os.MkdirAll(nvmBin, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", nvmBin, err)
	}
	t.Setenv("PATH", "/tmp/transient-shell-bin")

	got := daemonServiceSearchPath(home)
	entries := filepath.SplitList(got)

	if containsString(entries, "/tmp/transient-shell-bin") {
		t.Fatalf("service PATH captured transient caller PATH: %q", got)
	}
	if !containsString(entries, nvmBin) {
		t.Fatalf("service PATH missing NVM bin %q (full PATH: %q)", nvmBin, got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func valuesForEnvKey(env []string, key string) []string {
	prefix := key + "="
	var values []string
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			values = append(values, strings.TrimPrefix(entry, prefix))
		}
	}
	return values
}
