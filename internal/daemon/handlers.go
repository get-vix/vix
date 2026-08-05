package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
)

// RegisterBuiltinHandlers registers ping, init, and force_init handlers.
func RegisterBuiltinHandlers(s *Server) {
	RegisterCredentialHandlers(s)
	RegisterLocalProviderHandlers(s)

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

	// thread.list returns every persisted open thread, regardless of the
	// requesting cwd. The global store (~/.vix/threads) is surfaced whole so
	// the TUI can group threads by working directory; cwd scoping (for which
	// threads to auto-attach on launch) is applied by the client, not here.
	s.RegisterHandler("thread.list", func(data map[string]any) (map[string]any, error) {
		cwd, _ := data["cwd"].(string)
		configDir, _ := data["config_dir"].(string)
		paths := config.NewVixPaths(configDir, s.homeVixDir, cwd)
		recs := listOpenThreadRecords(paths)
		summaries := make([]protocol.ThreadSummary, 0, len(recs))
		for _, r := range recs {
			sum := r.summary()
			// Mark threads currently live in this daemon so the launching
			// client can skip the ones another instance already owns.
			s.threadMu.Lock()
			_, sum.Attached = s.threads[r.ID]
			s.threadMu.Unlock()
			summaries = append(summaries, sum)
		}
		return map[string]any{"status": "ok", "threads": summaries}, nil
	})

	// thread.dirs ranks the working directories used by open user threads
	// (across all projects, unlike thread.list which is cwd-scoped). Powers the
	// welcome screen's recent-directories list and the default working directory
	// for new threads.
	s.RegisterHandler("thread.dirs", func(data map[string]any) (map[string]any, error) {
		configDir, _ := data["config_dir"].(string)
		cwd, _ := data["cwd"].(string)
		paths := config.NewVixPaths(configDir, s.homeVixDir, cwd)
		dirs := aggregateThreadDirs(listOpenThreadRecords(paths))
		return map[string]any{"status": "ok", "dirs": dirs}, nil
	})

	// attachment.validate checks, at drop time, whether a user-attached file can
	// be turned into prompt text: it authorizes the path against the owning
	// thread's access policy + deny list, enforces the size cap, and (for PDFs)
	// confirms the built-in reader can extract a text layer. The file is re-read
	// and converted at send time; this is a fast up-front gate so the TUI can add
	// a chip ("ok") or alert and drop it ("invalid").
	s.RegisterHandler("attachment.validate", func(data map[string]any) (map[string]any, error) {
		path, _ := data["path"].(string)
		if path == "" {
			return map[string]any{"status": "error", "message": "missing 'path'"}, nil
		}
		threadID, _ := data["thread_id"].(string)
		s.threadMu.Lock()
		sess := s.threads[threadID]
		s.threadMu.Unlock()
		if sess == nil {
			return map[string]any{"status": "error", "message": "unknown thread"}, nil
		}
		raw, err := sess.readAttachmentFile(path)
		if err != nil {
			return map[string]any{"status": attachmentInvalid, "reason": err.Error()}, nil
		}
		status, reason, _ := resolveFileAttachment(path, raw)
		return map[string]any{"status": status, "reason": reason}, nil
	})

	// thread.dismiss archives a persisted thread record (open/ → closed/)
	// without attaching it. Used by the TUI to dismiss vix-initiated run
	// records from the threads list. Refuses threads currently live in a
	// connection.
	s.RegisterHandler("thread.dismiss", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		s.threadMu.Lock()
		_, live := s.threads[id]
		s.threadMu.Unlock()
		if live {
			return map[string]any{"status": "error", "message": "thread is open in another connection"}, nil
		}
		cwd, _ := data["cwd"].(string)
		configDir, _ := data["config_dir"].(string)
		paths := config.NewVixPaths(configDir, s.homeVixDir, cwd)
		if err := moveThreadToClosed(paths, id); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		s.broadcastThreadsChanged()
		return map[string]any{"status": "ok"}, nil
	})

	// message.create materialises a Vix-initiated message thread from a whole
	// JSON spec (MessageThreadSpec) carried in the "thread" field. It backs
	// `vix thread create`, letting external callers (notably command hooks)
	// surface a one-message conversation under "Vix-initiated" without
	// re-encoding the on-disk thread record.
	s.RegisterHandler("message.create", func(data map[string]any) (map[string]any, error) {
		raw, ok := data["thread"]
		if !ok {
			return map[string]any{"status": "error", "message": "missing 'thread'"}, nil
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return map[string]any{"status": "error", "message": "invalid thread payload"}, nil
		}
		var spec MessageThreadSpec
		if err := json.Unmarshal(b, &spec); err != nil {
			return map[string]any{"status": "error", "message": fmt.Sprintf("invalid thread payload: %v", err)}, nil
		}
		id, err := s.createMessageThread(spec)
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "thread_id": id}, nil
	})

	// job.run fires a scheduled job immediately by id, out of band from its
	// schedule (backs `vix job run <id>`). Returns the run's thread id; the run
	// proceeds in the background and lands under "Vix-initiated".
	s.RegisterHandler("job.run", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		threadID, err := s.RunJob(id)
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "thread_id": threadID}, nil
	})

	// hook.trigger fires a lifecycle hook immediately by id, out of band from its
	// event (backs `vix hook trigger <id>`). Workflow/prompt hooks run in an
	// isolated thread (its id is returned); command hooks have no thread, so
	// only the fire id is returned.
	s.RegisterHandler("hook.trigger", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		threadID, fireID, err := s.TriggerHook(id)
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "thread_id": threadID, "fire_id": fireID}, nil
	})

	// job.list returns the scheduled jobs (enabled and disabled), powering the
	// TUI's Jobs & Triggers tab. Jobs are daemon-global (~/.vix/jobs), so the
	// list is not cwd-filtered.
	s.RegisterHandler("job.list", func(data map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok", "jobs": s.JobSummaries()}, nil
	})

	// hook.list returns the lifecycle hooks (enabled and disabled), powering the
	// TUI's Jobs & Triggers tab.
	s.RegisterHandler("hook.list", func(data map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok", "hooks": s.HookSummaries()}, nil
	})

	// job.set_enabled toggles a job's `enabled` field (surgical in-place edit of
	// its job.json) and reschedules. Backs the Space toggle in the Jobs &
	// Triggers tab.
	s.RegisterHandler("job.set_enabled", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		enabled, _ := data["enabled"].(bool)
		if err := s.SetJobEnabled(id, enabled); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok"}, nil
	})

	// hook.set_enabled toggles a hook's `enabled` field (surgical in-place edit
	// of its hook.json) and reloads. Backs the Space toggle in the Jobs &
	// Triggers tab.
	s.RegisterHandler("hook.set_enabled", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		enabled, _ := data["enabled"].(bool)
		if err := s.SetHookEnabled(id, enabled); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok"}, nil
	})

	// mcp.list returns the configured MCP servers (home-only) with their status,
	// type, and tool count, powering the TUI's MCP tab. Enabled servers are
	// probed on demand.
	s.RegisterHandler("mcp.list", func(data map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok", "servers": s.MCPServerSummaries()}, nil
	})

	// mcp.set_enabled toggles an MCP server's `enabled` field (surgical in-place
	// edit of the home settings.json). Backs the Space toggle in the MCP tab.
	s.RegisterHandler("mcp.set_enabled", func(data map[string]any) (map[string]any, error) {
		name, _ := data["name"].(string)
		if name == "" {
			return map[string]any{"status": "error", "message": "missing 'name'"}, nil
		}
		enabled, _ := data["enabled"].(bool)
		if err := s.SetMCPEnabled(name, enabled); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok"}, nil
	})

	// mcp.authorize starts the interactive OAuth flow for a server and returns
	// the authorization URL to open in a browser. Backs `vix mcp auth` and the
	// F4 tab's authenticate action.
	s.RegisterHandler("mcp.authorize", func(data map[string]any) (map[string]any, error) {
		name, _ := data["name"].(string)
		if name == "" {
			return map[string]any{"status": "error", "message": "missing 'name'"}, nil
		}
		authURL, err := s.BeginMCPAuth(name)
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "auth_url": authURL}, nil
	})

	// mcp.logout deletes the stored OAuth token for a server. Backs
	// `vix mcp logout` and the F4 tab's sign-out action.
	s.RegisterHandler("mcp.logout", func(data map[string]any) (map[string]any, error) {
		name, _ := data["name"].(string)
		if name == "" {
			return map[string]any{"status": "error", "message": "missing 'name'"}, nil
		}
		if err := s.LogoutMCP(name); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok"}, nil
	})
}
