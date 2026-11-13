package auth

import (
	"log"

	"github.com/get-vix/vix/internal/secretstore"
)

// dazSecretsBackend is the opt-in daz-secrets OAuth-credential backend. It is
// selected by selectDefaultBackend only when the user sets
// features.daz_secrets in ~/.vix/settings.json; the OS keychain and plaintext
// auth.json selection are unchanged otherwise. Operations fail closed when
// the provider is unusable, so tokens are never silently redirected to a
// store the user did not choose.
type dazSecretsBackend struct {
	store *secretstore.Store
}

func (b dazSecretsBackend) Get(key string) (string, bool, error) {
	return b.store.Get(key)
}

func (b dazSecretsBackend) Set(key, value string) error {
	return b.store.Set(key, value)
}

func (b dazSecretsBackend) Delete(key string) error {
	_, err := b.store.Delete(key)
	return err
}

// selectDazSecretsBackend returns the daz-secrets backend when the feature
// flag is on, reporting whether it applied.
func selectDazSecretsBackend() (Backend, bool) {
	if !secretstore.Enabled() {
		return nil, false
	}
	log.Printf("[auth] OAuth credential store: daz-secrets (features.%s)", secretstore.FeatureFlag)
	return dazSecretsBackend{store: secretstore.New()}, true
}
