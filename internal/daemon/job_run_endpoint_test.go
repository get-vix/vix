package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/daemon/jobs"
)

// serveRun routes a request through a mux carrying the {id} pattern so
// r.PathValue("id") is populated, mirroring the real webserver wiring.
func serveRun(s *Server, req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/jobs/{id}/run", handleRunJob(s))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleRunJob(t *testing.T) {
	s := newRunTriggerTestServer(t)
	fakeRunner := func(context.Context, jobs.Spec, string) jobs.RunResult {
		return jobs.RunResult{Status: jobs.StatusOK}
	}
	sched := jobs.NewScheduler(jobs.NewStore(filepath.Join(s.homeVixDir, "jobs")), fakeRunner, nil, nil, 1)
	spec := jobs.Spec{
		ID:      "j",
		Enabled: true,
		Trigger: jobs.Trigger{Type: "cron", Expr: "@every 1m"},
		Prompt:  "hi",
		CWD:     t.TempDir(),
	}
	if err := sched.CreateJob(spec); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	s.jobScheduler = sched

	// Happy path: POST from a loopback origin fires the job and returns a
	// thread id.
	rec := serveRun(s, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/jobs/j/run", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["thread_id"] == "" {
		t.Fatalf("expected non-empty thread_id, got %v", body)
	}
	waitForFileContains(t, filepath.Join(s.homeVixDir, "jobs", "j", "state.json"), "last_status")

	// Unknown id → 400 with an error.
	rec = serveRun(s, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/jobs/ghost/run", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown id status = %d, want 400", rec.Code)
	}

	// Wrong method → 405.
	rec = serveRun(s, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/jobs/j/run", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}

	// Non-loopback Host → 403 (defeats cross-site/DNS-rebinding callers).
	req := httptest.NewRequest(http.MethodPost, "http://evil.example.com/api/jobs/j/run", nil)
	req.Host = "evil.example.com"
	rec = serveRun(s, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", rec.Code)
	}
}
