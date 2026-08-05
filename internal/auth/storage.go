package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// keyringService is the OS-keychain service name. It matches the value used by
// internal/config so vix's secrets live under one service.
const keyringService = "vix"

// keyringProbeUser is a sentinel key written and immediately deleted to probe
// whether the OS keychain is reachable (see keyringAvailable).
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
// backend is the OS keychain; the opt-in plaintext fallback is fileBackend;
// tests use an in-memory backend.
type Backend interface {
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

// fileBackend persists OAuth credentials in a plaintext auth.json (a flat
// {key: value} JSON map, written 0600 via temp+rename) when the OS keychain is
// unusable. It shares the same home-global auth.json that internal/config uses
// for API keys; OAuth keys ("<provider>-oauth") never collide with API-key
// entries ("<provider>-api-key").
//
// SECURITY: OAuth refresh tokens are stored in cleartext here. This backend is
// selected only on machines without a usable keychain; the UI and logs surface
// that tokens are unencrypted on disk.
type fileBackend struct {
	path string
	mu   sync.Mutex
}

func (f *fileBackend) load() (map[string]string, error) {
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

func (f *fileBackend) save(m map[string]string) error {
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

func (f *fileBackend) Get(key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[key]
	return v, ok, nil
}

func (f *fileBackend) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(m)
}

func (f *fileBackend) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return f.save(m)
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

// Storage manages OAuth credentials with automatic refresh-on-expiry, backed by
// the OS keychain, or a plaintext auth.json when the keychain is unusable.
type Storage struct {
	backend   Backend
	refreshMu sync.Mutex // serializes refreshes within this process
}

// NewStorage constructs a Storage over the given backend.
func NewStorage(b Backend) *Storage {
	return &Storage{backend: b}
}

var (
	defaultStorageMu sync.Mutex
	defaultStorage   *Storage
)

var (
	authFileMu   sync.Mutex
	authFilePath string
)

// SetAuthFilePath records where OAuth tokens are written when the OS keychain is
// unusable (the home-global auth.json shared with API keys). It is called once
// at process startup (cmd/vix, cmd/vixd) with VixPaths.AuthFile(). Setting it
// invalidates any storage already built so the next DefaultStorage() re-selects
// with the correct path, making backend selection order-independent. When the
// path is left empty, the fallback lands in a system-temp file.
func SetAuthFilePath(path string) {
	authFileMu.Lock()
	authFilePath = path
	authFileMu.Unlock()

	defaultStorageMu.Lock()
	defaultStorage = nil
	defaultStorageMu.Unlock()
}

func authFile() string {
	authFileMu.Lock()
	defer authFileMu.Unlock()
	return authFilePath
}

// DefaultStorage returns the process-wide Storage: backed by the OS keychain
// when reachable, otherwise by a plaintext auth.json (see selectDefaultBackend).
// The backend is built lazily and cached; SetAuthFilePath invalidates the cache.
func DefaultStorage() *Storage {
	defaultStorageMu.Lock()
	defer defaultStorageMu.Unlock()
	if defaultStorage == nil {
		defaultStorage = NewStorage(selectDefaultBackend())
	}
	return defaultStorage
}

// selectDefaultBackend picks the process-wide backend: the OS keychain when
// usable, otherwise a plaintext auth.json fallback so credentials persist on
// keyless machines (headless Linux/WSL/containers) instead of being refused.
// The fallback path comes from SetAuthFilePath, defaulting to a system-temp file
// when unset. This mirrors the API-key store (internal/config/credstore.go).
func selectDefaultBackend() Backend {
	if keyringAvailable() {
		return keyringBackend{}
	}
	path := authFile()
	if path == "" {
		path = filepath.Join(os.TempDir(), "vix-auth.json")
	}
	log.Printf("[auth] OS keychain unusable; OAuth tokens will be stored in plaintext at %s (0600). "+
		"Use an API key env var (e.g. ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN) to avoid on-disk storage.", path)
	return &fileBackend{path: path}
}

// keyringAvailable reports whether the OS keychain can actually store and
// retrieve a secret, via a sentinel write/read/delete round-trip. This is more
// reliable than a read-only probe: the failure seen on keyring-less Linux
// (no D-Bus Secret Service, or `dbus-launch` missing) only surfaces on a real
// Set. The sentinel is deleted immediately.
func keyringAvailable() bool {
	const sentinel = "ok"
	if err := keyring.Set(keyringService, keyringProbeUser, sentinel); err != nil {
		return false
	}
	v, err := keyring.Get(keyringService, keyringProbeUser)
	_ = keyring.Delete(keyringService, keyringProbeUser)
	return err == nil && v == sentinel
}

// KeychainAvailable is the exported form of keyringAvailable, for callers
// (e.g. the UI) that want to warn that a login's token will be stored in
// plaintext on a keyless machine.
func KeychainAvailable() bool { return keyringAvailable() }

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

// Set persists credentials for a provider (OS keychain, or the plaintext
// auth.json fallback on a keyless machine).
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
// credentials (OS keychain, or the plaintext auth.json fallback on a keyless
// machine).
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
