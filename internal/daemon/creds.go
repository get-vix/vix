package daemon

import (
	"encoding/json"
	"fmt"

	"github.com/get-vix/vix/internal/config"
)

// Credential management is daemon-owned: the TUI never touches the keychain or
// the auth.json fallback directly. It issues these request/response RPCs and the
// daemon performs the store/delete/read against config's credential store. This
// keeps a single writer/reader of credentials (the daemon is also the sole
// consumer when it resolves a credential to build the LLM client) and makes the
// fallback-store path authoritative on one side.
//
// Credentials are user-global (the keychain is global; the auth.json fallback
// lives at ~/.vix/auth.json), so these handlers take no cwd/config_dir.

// ProviderCredEntry pairs a provider id with its credential status. Defined in
// the daemon package so the handler (producer) and Client method (consumer)
// share the wire shape without a protocol-package dependency.
type ProviderCredEntry struct {
	Provider string                    `json:"provider"`
	Status   config.ProviderAuthStatus `json:"status"`
}

// credStatusAll returns the credential status of every known provider, in
// registry order.
func credStatusAll() []ProviderCredEntry {
	ids := config.KnownProviders()
	out := make([]ProviderCredEntry, 0, len(ids))
	for _, p := range ids {
		out = append(out, ProviderCredEntry{Provider: p, Status: config.GetProviderAuthStatus(p)})
	}
	return out
}

// credStatusResponse is the common reply shape: ok status, the active backend
// (so the UI can warn about plaintext storage), and the refreshed per-provider
// statuses so a mutating call updates the UI in a single round-trip.
func credStatusResponse() map[string]any {
	return map[string]any{
		"status":    "ok",
		"backend":   config.CredentialBackend(),
		"providers": credStatusAll(),
	}
}

// RegisterCredentialHandlers wires the daemon-owned credential RPCs.
func RegisterCredentialHandlers(s *Server) {
	// creds.status — read all providers' credential status + active backend.
	s.RegisterHandler("creds.status", func(data map[string]any) (map[string]any, error) {
		return credStatusResponse(), nil
	})

	// creds.store — store (or overwrite) the key for a provider's credential
	// method, plus a user-supplied base URL for methods that require one (e.g.
	// the MiMo Token Plan endpoint). Returns the refreshed status.
	s.RegisterHandler("creds.store", func(data map[string]any) (map[string]any, error) {
		provider, _ := data["provider"].(string)
		methodID, _ := data["method_id"].(string)
		key, _ := data["key"].(string)
		baseURL, _ := data["base_url"].(string)
		if provider == "" || methodID == "" || key == "" {
			return map[string]any{"status": "error", "message": "provider, method_id and key are required"}, nil
		}
		if err := config.StoreProviderMethodKey(provider, methodID, key, baseURL); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return credStatusResponse(), nil
	})

	// creds.delete — remove the stored key (and endpoint) for a method.
	s.RegisterHandler("creds.delete", func(data map[string]any) (map[string]any, error) {
		provider, _ := data["provider"].(string)
		methodID, _ := data["method_id"].(string)
		if provider == "" || methodID == "" {
			return map[string]any{"status": "error", "message": "provider and method_id are required"}, nil
		}
		if err := config.DeleteProviderMethodKey(provider, methodID); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return credStatusResponse(), nil
	})

	// creds.set_default — set (method_id non-empty) or clear (empty) the
	// provider's default credential method.
	s.RegisterHandler("creds.set_default", func(data map[string]any) (map[string]any, error) {
		provider, _ := data["provider"].(string)
		methodID, _ := data["method_id"].(string)
		if provider == "" {
			return map[string]any{"status": "error", "message": "provider is required"}, nil
		}
		var err error
		if methodID == "" {
			err = config.ClearProviderAuthDefault(provider)
		} else {
			err = config.SetProviderAuthDefault(provider, methodID)
		}
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return credStatusResponse(), nil
	})
}

// --- Client side (connection-level RPCs) ---

// CredStatus bundles the per-provider credential statuses with the active
// storage backend ("keyring" | "file").
type CredStatus struct {
	Backend   string
	Providers map[string]config.ProviderAuthStatus
}

// parseCredResponse converts a creds.* RPC reply into a CredStatus.
func parseCredResponse(resp map[string]any) (CredStatus, error) {
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		if msg == "" {
			msg = "credential request failed"
		}
		return CredStatus{}, fmt.Errorf("%s", msg)
	}
	backend, _ := resp["backend"].(string)
	raw, err := json.Marshal(resp["providers"])
	if err != nil {
		return CredStatus{}, err
	}
	var entries []ProviderCredEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return CredStatus{}, err
	}
	out := CredStatus{Backend: backend, Providers: make(map[string]config.ProviderAuthStatus, len(entries))}
	for _, e := range entries {
		out.Providers[e.Provider] = e.Status
	}
	return out, nil
}

// ProviderCredStatus reads the credential status of all providers.
func (c *Client) ProviderCredStatus() (CredStatus, error) {
	resp, err := c.sendRequest(map[string]any{"command": "creds.status"})
	if err != nil {
		return CredStatus{}, err
	}
	return parseCredResponse(resp)
}

// StoreProviderMethodKey stores a provider method's key (and optional base URL)
// daemon-side and returns the refreshed status.
func (c *Client) StoreProviderMethodKey(provider, methodID, key, baseURL string) (CredStatus, error) {
	resp, err := c.sendRequest(map[string]any{
		"command":   "creds.store",
		"provider":  provider,
		"method_id": methodID,
		"key":       key,
		"base_url":  baseURL,
	})
	if err != nil {
		return CredStatus{}, err
	}
	return parseCredResponse(resp)
}

// DeleteProviderMethodKey removes a provider method's stored key daemon-side.
func (c *Client) DeleteProviderMethodKey(provider, methodID string) (CredStatus, error) {
	resp, err := c.sendRequest(map[string]any{
		"command":   "creds.delete",
		"provider":  provider,
		"method_id": methodID,
	})
	if err != nil {
		return CredStatus{}, err
	}
	return parseCredResponse(resp)
}

// SetProviderAuthDefault sets (methodID non-empty) or clears (empty) a
// provider's default credential method daemon-side.
func (c *Client) SetProviderAuthDefault(provider, methodID string) (CredStatus, error) {
	resp, err := c.sendRequest(map[string]any{
		"command":   "creds.set_default",
		"provider":  provider,
		"method_id": methodID,
	})
	if err != nil {
		return CredStatus{}, err
	}
	return parseCredResponse(resp)
}
