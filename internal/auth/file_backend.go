package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// fileBackend persists credentials in a JSON file with 0600 permissions. It is
// the fallback used when the OS keychain is unavailable — e.g. headless Linux
// or WSL sessions with no D-Bus Secret Service. This mirrors pi's auth.json
// storage. Writes are atomic (temp file + rename) so concurrent readers never
// observe a torn file.
type fileBackend struct {
	path string
	mu   sync.Mutex
}

func (b *fileBackend) load() (map[string]string, error) {
	data, err := os.ReadFile(b.path)
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

func (b *fileBackend) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Tighten perms in case the file pre-existed with a looser mode.
	_ = os.Chmod(b.path, 0o600)
	return nil
}

func (b *fileBackend) Get(key string) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, err := b.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[key]
	return v, ok, nil
}

func (b *fileBackend) Set(key, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, err := b.load()
	if err != nil {
		return err
	}
	m[key] = value
	return b.save(m)
}

func (b *fileBackend) Delete(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, err := b.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return b.save(m)
}
