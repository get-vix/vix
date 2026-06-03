package daemon

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/config"
	"golang.org/x/net/websocket"
)

//go:embed web/dist
var webDist embed.FS

// StartPprofServer starts a pprof HTTP server on 127.0.0.1:<port>.
// Routes are registered by the net/http/pprof side-effect import.
// Blocks until ctx is cancelled; call in a goroutine.
func StartPprofServer(ctx context.Context, port int) {
	if port <= 0 {
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv := &http.Server{Addr: addr, Handler: http.DefaultServeMux}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background()) //nolint:errcheck
	}()
	log.Printf("pprof: http://%s/debug/pprof/", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("pprof server error: %v", err)
	}
}

// StartWebServer starts the local web UI HTTP server on 127.0.0.1:<port>.
// It blocks until srv.ListenAndServe returns (call in a goroutine).
func StartWebServer(ctx context.Context, s *Server, port int) {
	distFS, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		log.Printf("Web UI: failed to sub dist FS: %v", err)
		return
	}

	// Prime the CPU delta counter so the first WebSocket message has a real value.
	go collectVitals()

	fileServer := http.FileServer(http.FS(distFS))
	mux := http.NewServeMux()

	// Vite-generated JS/CSS bundles live under /assets/
	mux.Handle("/assets/", fileServer)

	// Existing API routes — unchanged
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(s.Sessions())
		w.Write(data)
	})

	// New per-session API routes
	mux.HandleFunc("/api/session/{id}/interview-data", handleInterviewData(s))
	mux.HandleFunc("/api/session/{id}/signed-url", handleSignedURL(s))
	mux.HandleFunc("/api/session/{id}/call-agent", handleCallAgent(s))

	// WebSocket for live session updates
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		ch := s.Subscribe()
		defer s.Unsubscribe(ch)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			buf := make([]byte, 64)
			for {
				_, err := conn.Read(buf)
				if err != nil {
					return
				}
			}
		}()

		if err := sendUpdate(conn, s.Sessions(), collectVitals()); err != nil {
			return
		}

		for {
			select {
			case <-ch:
				if err := sendUpdate(conn, s.Sessions(), collectVitals()); err != nil {
					return
				}
			case <-ticker.C:
				if err := sendUpdate(conn, s.Sessions(), collectVitals()); err != nil {
					return
				}
			case <-readDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}))

	// Catch-all: serve real files directly, fall back to index.html for SPA routing
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// Try to open the requested path as a real file
		if f, err := distFS.Open(p); err == nil {
			f.Close()
			if strings.HasSuffix(p, ".html") {
				w.Header().Set("Cache-Control", "no-store")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fall back to index.html for all SPA routes (React Router handles them)
		data, err := fs.ReadFile(distFS, "index.html")
		if err != nil {
			http.Error(w, "index.html not found — run make build-web", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("Web UI server error: %v", err)
		return
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	log.Printf("Web UI: %s", url)
	go openBrowser(url)

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("Web UI server error: %v", err)
	}
}

func handleInterviewData(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		title := id
		for _, sess := range s.Sessions() {
			if sess.ID == id {
				title = sess.CWD
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":                     id,
			"title":                  title,
			"maximumDurationSeconds": 2700,
			"difficulty":             "Practice",
			"userProfile": map[string]any{
				"firstName": "",
				"seniority": nil,
			},
		})
	}
}

func handleSignedURL(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		paths := config.NewVixPaths("", s.homeVixDir, "")

		agentID := r.URL.Query().Get("agent_id")
		if agentID == "" {
			agentID = config.ElevenLabsAgentID(paths)
		}

		w.Header().Set("Content-Type", "application/json")

		if config.ElevenLabsAuthMode(paths) == "public" {
			json.NewEncoder(w).Encode(map[string]string{"agentId": agentID})
			return
		}

		// signed_url mode: exchange the agent ID for a temporary signed URL
		// using the server-side API key. Switch auth_mode back to "public" in
		// settings to bypass this and connect directly without an API key.
		apiKey, _ := config.ResolveEnvVar("ELEVENLABS_API_KEY")
		if apiKey == "" {
			http.Error(w, `{"error":"ELEVENLABS_API_KEY not configured"}`, http.StatusInternalServerError)
			return
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
			"https://api.elevenlabs.io/v1/convai/conversation/get-signed-url?agent_id="+agentID+"&include_conversation_id=true",
			nil)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		req.Header.Set("xi-api-key", apiKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, `{"error":"failed to reach ElevenLabs"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			http.Error(w, `{"error":"ElevenLabs API error"}`, resp.StatusCode)
			return
		}

		var elResp struct {
			SignedURL string `json:"signed_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&elResp); err != nil || elResp.SignedURL == "" {
			http.Error(w, `{"error":"invalid response from ElevenLabs"}`, http.StatusBadGateway)
			return
		}

		json.NewEncoder(w).Encode(map[string]string{"signedUrl": elResp.SignedURL})
	}
}

func handleCallAgent(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		id := r.PathValue("id")
		sess := s.getSession(id)
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}

		var body struct {
			Agent  string `json:"agent"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Agent == "" || body.Prompt == "" {
			http.Error(w, `{"error":"agent and prompt are required"}`, http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()

		result, err := sess.RunExploration(ctx, body.Agent, body.Prompt)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if result.IsError {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": result.Output})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"result": result.Output})
	}
}

func sendUpdate(conn *websocket.Conn, sessions []SessionInfo, vitals ServerVitals) error {
	data, err := json.Marshal(wsMessage{Sessions: sessions, Vitals: vitals})
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, string(data))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Web UI: could not open browser: %v", err)
	}
}
