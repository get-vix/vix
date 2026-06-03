package daemon

import (
	"context"
	"time"

	"github.com/kirby88/vix/internal/config"
	"github.com/kirby88/vix/internal/daemon/llm"
	"github.com/kirby88/vix/internal/protocol"
)

// modelListProviders is the set of providers the daemon attempts to list models
// for. Providers without a usable credential are skipped automatically.
var modelListProviders = []llm.ProviderID{
	llm.ProviderAnthropic,
	llm.ProviderOpenAI,
	llm.ProviderOpenRouter,
	llm.ProviderMiniMax,
	llm.ProviderMiMo,
	llm.ProviderCodex,
}

// RegisterModelsHandler registers the "list_models" RPC, which fetches the live
// model catalogue from every provider the user has credentials for. The result
// is keyed by provider id; providers that can't be reached are simply absent
// (the client falls back to its curated list for those).
func RegisterModelsHandler(s *Server) {
	s.RegisterHandler("list_models", func(data map[string]any) (map[string]any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		creds := make(map[llm.ProviderID]config.Credential, len(modelListProviders))
		for _, p := range modelListProviders {
			creds[p] = config.ResolveProviderCredentialFresh(ctx, p.CredentialName(), p.UsesOAuth())
		}

		listed := llm.ListAllModels(ctx, creds)

		// Report an entry for every provider — including those with no
		// credential — so the client can show a tailored "how to authenticate"
		// hint instead of a stale curated list.
		byProvider := make(map[string]protocol.ProviderModels, len(modelListProviders))
		for _, p := range modelListProviders {
			pid := string(p)
			pm := protocol.ProviderModels{Authenticated: creds[p].Value != ""}
			for _, m := range listed[pid] {
				pm.Models = append(pm.Models, protocol.ModelInfo{
					Spec:        m.Spec,
					Provider:    m.Provider,
					DisplayName: m.DisplayName,
					Created:     m.Created,
				})
			}
			byProvider[pid] = pm
		}

		return map[string]any{
			"status": "ok",
			"data":   map[string]any{"providers": byProvider},
		}, nil
	})
}
