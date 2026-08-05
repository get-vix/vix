package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectStore_KeyringWhenUsable(t *testing.T) {
	if got := selectStore(true, filepath.Join(t.TempDir(), "auth.json")); got.Backend() != BackendKeyring {
		t.Fatalf("Backend() = %q, want %q", got.Backend(), BackendKeyring)
	}
}

func TestSelectStore_FileWhenKeyringUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	st := selectStore(false, path)
	if st.Backend() != BackendFile {
		t.Fatalf("Backend() = %q, want %q", st.Backend(), BackendFile)
	}
	fs, ok := st.(*fileStore)
	if !ok || fs.path != path {
		t.Fatalf("expected *fileStore at %q, got %T %+v", path, st, st)
	}
}

func TestFileStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	st := &fileStore{path: path}

	// Missing key.
	if _, err := st.Get("mimo-api-key"); err != ErrCredNotFound {
		t.Fatalf("Get(missing) err = %v, want ErrCredNotFound", err)
	}
	if err := st.Delete("mimo-api-key"); err != ErrCredNotFound {
		t.Fatalf("Delete(missing) err = %v, want ErrCredNotFound", err)
	}

	// Set + Get.
	if err := st.Set("mimo-api-key", "tp-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := st.Get("mimo-api-key"); err != nil || v != "tp-secret" {
		t.Fatalf("Get = (%q, %v), want (tp-secret, nil)", v, err)
	}

	// Overwrite + second key (persistence across instances).
	if err := st.Set("mimo-api-key", "tp-secret2"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	if err := st.Set("openai-api-key", "sk-xyz"); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	st2 := &fileStore{path: path}
	if v, _ := st2.Get("mimo-api-key"); v != "tp-secret2" {
		t.Errorf("reopened Get(mimo) = %q, want tp-secret2", v)
	}
	if v, _ := st2.Get("openai-api-key"); v != "sk-xyz" {
		t.Errorf("reopened Get(openai) = %q, want sk-xyz", v)
	}

	// Delete.
	if err := st.Delete("mimo-api-key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get("mimo-api-key"); err != ErrCredNotFound {
		t.Errorf("Get after delete err = %v, want ErrCredNotFound", err)
	}
}

func TestFileStore_Perms0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	st := &fileStore{path: path}
	if err := st.Set("anthropic-api-key", "sk-ant"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth.json perms = %o, want 600", perm)
	}
	// No leftover temp file.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}
