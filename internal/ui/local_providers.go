package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/get-vix/vix/internal/daemon"
)

// localProvidersMsg carries the daemon's live probe of the local providers
// (Ollama, llama.cpp): reachability plus discovered models.
type localProvidersMsg struct {
	states map[string]daemon.LocalProviderState
}

// fetchLocalProviders asks the daemon to probe the local providers' servers.
// Triggered on entering the Models tab and when the provider cursor lands on a
// local provider (the daemon caches probe results for a few seconds).
func fetchLocalProviders(socketPath, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		states, err := client.LocalProviderStatus()
		if err != nil {
			return localProvidersMsg{}
		}
		return localProvidersMsg{states: states}
	}
}

// localProviderUIFromState converts one daemon probe result into the Models-tab
// view state: prefixed specs become grid entries, Ollama's in-memory models get
// a "●" marker, and a single-model llama.cpp server is labeled as fixed (the
// model is chosen at server startup, not per request).
func localProviderUIFromState(st daemon.LocalProviderState) LocalProviderUI {
	ui := LocalProviderUI{BaseURL: st.BaseURL, Reachable: st.Reachable, Fetched: true}
	fixed := st.Provider == "llamacpp" && len(st.Models) == 1
	for _, lm := range st.Models {
		name := lm.DisplayName
		if lm.Loaded {
			name += " ●"
		}
		if fixed {
			name += " (fixed by server)"
		}
		ui.Models = append(ui.Models, ModelInfo{
			Spec:        lm.Spec,
			Provider:    st.Provider,
			DisplayName: name,
		})
	}
	return ui
}
