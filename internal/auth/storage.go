package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// keyringService is the OS-keychain service name. It matches the value used by
// internal/config so vix's secrets live under one service.
const keyringService = "vix"

// keyringProbeUser is a never-written key used to non-destructively probe
// whether the OS keychain is reachable.
const keyringProbeUser = "__vix_keyring_probe__"

// ErrNoCredentials is returned when no OAuth login is stored for a provider.
var ErrNoCredentials = errors.New("no OAuth credentials stored")

// oauthKeyringUser returns the keychain "user" field holding a provider's
// OAuth credentials, e.g. "anthropic" -> "anthropic-oauth". This is distinct
// from the "<provider>-api-key" entries managed by internal/config.
func oauthKeyringUser(provider string) string {
	return provider + "-oauth"
}

// Backend abstracts where OAuth credentials are persisted. The production
// backend is the OS keychain; tests use an in-memory backend.
type Backend interface {
	// Get returns the stored value and whether it exists.
	Get(key string) (value string, ok bool, err error)
	Set(key, value string) error
	Delete(key string) error
}

// keyringBackend persists credentials in the OS keychain under keyringService.
type keyringBackend struct{}

func (keyringBackend) Get(key string) (string, bool, error) {
	v, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (keyringBackend) Set(key, value string) error {
	return keyring.Set(keyringService, key, value)
}

func (keyringBackend) Delete(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// MemoryBackend is an in-memory Backend for tests.
type MemoryBackend struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemoryBackend constructs an empty in-memory backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{m: map[string]string{}}
}

func (b *MemoryBackend) Get(key string) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[key]
	return v, ok, nil
}

func (b *MemoryBackend) Set(key, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[key] = value
	return nil
}

func (b *MemoryBackend) Delete(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, key)
	return nil
}

// Storage manages OAuth credentials with automatic refresh-on-expiry. It is
// the Go counterpart of pi's AuthStorage, adapted to vix's keychain storage.
type Storage struct {
	backend   Backend
	refreshMu sync.Mutex // serializes refreshes within this process
}

// NewStorage constructs a Storage over the given backend.
func NewStorage(b Backend) *Storage {
	return &Storage{backend: b}
}

var (
	defaultStorageOnce sync.Once
	defaultStorage     *Storage
)

// DefaultStorage returns the process-wide Storage. It uses the OS keychain when
// reachable, and otherwise falls back to a 0600 JSON file (see fileBackend) so
// OAuth logins also work on headless Linux / WSL sessions without a keychain.
func DefaultStorage() *Storage {
	defaultStorageOnce.Do(func() {
		defaultStorage = NewStorage(newDefaultBackend())
	})
	return defaultStorage
}

// newDefaultBackend picks the keychain when available, else the file fallback.
func newDefaultBackend() Backend {
	if keyringAvailable() {
		lg().Debug("auth storage: using OS keychain", "service", keyringService)
		return keyringBackend{}
	}
	p := defaultAuthFilePath()
	lg().Info("auth storage: OS keychain unavailable, using file fallback", "path", p)
	return &fileBackend{path: p}
}

// keyringAvailable reports whether the OS keychain can be reached. It performs
// a read-only probe: a working keychain returns either a value or ErrNotFound,
// while an unreachable one (no D-Bus Secret Service, etc.) returns a transport
// error.
func keyringAvailable() bool {
	_, err := keyring.Get(keyringService, keyringProbeUser)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

// defaultAuthFilePath returns the fallback credentials file path (~/.vix/auth.json).
func defaultAuthFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "vix-auth.json")
	}
	return filepath.Join(home, ".vix", "auth.json")
}

// DefaultStorageLocation returns a human-readable description of where the
// default storage persists credentials, for display in the login CLI.
func DefaultStorageLocation() string {
	if keyringAvailable() {
		return `OS keychain (service "vix")`
	}
	return defaultAuthFilePath()
}

// Get returns the stored credentials for a provider.
func (s *Storage) Get(provider string) (Credentials, bool, error) {
	raw, ok, err := s.backend.Get(oauthKeyringUser(provider))
	if err != nil || !ok {
		return Credentials{}, false, err
	}
	var creds Credentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return Credentials{}, false, fmt.Errorf("corrupt OAuth credentials for %s: %w", provider, err)
	}
	return creds, true, nil
}

// Set persists credentials for a provider.
func (s *Storage) Set(provider string, creds Credentials) error {
	data, err := json.Marshal(creds)
	if err != nil {
		lg().Error("storage: marshal credentials failed", "provider", provider, "err", err)
		return err
	}
	if err := s.backend.Set(oauthKeyringUser(provider), string(data)); err != nil {
		lg().Error("storage: persist credentials failed", "provider", provider, "err", err)
		return err
	}
	lg().Debug("storage: credentials persisted", "provider", provider, "bytes", len(data))
	return nil
}

// Remove deletes any stored credentials for a provider.
func (s *Storage) Remove(provider string) error {
	if err := s.backend.Delete(oauthKeyringUser(provider)); err != nil {
		lg().Error("storage: delete credentials failed", "provider", provider, "err", err)
		return err
	}
	lg().Debug("storage: credentials removed", "provider", provider)
	return nil
}

// HasLogin reports whether an OAuth login is stored for a provider.
func (s *Storage) HasLogin(provider string) bool {
	_, ok, _ := s.Get(provider)
	return ok
}

// List returns the registered provider ids that currently have stored
// credentials. (The OS keychain cannot be enumerated, so this checks each
// known provider.)
func (s *Storage) List() []string {
	var out []string
	for _, p := range GetProviders() {
		if s.HasLogin(p.ID()) {
			out = append(out, p.ID())
		}
	}
	return out
}

// AccessToken returns the stored access token for a provider without
// refreshing. expired reports whether it is past expiry; ok is false when no
// login is stored.
func (s *Storage) AccessToken(provider string) (token string, expired bool, ok bool) {
	creds, found, err := s.Get(provider)
	if err != nil || !found {
		return "", false, false
	}
	p, known := GetProvider(provider)
	if !known {
		return "", false, false
	}
	return p.APIKey(creds), creds.Expired(), true
}

// AccessTokenRefreshing returns a valid access token for a provider, refreshing
// and persisting the credentials first if they have expired. Refreshes are
// serialized within the process and re-checked after acquiring the lock so a
// concurrent refresh is not duplicated.
func (s *Storage) AccessTokenRefreshing(ctx context.Context, provider string) (string, error) {
	p, ok := GetProvider(provider)
	if !ok {
		return "", fmt.Errorf("unknown OAuth provider: %s", provider)
	}

	creds, found, err := s.Get(provider)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoCredentials
	}
	if !creds.Expired() {
		return p.APIKey(creds), nil
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	// Re-read in case another goroutine refreshed while we waited.
	creds, found, err = s.Get(provider)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoCredentials
	}
	if !creds.Expired() {
		lg().Debug("token refresh: already refreshed by another caller", "provider", provider)
		return p.APIKey(creds), nil
	}

	lg().Info("token refresh: access token expired, refreshing", "provider", provider, "expired_unix_ms", creds.Expires)
	refreshed, err := p.RefreshToken(ctx, creds)
	if err != nil {
		lg().Error("token refresh: failed", "provider", provider, "err", err)
		return "", fmt.Errorf("failed to refresh OAuth token for %s: %w", provider, err)
	}
	if err := s.Set(provider, refreshed); err != nil {
		return "", err
	}
	lg().Info("token refresh: succeeded", "provider", provider, "new_expires_unix_ms", refreshed.Expires)
	return p.APIKey(refreshed), nil
}

// Login runs the provider's interactive login flow and persists the resulting
// credentials.
func (s *Storage) Login(ctx context.Context, providerID string, cb LoginCallbacks) error {
	p, ok := GetProvider(providerID)
	if !ok {
		lg().Error("login: unknown provider", "provider", providerID)
		return fmt.Errorf("unknown OAuth provider: %s", providerID)
	}
	lg().Info("login: starting", "provider", providerID)
	creds, err := p.Login(ctx, cb)
	if err != nil {
		lg().Error("login: flow failed", "provider", providerID, "err", err)
		return err
	}
	if err := s.Set(providerID, creds); err != nil {
		return err
	}
	lg().Info("login: succeeded", "provider", providerID, "access", redact(creds.Access), "refresh", redact(creds.Refresh), "expires_unix_ms", creds.Expires)
	return nil
}

// Logout removes a provider's stored credentials.
func (s *Storage) Logout(provider string) error {
	return s.Remove(provider)
}
