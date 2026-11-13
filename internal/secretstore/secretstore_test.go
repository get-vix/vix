package secretstore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/darrenoakey/daz-secrets/pkg/dazsecrets"
)

// TestEnabledIsOptInOnly proves the backend never activates by default: no
// settings file, an absent flag, an explicit false, or an unparsable file all
// leave the backend off.
func TestEnabledIsOptInOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	vixDir := filepath.Join(home, ".vix")
	if err := os.MkdirAll(vixDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		settings string
		want     bool
	}{
		{"missing file", "", false},
		{"absent flag", `{"theme":"dark"}`, false},
		{"explicit false", `{"features":{"daz_secrets":false}}`, false},
		{"opted in", `{"features":{"daz_secrets":true}}`, true},
		{"unparsable", `{"features":`, false},
		{"flag not a bool", `{"features":{"daz_secrets":"yes"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.settings != "" {
				path := filepath.Join(vixDir, "settings.json")
				if err := os.WriteFile(path, []byte(tc.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUnconfiguredFailsClosed proves an enabled-but-unusable provider never
// falls back to another store and never panics: every operation reports an
// error wrapping ErrNotConfigured with remediation context.
func TestUnconfiguredFailsClosed(t *testing.T) {
	s := &Store{initErr: errors.New("redacted: provider.toml unreadable")}

	if s.Available() {
		t.Fatal("Available() = true for an unusable provider")
	}
	if v, found, err := s.Get("anthropic-api-key"); err == nil || found || v != "" {
		t.Fatalf("Get = (%q, %v, %v), want fail-closed error", v, found, err)
	} else if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Get error does not wrap ErrNotConfigured: %v", err)
	}
	if err := s.Set("anthropic-api-key", "secret"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Set error does not wrap ErrNotConfigured: %v", err)
	}
	if _, err := s.Delete("anthropic-api-key"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Delete error does not wrap ErrNotConfigured: %v", err)
	}
}

// TestMissingProviderPathFailsClosed configures a nonexistent provider
// executable: construction fails, and the resulting Store fails closed rather
// than degrading to any other store.
func TestMissingProviderPathFailsClosed(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // daz-secrets rejects symlinked path components
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithConfig(dazsecrets.ClientConfig{
		ProviderPath: filepath.Join(dir, "missing-provider"),
		ProviderID:   "provider",
		Timeout:      2 * time.Second,
	})
	if s.initErr == nil {
		t.Fatal("client construction succeeded for a nonexistent provider executable")
	}
	if s.Available() {
		t.Fatal("Available() = true for a nonexistent provider executable")
	}
	if v, found, err := s.Get("anthropic-api-key"); err == nil || found {
		t.Fatalf("Get = (%q, %v, %v), want a fail-closed error", v, found, err)
	}
	if err := s.Set("anthropic-api-key", "secret"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Set error does not wrap ErrNotConfigured: %v", err)
	}
}

// TestRealProviderRoundTrip drives the real daz-secrets conformance provider
// process through the production protocol: set, read back, overwrite, delete,
// and confirm not-found. Skipped only when the provider cannot be built from
// the daz-secrets module (offline host without a warm module cache).
func TestRealProviderRoundTrip(t *testing.T) {
	bin := buildConformanceProvider(t)
	if bin == "" {
		t.Skip("daz-secrets conformance provider could not be built; skipping real-provider test")
	}

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The conformance provider derives its identity and mode from its
	// executable filename (<id>--<mode>), mirroring the upstream tests.
	provider := filepath.Join(dir, "provider--ok")
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider, data, 0o700); err != nil {
		t.Fatal(err)
	}

	s := NewWithConfig(dazsecrets.ClientConfig{
		ProviderPath: provider,
		ProviderID:   "provider",
		Timeout:      5 * time.Second,
	})

	const account = "anthropic-api-key"
	if err := s.Set(account, "sk-test-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, found, err := s.Get(account)
	if err != nil || !found || got != "sk-test-value" {
		t.Fatalf("Get = (%q, %v, %v), want stored value", got, found, err)
	}
	if err := s.Set(account, "sk-replaced"); err != nil {
		t.Fatalf("overwrite Set: %v", err)
	}
	if got, found, err := s.Get(account); err != nil || !found || got != "sk-replaced" {
		t.Fatalf("Get after overwrite = (%q, %v, %v)", got, found, err)
	}
	if deleted, err := s.Delete(account); err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v), want deleted", deleted, err)
	}
	if _, found, err := s.Get(account); found || err != nil {
		t.Fatalf("Get after delete = found=%v err=%v, want not-found", found, err)
	}
	if _, err := s.Delete(account); err != nil {
		t.Fatalf("second Delete should report absent without error: %v", err)
	}
}

// buildConformanceProvider builds the test-only daz-secrets conformance
// provider from the daz-secrets module, returning the binary path or "" when
// the build is not possible on this host.
func buildConformanceProvider(t *testing.T) string {
	t.Helper()
	env := append(os.Environ(), "GOFLAGS=-mod=mod")
	list := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/darrenoakey/daz-secrets")
	list.Env = env
	moduleDir, err := list.Output()
	if err != nil {
		return ""
	}
	bin := filepath.Join(t.TempDir(), "conformance")
	build := exec.Command("go", "build", "-o", bin, filepath.Join(strings.TrimSpace(string(moduleDir)), "cmd", "daz-secrets-conformance-provider"))
	build.Env = env
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Logf("conformance provider build failed: %v: %s", buildErr, output)
		return ""
	}
	return bin
}
