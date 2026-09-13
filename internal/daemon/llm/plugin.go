package llm

// PluginConfig is the merged output of running all discovered .vix/plugins/
// executables on daemon startup. The plugin loader lives in package daemon;
// the struct lives here so every adapter can apply it without importing
// daemon (which would cycle).
type PluginConfig struct {
	// Headers maps HTTP header name → value. A nil pointer value means
	// "strip this header from every outgoing API request". A non-nil
	// pointer means "set (or override) this header to the given string".
	Headers map[string]*string `json:"headers"`

	// SystemPrefix is prepended as the first system-prompt text block on
	// every StreamMessage call. Empty means no-op.
	SystemPrefix string `json:"system_prefix"`

	// SessionHeader is the header key used to inject the session ID
	// (thread UUID) on every outbound LLM request. Empty means no
	// injection. Set from the provider's inference.session_header.
	SessionHeader string `json:"session_header"`
}
