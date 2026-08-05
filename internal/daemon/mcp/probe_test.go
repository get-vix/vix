package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecretAccount(t *testing.T) {
	cases := map[string]struct {
		account string
		ok      bool
	}{
		"literal":                  {"", false},
		"${secret:mcp-api-token}":  {"mcp-api-token", true},
		"${MCP_EXAMPLE_API_TOKEN}": {"mcp-example-api-token", true},
		"${secret:}":               {"", false},
	}
	for value, want := range cases {
		account, ok := secretAccount(value)
		if account != want.account || ok != want.ok {
			t.Errorf("secretAccount(%q) = (%q, %v), want (%q, %v)", value, account, ok, want.account, want.ok)
		}
	}
}

// mockMCPHTTPServer returns an httptest server implementing the minimal MCP
// HTTP handshake (initialize + tools/list) advertising the given tool names.
func mockMCPHTTPServer(t *testing.T, toolNames ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		var body []byte
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			tools := make([]map[string]any, 0, len(toolNames))
			for _, n := range toolNames {
				tools = append(tools, map[string]any{"name": n, "description": n})
			}
			result, _ := json.Marshal(map[string]any{"tools": tools})
			body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(result)})
		default: // initialize and any notification
			body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(`{}`)})
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServerConfigIsEnabled(t *testing.T) {
	tru, fls := true, false
	if !(ServerConfig{}).IsEnabled() {
		t.Error("nil Enabled should default to enabled")
	}
	if !(ServerConfig{Enabled: &tru}).IsEnabled() {
		t.Error("Enabled=true should be enabled")
	}
	if (ServerConfig{Enabled: &fls}).IsEnabled() {
		t.Error("Enabled=false should be disabled")
	}
}

func TestProbeServers(t *testing.T) {
	good := mockMCPHTTPServer(t, "alpha", "beta", "gamma")

	configs := []ServerConfig{
		{Name: "good", Type: "url", URL: good.URL},
		{Name: "filtered", Type: "url", URL: good.URL, AllowedTools: []string{"alpha"}},
		{Name: "badcmd", Type: "stdio"},        // missing command → validation error
		{Name: "", Type: "url", URL: good.URL}, // empty name → skipped
	}

	statuses := ProbeServers(context.Background(), configs)
	if len(statuses) != 3 {
		t.Fatalf("expected 3 statuses (empty-name skipped), got %d: %+v", len(statuses), statuses)
	}

	byName := map[string]ServerStatus{}
	for _, s := range statuses {
		byName[s.Name] = s
	}

	if g := byName["good"]; !g.Connected || g.ToolCount != 3 || g.Type != "url" {
		t.Errorf("good server: %+v, want connected url with 3 tools", g)
	}
	if f := byName["filtered"]; !f.Connected || f.ToolCount != 1 {
		t.Errorf("filtered server: %+v, want connected with 1 tool", f)
	}
	if b := byName["badcmd"]; b.Connected || b.Error == "" || b.Type != "stdio" {
		t.Errorf("badcmd server: %+v, want disconnected stdio with error", b)
	}
}
