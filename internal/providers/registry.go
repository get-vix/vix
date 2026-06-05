package providers

import (
	"fmt"
	"strings"
)

// Registry is an immutable, validated set of provider specs and auth logins,
// indexed for lookup by id and by model-spec prefix.
type Registry struct {
	providers  []ProviderSpec
	byID       map[string]int // index into providers
	authLogins []AuthLogin
	authByID   map[string]int // index into authLogins
}

// newRegistry builds and indexes a Registry from a parsed, merged file. It
// assumes f has already passed validation.
func newRegistry(f File) *Registry {
	r := &Registry{
		providers:  f.Providers,
		byID:       make(map[string]int, len(f.Providers)),
		authLogins: f.AuthLogins,
		authByID:   make(map[string]int, len(f.AuthLogins)),
	}
	for i := range f.Providers {
		r.byID[f.Providers[i].ID] = i
	}
	for i := range f.AuthLogins {
		r.authByID[f.AuthLogins[i].ID] = i
	}
	return r
}

// All returns every provider spec in declaration order.
func (r *Registry) All() []ProviderSpec {
	return r.providers
}

// Lookup returns the provider spec with the given id.
func (r *Registry) Lookup(id string) (ProviderSpec, bool) {
	i, ok := r.byID[id]
	if !ok {
		return ProviderSpec{}, false
	}
	return r.providers[i], true
}

// IDs returns every provider id in declaration order.
func (r *Registry) IDs() []string {
	out := make([]string, len(r.providers))
	for i, p := range r.providers {
		out[i] = p.ID
	}
	return out
}

// Prefixes returns every registered model-spec prefix (with trailing slash) in
// declaration order — used for ParseModel error hints.
func (r *Registry) Prefixes() []string {
	out := make([]string, len(r.providers))
	for i, p := range r.providers {
		out[i] = p.Prefix()
	}
	return out
}

// ParseModel maps a vix-style model spec (with mandatory provider prefix) to
// its provider spec and bare model name — the first matching prefix wins. Bare
// unprefixed names error explicitly rather than routing to the wrong provider.
func (r *Registry) ParseModel(spec string) (ProviderSpec, string, error) {
	if spec == "" {
		return ProviderSpec{}, "", fmt.Errorf("model spec is empty")
	}
	for _, p := range r.providers {
		if strings.HasPrefix(spec, p.Prefix()) {
			return p, strings.TrimPrefix(spec, p.Prefix()), nil
		}
	}
	return ProviderSpec{}, "", fmt.Errorf("model spec %q must start with one of: %s", spec, strings.Join(r.Prefixes(), ", "))
}

// DefaultEffort returns the default reasoning effort for a bare model name
// under the provider's effort policy.
func (p ProviderSpec) DefaultEffort(model string) string {
	switch p.EffortPolicy {
	case EffortAdaptive:
		return "adaptive"
	case EffortOpenAIReasoning:
		if IsReasoningModel(model) {
			return "medium"
		}
		return ""
	}
	return ""
}

// ContextWindow returns the input context window in tokens for a full model
// spec (e.g. "anthropic/claude-opus-4-8"), or 0 when the model isn't catalogued
// or has no window recorded. Callers treat 0 as "unknown".
func (r *Registry) ContextWindow(spec string) int64 {
	p, _, err := r.ParseModel(spec)
	if err != nil {
		return 0
	}
	for _, m := range p.Models {
		if m.Spec == spec {
			return m.ContextWindow
		}
	}
	return 0
}

// AuthLogin returns the auth login spec with the given id.
func (r *Registry) AuthLogin(id string) (AuthLogin, bool) {
	i, ok := r.authByID[id]
	if !ok {
		return AuthLogin{}, false
	}
	return r.authLogins[i], true
}

// AuthLogins returns every auth login spec in declaration order.
func (r *Registry) AuthLogins() []AuthLogin {
	return r.authLogins
}

// IsReasoningModel reports whether a bare model name belongs to a
// reasoning-capable family that accepts the OpenAI reasoning_effort knob. Used
// for the openai_reasoning default-effort policy.
func IsReasoningModel(model string) bool {
	m := strings.ToLower(model)
	// OpenRouter prefixes upstream models with "openai/" etc.; take the leaf.
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4") ||
		strings.HasPrefix(m, "gpt-5") ||
		strings.Contains(m, "-thinking")
}
