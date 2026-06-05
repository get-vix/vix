package providers

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

//go:embed providers.json
var embeddedJSON []byte

// URLDenied, when non-nil, reports whether a provider/auth URL is blocked by
// the user's deny_list. It is injected by internal/config at startup so the
// providers package stays free of internal imports (avoiding an import cycle).
// Validation rejects any spec URL for which URLDenied returns true.
var URLDenied func(string) bool

var (
	defaultMu   sync.RWMutex
	defaultReg  *Registry
	defaultOnce sync.Once
)

// Default returns the process-wide provider registry. On first use it loads the
// embedded providers.json only; call Configure first to overlay on-disk layers.
func Default() *Registry {
	defaultOnce.Do(func() {
		defaultMu.Lock()
		defer defaultMu.Unlock()
		if defaultReg != nil {
			return // Configure already populated it
		}
		reg, err := loadEmbedded()
		if err != nil {
			// The embedded file is shipped with the binary and tested; a
			// failure here is a build-time defect, not a runtime condition.
			panic(fmt.Sprintf("providers: embedded providers.json invalid: %v", err))
		}
		defaultReg = reg
	})
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultReg
}

// Configure loads the embedded defaults, overlays each existing path in order
// (later wins, merged by id), validates, and installs the result as the
// process-wide registry. Missing paths are skipped. On any error the previous
// registry is left intact and the error is returned.
func Configure(paths []string) error {
	base, err := parseFile(embeddedJSON)
	if err != nil {
		return fmt.Errorf("providers: embedded providers.json invalid: %w", err)
	}
	merged := base
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("providers: read %s: %w", p, err)
		}
		overlay, err := parseFile(data)
		if err != nil {
			return fmt.Errorf("providers: parse %s: %w", p, err)
		}
		merged = mergeFiles(merged, overlay)
	}
	if err := validate(merged); err != nil {
		return fmt.Errorf("providers: %w", err)
	}
	reg := newRegistry(merged)

	defaultMu.Lock()
	defaultReg = reg
	defaultMu.Unlock()
	// Mark the lazy init done so a later Default() doesn't overwrite us.
	defaultOnce.Do(func() {})
	return nil
}

// loadEmbedded parses and validates the embedded providers.json.
func loadEmbedded() (*Registry, error) {
	f, err := parseFile(embeddedJSON)
	if err != nil {
		return nil, err
	}
	if err := validate(f); err != nil {
		return nil, err
	}
	return newRegistry(f), nil
}

// parseFile unmarshals a providers.json document with unknown-field rejection
// so typos in user overlays surface as errors rather than silent no-ops.
func parseFile(data []byte) (File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, err
	}
	return f, nil
}

// mergeFiles overlays b onto a: providers and auth logins sharing an id are
// field-patched (non-empty fields in b win); new ids are appended in b's order.
// schema_version from b wins when non-zero.
func mergeFiles(a, b File) File {
	out := File{SchemaVersion: a.SchemaVersion}
	if b.SchemaVersion != 0 {
		out.SchemaVersion = b.SchemaVersion
	}

	// Providers.
	provIdx := make(map[string]int, len(a.Providers))
	out.Providers = make([]ProviderSpec, len(a.Providers))
	for i, p := range a.Providers {
		out.Providers[i] = p
		provIdx[p.ID] = i
	}
	for _, bp := range b.Providers {
		if i, ok := provIdx[bp.ID]; ok {
			out.Providers[i] = mergeProvider(out.Providers[i], bp)
			continue
		}
		provIdx[bp.ID] = len(out.Providers)
		out.Providers = append(out.Providers, bp)
	}

	// Auth logins.
	authIdx := make(map[string]int, len(a.AuthLogins))
	out.AuthLogins = make([]AuthLogin, len(a.AuthLogins))
	for i, l := range a.AuthLogins {
		out.AuthLogins[i] = l
		authIdx[l.ID] = i
	}
	for _, bl := range b.AuthLogins {
		if i, ok := authIdx[bl.ID]; ok {
			out.AuthLogins[i] = mergeAuthLogin(out.AuthLogins[i], bl)
			continue
		}
		authIdx[bl.ID] = len(out.AuthLogins)
		out.AuthLogins = append(out.AuthLogins, bl)
	}
	return out
}

// mergeProvider field-patches a provider: non-empty scalar fields in b win;
// slices and maps replace wholesale when present in b.
func mergeProvider(a, b ProviderSpec) ProviderSpec {
	out := a
	if b.DisplayName != "" {
		out.DisplayName = b.DisplayName
	}
	if b.ModelPrefix != "" {
		out.ModelPrefix = b.ModelPrefix
	}
	if b.WireFormat != "" {
		out.WireFormat = b.WireFormat
	}
	if b.EffortPolicy != "" {
		out.EffortPolicy = b.EffortPolicy
	}
	out.Inference = mergeInference(a.Inference, b.Inference)
	if b.Credential != nil {
		out.Credential = b.Credential
	}
	if b.Models != nil {
		out.Models = b.Models
	}
	return out
}

// mergeInference field-patches inference settings; maps merge key-by-key.
func mergeInference(a, b InferenceSpec) InferenceSpec {
	out := a
	if b.BaseURL != "" {
		out.BaseURL = b.BaseURL
	}
	if b.AuthScheme != "" {
		out.AuthScheme = b.AuthScheme
	}
	if b.AuthHeader != "" {
		out.AuthHeader = b.AuthHeader
	}
	if b.EffortStyle != "" {
		out.EffortStyle = b.EffortStyle
	}
	out.Headers = mergeStringMap(a.Headers, b.Headers)
	out.QueryParams = mergeStringMap(a.QueryParams, b.QueryParams)
	if b.JSONSet != nil {
		out.JSONSet = b.JSONSet
	}
	return out
}

// mergeAuthLogin field-patches an auth login: non-empty scalars in b win.
func mergeAuthLogin(a, b AuthLogin) AuthLogin {
	out := a
	if b.Flow != "" {
		out.Flow = b.Flow
	}
	if b.ClientID != "" {
		out.ClientID = b.ClientID
	}
	if b.ClientIDB64 != "" {
		out.ClientIDB64 = b.ClientIDB64
	}
	if b.AuthorizeURL != "" {
		out.AuthorizeURL = b.AuthorizeURL
	}
	if b.TokenURL != "" {
		out.TokenURL = b.TokenURL
	}
	if b.KeysURL != "" {
		out.KeysURL = b.KeysURL
	}
	if b.CallbackPort != 0 {
		out.CallbackPort = b.CallbackPort
	}
	if b.CallbackPath != "" {
		out.CallbackPath = b.CallbackPath
	}
	if b.RedirectURI != "" {
		out.RedirectURI = b.RedirectURI
	}
	if b.Scope != "" {
		out.Scope = b.Scope
	}
	if b.Originator != "" {
		out.Originator = b.Originator
	}
	if b.ExtraAuthorizeParams != nil {
		out.ExtraAuthorizeParams = b.ExtraAuthorizeParams
	}
	if b.Device != nil {
		out.Device = b.Device
	}
	return out
}

// mergeStringMap returns a new map with a's entries overlaid by b's. Returns
// nil when both are empty.
func mergeStringMap(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
