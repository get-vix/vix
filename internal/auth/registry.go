package auth

import "sync"

// Provider registry: built-in providers are registered at init, custom ones
// can be added, and unregistering a built-in restores its default
// implementation.
var (
	registryMu    sync.RWMutex
	providerByID  map[string]Provider
	providerOrder []string
	builtinByID   map[string]Provider
	builtinOrder  []string
)

func init() {
	builtins := []Provider{
		newAnthropicProvider(),
		newOpenAICodexProvider(),
	}
	builtinByID = make(map[string]Provider, len(builtins))
	builtinOrder = make([]string, 0, len(builtins))
	for _, p := range builtins {
		builtinByID[p.ID()] = p
		builtinOrder = append(builtinOrder, p.ID())
	}
	resetProvidersLocked()
}

func resetProvidersLocked() {
	providerByID = make(map[string]Provider, len(builtinByID))
	providerOrder = append([]string(nil), builtinOrder...)
	for id, p := range builtinByID {
		providerByID[id] = p
	}
}

// GetProvider returns the registered provider for id.
func GetProvider(id string) (Provider, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := providerByID[id]
	return p, ok
}

// GetProviders returns all registered providers in a stable order (built-ins
// first, then any custom providers in registration order).
func GetProviders() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Provider, 0, len(providerOrder))
	for _, id := range providerOrder {
		if p, ok := providerByID[id]; ok {
			out = append(out, p)
		}
	}
	return out
}

// RegisterProvider registers (or replaces) a custom OAuth provider.
func RegisterProvider(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := providerByID[p.ID()]; !exists {
		providerOrder = append(providerOrder, p.ID())
	}
	providerByID[p.ID()] = p
}

// UnregisterProvider removes a custom provider, or restores the built-in
// implementation when id names a built-in.
func UnregisterProvider(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if builtin, ok := builtinByID[id]; ok {
		providerByID[id] = builtin
		return
	}
	delete(providerByID, id)
	for i, existing := range providerOrder {
		if existing == id {
			providerOrder = append(providerOrder[:i], providerOrder[i+1:]...)
			break
		}
	}
}

// ResetProviders restores the registry to the built-in providers only.
func ResetProviders() {
	registryMu.Lock()
	defer registryMu.Unlock()
	resetProvidersLocked()
}
