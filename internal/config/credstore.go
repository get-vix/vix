package config

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrCredNotFound is returned by a CredentialStore.Get/Delete when no secret is
// stored under the given user. It is the backend-agnostic equivalent of
// keyring.ErrNotFound.
var ErrCredNotFound = errors.New("credential not found")

// Credential-store backend names, reported by CredentialStore.Backend and
// surfaced to the UI so it can warn when secrets are kept unencrypted on disk.
const (
	BackendKeyring = "keyring"
	BackendFile    = "file"
)

// CredentialStore persists secrets keyed by a "user" string under the vix
// keyring service. Two backends implement it: the OS keyring (preferred) and a
// plaintext auth.json fallback for environments without a keyring (headless
// Linux, minimal containers). The "user" strings are the same the keyring uses
// (e.g. "<provider>-api-key", "<provider>-auth-default"), so storage and
// resolution stay consistent across backends.
type CredentialStore interface {
	// Get returns the stored secret, or ErrCredNotFound when absent.
	Get(user string) (string, error)
	// Set stores (or overwrites) the secret.
	Set(user, secret string) error
	// Delete removes the secret, returning ErrCredNotFound when absent.
	Delete(user string) error
	// Backend names the active store (BackendKeyring | BackendFile).
	Backend() string
}

// keyringStore is the OS-keyring backend, a thin wrapper translating
// keyring.ErrNotFound into ErrCredNotFound.
type keyringStore struct{}

func (keyringStore) Get(user string) (string, error) {
	v, err := keyring.Get(keyringService, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrCredNotFound
	}
	return v, err
}

func (keyringStore) Set(user, secret string) error {
	return keyring.Set(keyringService, user, secret)
}

func (keyringStore) Delete(user string) error {
	err := keyring.Delete(keyringService, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrCredNotFound
	}
	return err
}

func (keyringStore) Backend() string { return BackendKeyring }

// fileStore is the plaintext auth.json fallback used when no OS keyring is
// available. The file is a flat {user: secret} JSON map, written 0600 via a
// temp+rename so a crash can't leave a half-written file. Credentials are
// user-global, so the file lives next to threads (home/auth.json), never in a
// project's ./.vix.
//
// SECURITY: secrets are stored in cleartext. This backend exists only as a
// fallback when the OS keyring is unreachable; the UI surfaces the backend so
// the user knows. At-rest encryption is a possible future improvement.
type fileStore struct {
	path string
	mu   sync.Mutex
}

func (f *fileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (f *fileStore) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileStore) Get(user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[user]
	if !ok {
		return "", ErrCredNotFound
	}
	return v, nil
}

func (f *fileStore) Set(user, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[user] = secret
	return f.save(m)
}

func (f *fileStore) Delete(user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[user]; !ok {
		return ErrCredNotFound
	}
	delete(m, user)
	return f.save(m)
}

func (f *fileStore) Backend() string { return BackendFile }

// credProbeUser is the sentinel keyring entry used to detect keyring
// availability. It is written and immediately deleted by keyringUsable.
const credProbeUser = "__vix_probe__"

var (
	defaultStoreOnce sync.Once
	defaultStoreInst CredentialStore
)

// defaultStore returns the process-wide credential store, choosing the OS
// keyring when a real round-trip succeeds, else the auth.json fallback. The
// decision is made once per process (keyring availability does not change
// mid-run). Credentials are user-global, so the fallback path is derived from
// HomeVixDir, matching the keyring's global scope.
func defaultStore() CredentialStore {
	defaultStoreOnce.Do(func() {
		authFile := NewVixPaths("", HomeVixDir(), "").AuthFile()
		defaultStoreInst = selectStore(keyringUsable(), authFile)
	})
	return defaultStoreInst
}

// CredentialBackend reports the active credential backend (BackendKeyring |
// BackendFile) so callers (the UI) can warn when secrets are kept in cleartext.
func CredentialBackend() string { return defaultStore().Backend() }

// DefaultCredentialStore returns the process-wide credential store (OS keyring,
// or the auth.json fallback). Exposed so other subsystems (e.g. MCP OAuth token
// storage) persist secrets through the same backend as provider credentials.
func DefaultCredentialStore() CredentialStore { return defaultStore() }

// selectStore picks the backend: the keyring when usable, otherwise a plaintext
// auth.json fallback at authFile (or a system-temp path when authFile is empty,
// e.g. when the home dir is unavailable). Pure so it can be unit-tested without
// touching a real keyring.
func selectStore(keyringOK bool, authFile string) CredentialStore {
	if keyringOK {
		return keyringStore{}
	}
	if authFile == "" {
		authFile = filepath.Join(os.TempDir(), "vix-auth.json")
	}
	log.Printf("[config] OS keyring unavailable; storing credentials in plaintext at %s (0600). "+
		"Set provider env vars (e.g. ANTHROPIC_API_KEY) to avoid on-disk storage.", authFile)
	return &fileStore{path: authFile}
}

// keyringUsable reports whether the OS keyring can actually store and retrieve a
// secret, via a sentinel round-trip. This is more reliable than probing for a
// D-Bus thread bus: the failure observed on keyring-less Linux containers is
// keyring.Set returning `exec: "dbus-launch": executable file not found`, which
// only a real Set surfaces.
func keyringUsable() bool {
	const sentinel = "ok"
	if err := keyring.Set(keyringService, credProbeUser, sentinel); err != nil {
		return false
	}
	v, err := keyring.Get(keyringService, credProbeUser)
	_ = keyring.Delete(keyringService, credProbeUser)
	return err == nil && v == sentinel
}
