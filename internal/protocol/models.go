package protocol

// ModelInfo is one selectable model returned by the daemon's list_models RPC.
type ModelInfo struct {
	Spec        string `json:"spec"`         // full prefixed spec, e.g. "anthropic/claude-opus-4-8"
	Provider    string `json:"provider"`     // provider id, e.g. "anthropic"
	DisplayName string `json:"display_name"` // human-readable label
	Created     int64  `json:"created"`      // unix seconds published; 0 if unknown
}

// ProviderModels is the list_models result for one provider: whether a usable
// credential was found, and the models fetched (empty when unauthenticated or
// the fetch failed).
type ProviderModels struct {
	Authenticated bool        `json:"authenticated"`
	Models        []ModelInfo `json:"models"`
}
