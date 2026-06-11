package daemon

import (
	"os"
	"path/filepath"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
)

// RegisterBuiltinHandlers registers ping, init, and force_init handlers.
func RegisterBuiltinHandlers(s *Server) {
	RegisterCredentialHandlers(s)

	s.RegisterHandler("ping", func(data map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok", "message": "pong", "version": s.version}, nil
	})

	// daemon.stop performs a coordinated shutdown: every attached vix instance
	// is told to quit, then the daemon exits. Used by `vix daemon stop` and
	// deliberately version-gate-exempt (one-shot RPCs carry no version): the
	// stop command must work precisely when client and daemon versions differ.
	s.RegisterHandler("daemon.stop", func(data map[string]any) (map[string]any, error) {
		go s.QuitAll()
		return map[string]any{"status": "ok", "message": "stopping"}, nil
	})

	s.RegisterHandler("init", func(data map[string]any) (map[string]any, error) {
		path, _ := data["path"].(string)
		if path == "" {
			return map[string]any{"status": "error", "message": "missing 'path'"}, nil
		}
		handler := s.GetHandler("brain.init")
		if handler == nil {
			return map[string]any{"status": "error", "message": "brain.init handler not registered"}, nil
		}
		return handler(map[string]any{"params": map[string]any{"project_path": path}})
	})

	s.RegisterHandler("force_init", func(data map[string]any) (map[string]any, error) {
		path, _ := data["path"].(string)
		if path == "" {
			return map[string]any{"status": "error", "message": "missing 'path'"}, nil
		}
		brainDir := filepath.Join(path, ".vix")

		// Only remove generated artifacts, preserve user config (settings.json, etc.)
		os.RemoveAll(filepath.Join(brainDir, "context"))

		handler := s.GetHandler("brain.init")
		if handler == nil {
			return map[string]any{"status": "error", "message": "brain.init handler not registered"}, nil
		}
		return handler(map[string]any{"params": map[string]any{"project_path": path}})
	})

	// session.list returns the persisted open sessions for the requesting cwd,
	// so a freshly launched TUI can reopen them. Filtering by cwd keeps the
	// global store (~/.vix/sessions) project-scoped at the UI layer.
	s.RegisterHandler("session.list", func(data map[string]any) (map[string]any, error) {
		cwd, _ := data["cwd"].(string)
		configDir, _ := data["config_dir"].(string)
		paths := config.NewVixPaths(configDir, s.homeVixDir, cwd)
		recs := listOpenSessionRecords(paths)
		summaries := make([]protocol.SessionSummary, 0, len(recs))
		for _, r := range recs {
			if cwd != "" && r.CWD != cwd {
				continue
			}
			sum := r.summary()
			// Mark sessions currently live in this daemon so the launching
			// client can skip the ones another instance already owns.
			s.sessionMu.Lock()
			_, sum.Attached = s.sessions[r.ID]
			s.sessionMu.Unlock()
			summaries = append(summaries, sum)
		}
		return map[string]any{"status": "ok", "sessions": summaries}, nil
	})

	// session.dismiss archives a persisted session record (open/ → closed/)
	// without attaching it. Used by the TUI to dismiss vix-initiated run
	// records from the sessions list. Refuses sessions currently live in a
	// connection.
	s.RegisterHandler("session.dismiss", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		s.sessionMu.Lock()
		_, live := s.sessions[id]
		s.sessionMu.Unlock()
		if live {
			return map[string]any{"status": "error", "message": "session is open in another connection"}, nil
		}
		cwd, _ := data["cwd"].(string)
		configDir, _ := data["config_dir"].(string)
		paths := config.NewVixPaths(configDir, s.homeVixDir, cwd)
		if err := moveSessionToClosed(paths, id); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		s.broadcastSessionsChanged()
		return map[string]any{"status": "ok"}, nil
	})
}
