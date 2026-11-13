// Package secretstore is Vix's opt-in bridge to the configured daz-secrets
// provider (a daz-secrets provider process selected by
// ~/.config/daz-secrets/provider.toml). It is never probed or used
// automatically: the OS keyring and auth.json fallback remain the default
// credential stores, and this backend is active only while the user opts in
// via the daz_secrets feature flag.
//
// When the flag is on, every operation fails closed: if the provider is not
// configured or unreachable, reads, writes, and deletes return an error
// instead of silently falling back to another store, so credentials never
// land anywhere the user did not choose. Provider-facing errors are redacted
// by the daz-secrets client; this package adds only remediation context.
package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/darrenoakey/daz-secrets/pkg/dazsecrets"
)

// Service is the stable daz-secrets namespace for all Vix credentials. It
// matches the keyring service name used by internal/config and internal/auth,
// so the stored "user"/account keys stay consistent across backends.
const Service = "vix"

// FeatureFlag is the settings.json "features" key that opts Vix into the
// daz-secrets credential backend.
const FeatureFlag = "daz_secrets"

// ErrNotConfigured reports that the daz-secrets backend is enabled but the
// configured provider is not usable. Use errors.Is to detect it; the wrapped
// cause is the client's (redacted) initialization error.
var ErrNotConfigured = errors.New("daz-secrets provider unavailable")

// Store is a fail-closed bridge to one daz-secrets provider. It keeps either
// a validated client or the redacted error from loading the configuration, so
// an unusable provider surfaces on every operation without re-reading the
// configuration file or prompting the operating system.
type Store struct {
	client  *dazsecrets.Client
	initErr error
}

// New loads the user's configured daz-secrets provider. The returned Store is
// safe to use even when the provider is not configured: every operation then
// fails closed with an error wrapping ErrNotConfigured.
func New() *Store {
	client, err := dazsecrets.NewDefaultClient()
	return &Store{client: client, initErr: err}
}

// NewWithConfig builds a Store for an explicit provider configuration. This
// exists so tests can drive a real provider without touching the user's home
// directory; production code should use New.
func NewWithConfig(config dazsecrets.ClientConfig) *Store {
	client, err := dazsecrets.NewClient(config)
	return &Store{client: client, initErr: err}
}

// unavailable wraps the initialization error with remediation context.
func (s *Store) unavailable() error {
	return fmt.Errorf("%w: %v (features.%s is enabled; unset it or fix ~/.config/daz-secrets/provider.toml)",
		ErrNotConfigured, s.initErr, FeatureFlag)
}

// Available reports whether the configured provider answers its protocol
// handshake. Errors are deliberately redacted by daz-secrets.
func (s *Store) Available() bool {
	if s.initErr != nil {
		return false
	}
	_, err := s.client.Info(context.Background())
	return err == nil
}

// Get returns the UTF-8 credential stored under account and whether it
// exists. A missing credential is ("", false, nil).
func (s *Store) Get(account string) (string, bool, error) {
	if s.initErr != nil {
		return "", false, s.unavailable()
	}
	secret, err := s.client.Get(context.Background(), Service, account)
	if dazsecrets.IsCode(err, dazsecrets.CodeNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(secret.Value), true, nil
}

// Set unconditionally stores (or overwrites) a UTF-8 credential.
func (s *Store) Set(account, value string) error {
	if s.initErr != nil {
		return s.unavailable()
	}
	_, err := s.client.Set(context.Background(), Service, account, []byte(value), nil)
	return err
}

// Delete removes a credential and reports whether it existed.
func (s *Store) Delete(account string) (bool, error) {
	if s.initErr != nil {
		return false, s.unavailable()
	}
	deleted, err := s.client.Delete(context.Background(), Service, account, nil)
	if dazsecrets.IsCode(err, dazsecrets.CodeNotFound) {
		return false, nil
	}
	return deleted.Deleted, err
}

// Enabled reports whether the user opted into the daz-secrets backend via
// features.daz_secrets in ~/.vix/settings.json. A missing file, missing flag,
// or unparsable JSON all mean false: the backend is strictly opt-in and never
// activates by default. This mirrors the feature() convention in
// internal/config; the small path/JSON read is duplicated here because
// internal/auth needs the same flag and cannot import internal/config
// (internal/config already imports internal/auth).
func Enabled() bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".vix", "settings.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Features map[string]bool `json:"features"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return false
	}
	return cfg.Features[FeatureFlag]
}
