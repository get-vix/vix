package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"

	"github.com/get-vix/vix/internal/daemon/jobs"
)

// handleCreateJob handles POST /api/jobs: it accepts a job spec from the local
// web UI, forces the server-owned fields (a fresh id, enabled, provenance),
// validates + persists it via the scheduler, and returns the assigned id.
// Refused for non-local origins (see sameOriginLocal).
func handleCreateJob(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if !sameOriginLocal(r) {
			http.Error(w, `{"error":"forbidden origin"}`, http.StatusForbidden)
			return
		}

		// The request body is a job spec (same JSON shape as jobs.Spec, so the
		// inline workflow.Def under "workflow" decodes directly).
		var spec jobs.Spec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		// Server owns these regardless of what the client sent: the id is
		// derived server-side, web-created jobs start enabled, and provenance is
		// fixed so the UI can tell them apart from agent/user-authored ones.
		spec.ID = ""
		spec.Enabled = true
		spec.CreatedBy = "web"

		w.Header().Set("Content-Type", "application/json")
		id, err := s.CreateJob(spec)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	}
}

// handleRunJob handles POST /api/jobs/{id}/run: it fires an existing job
// immediately (out of band from its schedule), mirroring `vix job run <id>`,
// and returns the run's thread id. Refused for non-local origins. This backs
// the web UI's "add + trigger" flow, where a freshly created job is run once to
// establish its baseline right away instead of waiting for the first tick.
func handleRunJob(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if !sameOriginLocal(r) {
			http.Error(w, `{"error":"forbidden origin"}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		id := r.PathValue("id")
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "missing job id"})
			return
		}
		threadID, err := s.RunJob(id)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"thread_id": threadID})
	}
}

// sameOriginLocal reports whether the request comes from the local web UI rather
// than a cross-site caller. The Host header must resolve to a loopback address —
// this defeats DNS-rebinding, since a rebinding page's Host stays its own domain
// even after the name resolves to 127.0.0.1. When an Origin header is present
// (browser fetch) it must be loopback too. Requests without an Origin (curl,
// tests) are allowed as long as the Host is loopback.
func sameOriginLocal(r *http.Request) bool {
	if !isLoopbackHost(hostOnly(r.Host)) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !isLoopbackHost(u.Hostname()) {
			return false
		}
	}
	return true
}

// hostOnly strips an optional :port from a host[:port] string.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// isLoopbackHost reports whether host is "localhost" or a loopback IP literal.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
