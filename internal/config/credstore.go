package config

import (
	"errors"

	"github.com/get-vix/vix/internal/secretstore"
)

// ErrCredNotFound is returned when no secret is stored under the requested
// account.
var ErrCredNotFound = errors.New("credential not found")

// BackendProvider is the only credential backend Vix uses.
const BackendProvider = "daz-secrets"

// CredentialStore persists credentials in the machine-configured daz-secrets
// provider. Implementations must fail closed; plaintext and environment
// fallbacks are intentionally unsupported.
type CredentialStore interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
	Backend() string
}

type providerStore struct{ store *secretstore.Store }

func (p providerStore) Get(account string) (string, error) {
	value, ok, err := p.store.Get(account)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrCredNotFound
	}
	return value, nil
}

func (p providerStore) Set(account, secret string) error {
	return p.store.Set(account, secret)
}

func (p providerStore) Delete(account string) error {
	deleted, err := p.store.Delete(account)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrCredNotFound
	}
	return nil
}

func (providerStore) Backend() string { return BackendProvider }

var defaultStoreInst CredentialStore = providerStore{store: secretstore.Default()}

// CredentialBackend reports the active noninteractive provider backend.
func CredentialBackend() string { return defaultStoreInst.Backend() }

// DefaultCredentialStore returns the process-wide provider-backed store.
func DefaultCredentialStore() CredentialStore { return defaultStoreInst }

func defaultStore() CredentialStore { return defaultStoreInst }

// UseCredentialStoreForTesting replaces the process store and returns a restore
// function. It is scoped to internal integration tests; production startup
// never calls it.
func UseCredentialStoreForTesting(store CredentialStore) func() {
	previous := defaultStoreInst
	defaultStoreInst = store
	return func() { defaultStoreInst = previous }
}
