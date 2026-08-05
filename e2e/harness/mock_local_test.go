package harness

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestLocalProviderEndpoints checks the mock serves the llama.cpp / Ollama
// discovery shapes vix probes, so a local-detection scenario (issue #35) can
// point a *_BASE_URL env at the mock and find the advertised model.
func TestLocalProviderEndpoints(t *testing.T) {
	m := newMock()
	defer m.close()

	// Before configuration the model list is empty (no local model advertised).
	if body := get(t, m, "/v1/models"); !strings.Contains(body, `"data":[]`) {
		t.Errorf("empty /v1/models = %s, want empty data", body)
	}

	m.SetLocalModel("my-local-model", 4096)

	if body := get(t, m, "/v1/models"); !strings.Contains(body, `"id":"my-local-model"`) {
		t.Errorf("/v1/models missing model id\n%s", body)
	}
	if body := get(t, m, "/props"); !strings.Contains(body, `"n_ctx":4096`) {
		t.Errorf("/props missing n_ctx\n%s", body)
	}
	if body := get(t, m, "/api/ps"); !strings.Contains(body, `"name":"my-local-model"`) {
		t.Errorf("/api/ps missing model name\n%s", body)
	}
	if body := get(t, m, "/api/show"); !strings.Contains(body, `"local.context_length":4096`) {
		t.Errorf("/api/show missing context length\n%s", body)
	}
}

func get(t *testing.T, m *Mock, path string) string {
	t.Helper()
	resp, err := http.Get(m.BaseURL() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
