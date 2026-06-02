package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileBackendRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	b := &fileBackend{path: path}

	if _, ok, err := b.Get("missing"); ok || err != nil {
		t.Errorf("Get missing: ok=%v err=%v", ok, err)
	}

	if err := b.Set("anthropic-oauth", `{"access":"a"}`); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok, err := b.Get("anthropic-oauth")
	if err != nil || !ok || v != `{"access":"a"}` {
		t.Errorf("Get: v=%q ok=%v err=%v", v, ok, err)
	}

	if err := b.Delete("anthropic-oauth"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := b.Get("anthropic-oauth"); ok {
		t.Errorf("still present after delete")
	}
	// Deleting a missing key is a no-op, not an error.
	if err := b.Delete("anthropic-oauth"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func TestFileBackendPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	b := &fileBackend{path: path}
	if err := b.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

func TestFileBackendPersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := (&fileBackend{path: path}).Set("k", "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// A fresh backend over the same path must see the persisted value.
	v, ok, err := (&fileBackend{path: path}).Get("k")
	if err != nil || !ok || v != "v1" {
		t.Errorf("reopened Get: v=%q ok=%v err=%v", v, ok, err)
	}
}

func TestStorageOverFileBackend(t *testing.T) {
	setNow(t, 1000)
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewStorage(&fileBackend{path: path})

	creds := Credentials{Access: "tok", Refresh: "r", Expires: 5000, Extra: map[string]any{"accountId": "x"}}
	if err := s.Set("anthropic", creds); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// AccessToken resolves through the real anthropic provider (registered at init).
	tok, expired, ok := s.AccessToken("anthropic")
	if !ok || expired || tok != "tok" {
		t.Errorf("AccessToken: tok=%q expired=%v ok=%v", tok, expired, ok)
	}

	got, ok, err := s.Get("anthropic")
	if err != nil || !ok || got.StringExtra("accountId") != "x" {
		t.Errorf("Get: %+v ok=%v err=%v", got, ok, err)
	}
}
