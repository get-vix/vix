package harness

import (
	"encoding/json"
	"net/http"
)

// Local-model provider discovery. vix probes a local provider (llama.cpp or
// Ollama, selected by LLAMACPP_BASE_URL / OLLAMA_BASE_URL) with an
// OpenAI-compatible `GET {base}/models`, then enriches the context window via
// `GET {root}/props` (llama.cpp) or `GET {root}/api/ps` + `POST {root}/api/show`
// (Ollama). The mock serves all four so a scenario can point a base-URL env at
// it and assert the model surfaces in the F3 picker (issue #35).

// localModel is the single discoverable local model the mock advertises.
type localModel struct {
	id     string
	ctxWin int64
}

// SetLocalModel makes the mock advertise one local model with the given id and
// context window across both the llama.cpp and Ollama discovery shapes. The
// provider actually probed is decided by which *_BASE_URL env the scenario
// points at the mock. Call before navigating the TUI to the Models tab.
func (m *Mock) SetLocalModel(id string, ctxWindow int64) {
	m.mu.Lock()
	m.local = &localModel{id: id, ctxWin: ctxWindow}
	m.mu.Unlock()
}

func (m *Mock) currentLocal() *localModel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.local
}

// handleOpenAIModels answers the OpenAI-compatible model list used for the
// primary reachability probe.
func (m *Mock) handleOpenAIModels(w http.ResponseWriter, _ *http.Request) {
	lm := m.currentLocal()
	data := []any{}
	if lm != nil {
		data = append(data, map[string]any{"id": lm.id, "object": "model"})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

// handleLlamaProps answers llama.cpp's native /props for context-window enrichment.
func (m *Mock) handleLlamaProps(w http.ResponseWriter, _ *http.Request) {
	var nCtx int64
	if lm := m.currentLocal(); lm != nil {
		nCtx = lm.ctxWin
	}
	writeJSON(w, map[string]any{
		"default_generation_settings": map[string]any{"n_ctx": nCtx},
	})
}

// handleOllamaPS answers Ollama's /api/ps (loaded-model probe).
func (m *Mock) handleOllamaPS(w http.ResponseWriter, _ *http.Request) {
	lm := m.currentLocal()
	models := []any{}
	if lm != nil {
		models = append(models, map[string]any{"name": lm.id})
	}
	writeJSON(w, map[string]any{"models": models})
}

// handleOllamaShow answers Ollama's /api/show with a context-length attribute.
func (m *Mock) handleOllamaShow(w http.ResponseWriter, _ *http.Request) {
	var ctx int64
	if lm := m.currentLocal(); lm != nil {
		ctx = lm.ctxWin
	}
	writeJSON(w, map[string]any{
		"model_info": map[string]any{"local.context_length": ctx},
	})
}

func writeJSON(w http.ResponseWriter, body any) {
	payload, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}
