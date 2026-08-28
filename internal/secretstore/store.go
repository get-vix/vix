// Package secretstore is Vix's sole bridge to the configured daz-secrets
// provider. It never probes or calls an operating-system credential store and
// never falls back to environment variables or plaintext files.
package secretstore

import (
	"context"
	"sync"

	"github.com/darrenoakey/daz-secrets/pkg/dazsecrets"
)

// Service is the stable daz-secrets namespace for all Vix credentials.
const Service = "vix"

// Store keeps either a validated client or its redacted initialization error.
// Keeping the error makes every operation fail closed without repeatedly
// reparsing configuration or producing interactive operating-system prompts.
type Store struct {
	client  *dazsecrets.Client
	initErr error
}

// NewDefault loads Vix's configured daz-secrets provider.
func NewDefault() *Store {
	client, err := dazsecrets.NewDefaultClient()
	return &Store{client: client, initErr: err}
}

var (
	defaultOnce  sync.Once
	defaultStore *Store
)

// Default returns the process-wide provider bridge.
func Default() *Store {
	defaultOnce.Do(func() { defaultStore = NewDefault() })
	return defaultStore
}

// Available verifies that the configured provider process answers its protocol
// handshake. Errors are deliberately redacted by daz-secrets.
func (s *Store) Available() bool {
	if s.initErr != nil {
		return false
	}
	_, err := s.client.Info(context.Background())
	return err == nil
}

// Get returns a UTF-8 credential and whether it exists.
func (s *Store) Get(account string) (string, bool, error) {
	if s.initErr != nil {
		return "", false, s.initErr
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

// Set unconditionally stores a UTF-8 credential.
func (s *Store) Set(account, value string) error {
	if s.initErr != nil {
		return s.initErr
	}
	_, err := s.client.Set(context.Background(), Service, account, []byte(value), nil)
	return err
}

// Delete removes a credential and reports whether it existed.
func (s *Store) Delete(account string) (bool, error) {
	if s.initErr != nil {
		return false, s.initErr
	}
	deleted, err := s.client.Delete(context.Background(), Service, account, nil)
	if dazsecrets.IsCode(err, dazsecrets.CodeNotFound) {
		return false, nil
	}
	return deleted.Deleted, err
}
