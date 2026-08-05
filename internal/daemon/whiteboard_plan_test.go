package daemon

import (
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
)

func inflateParam(t *testing.T, v string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		t.Fatalf("base64url decode: %v", err)
	}
	out, err := io.ReadAll(flate.NewReader(strings.NewReader(string(raw))))
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	return string(out)
}

// TestPlanWhiteboardURL verifies the plan workflow's mermaid scenes are
// converted to positioned canvas scenes and packed into the per-thread
// whiteboard URL (compressed scenes + plan + agent), replacing the old python
// step.
func TestPlanWhiteboardURL(t *testing.T) {
	data := []byte(`[
	  {"name":"Arch","context":"the flow","mermaid":"graph LR\nA[Client]-->B[(DB)]"},
	  {"name":"Auth","context":"login","mermaid":"graph TD\nA[Client]-->B{Valid?}"}
	]`)

	got, err := planWhiteboardURL("http://localhost:1337", "thread-9", "My plan text", data)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(got, "http://localhost:1337/thread/thread-9/whiteboard?scenes_z=") {
		t.Fatalf("unexpected URL prefix: %s", got)
	}
	// Compressed base64url payload — no percent-encoding for terminals to mangle.
	if strings.Contains(got, "%") {
		t.Errorf("compressed URL should have no percent-encoding: %s", got)
	}
	if !strings.Contains(got, "&agent_id=agent_") {
		t.Errorf("voice agent id not in URL: %s", got)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	// The plan text round-trips through the compressed plan_z param.
	if plan := inflateParam(t, u.Query().Get("plan_z")); plan != "My plan text" {
		t.Errorf("plan_z = %q, want %q", plan, "My plan text")
	}

	var scenes []struct {
		Name    string           `json:"name"`
		Context string           `json:"context"`
		Nodes   []map[string]any `json:"nodes"`
		Edges   []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal([]byte(inflateParam(t, u.Query().Get("scenes_z"))), &scenes); err != nil {
		t.Fatalf("scenes not valid JSON: %v", err)
	}
	if len(scenes) != 2 {
		t.Fatalf("scenes = %d, want 2", len(scenes))
	}
	if scenes[0].Name != "Arch" || scenes[0].Context != "the flow" || len(scenes[0].Nodes) != 2 {
		t.Errorf("scene[0] = %+v", scenes[0])
	}
	if _, ok := scenes[0].Nodes[0]["x"]; !ok {
		t.Errorf("scene node missing layout coordinates: %+v", scenes[0].Nodes[0])
	}
}

func TestPlanWhiteboardURLRejectsBadJSON(t *testing.T) {
	if _, err := planWhiteboardURL("http://localhost:1337", "t", "p", []byte("not json")); err == nil {
		t.Fatal("expected an error for non-JSON scenes data")
	}
}
