package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/get-vix/vix/internal/daemon/brain"
	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

// configWatchDebounce coalesces the burst of filesystem events editors emit
// for a single save (truncate-write, or write-temp-then-rename) into one reload.
const configWatchDebounce = 250 * time.Millisecond

// configWatcher watches ~/.vix/config for changes to workflow.json and
// languages.json and hot-reloads them. Workflows are pushed to every live
// session (re-emitting event.workflows_available so the TUI refreshes its
// slash menu and Shift+Tab cycle); languages rebuild the brain ext→language
// map and restart the LSP pool.
//
// The watcher targets the home-level config directory only, matching the
// home-only resolution of these files. Config-dir override sessions read their
// own config/ and are not hot-reloaded.
type configWatcher struct {
	server   *Server
	wfPath   string
	langPath string
	jobsDir  string
	w        *fsnotify.Watcher

	mu       sync.Mutex
	debounce map[string]*time.Timer
}

// startConfigWatcher begins watching homeVixDir/config. Safe to call once from
// ListenAndServe; it returns immediately and runs until serverCtx is cancelled.
// No-op when the home dir is unavailable.
func (s *Server) startConfigWatcher() {
	if s.homeVixDir == "" {
		return
	}
	dir := filepath.Join(s.homeVixDir, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		LogError("config watcher: cannot create %s: %v", dir, err)
		return
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		LogError("config watcher: %v", err)
		return
	}
	// Watch the directory (not the files) so atomic saves that replace the
	// file's inode via rename keep delivering events.
	if err := w.Add(dir); err != nil {
		LogError("config watcher: cannot watch %s: %v", dir, err)
		w.Close()
		return
	}

	cw := &configWatcher{
		server:   s,
		wfPath:   filepath.Join(dir, "workflow.json"),
		langPath: filepath.Join(dir, "languages.json"),
		w:        w,
		debounce: make(map[string]*time.Timer),
	}

	// Hot-reload the scheduled-jobs spec directory too, when the scheduler is
	// running: writing ~/.vix/jobs/<id>.json (by hand or by the model) takes
	// effect without a daemon restart.
	if s.jobScheduler != nil {
		jobsDir := filepath.Join(s.homeVixDir, "jobs")
		if err := os.MkdirAll(jobsDir, 0o755); err == nil {
			if err := w.Add(jobsDir); err == nil {
				cw.jobsDir = jobsDir
			} else {
				LogError("config watcher: cannot watch %s: %v", jobsDir, err)
			}
		}
	}

	go cw.run(s.serverCtx)
	LogInfo("config watcher: watching %s", dir)
}

func (cw *configWatcher) run(ctx context.Context) {
	defer cw.w.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-cw.w.Events:
			if !ok {
				return
			}
			// Ignore pure chmod noise; reload on create/write/rename/remove.
			if ev.Op == fsnotify.Chmod {
				continue
			}
			switch filepath.Clean(ev.Name) {
			case cw.wfPath:
				cw.schedule(cw.wfPath, cw.reloadWorkflows)
			case cw.langPath:
				cw.schedule(cw.langPath, cw.reloadLanguages)
			default:
				if cw.jobsDir != "" && filepath.Dir(filepath.Clean(ev.Name)) == cw.jobsDir {
					cw.schedule(cw.jobsDir, cw.reloadJobs)
				}
			}
		case err, ok := <-cw.w.Errors:
			if !ok {
				return
			}
			LogError("config watcher error: %v", err)
		}
	}
}

// schedule debounces reloads per file so a single save triggers exactly one
// reload even when the editor emits several events.
func (cw *configWatcher) schedule(key string, fn func()) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if t, ok := cw.debounce[key]; ok {
		t.Stop()
	}
	cw.debounce[key] = time.AfterFunc(configWatchDebounce, fn)
}

// reloadWorkflows re-reads workflow.json and pushes the new list to every live
// session.
func (cw *configWatcher) reloadWorkflows() {
	wfs := LoadWorkflowsFile(cw.wfPath)
	LogInfo("config watcher: reloaded %d workflow(s) from %s", len(wfs), cw.wfPath)

	cw.server.sessionMu.Lock()
	sessions := make([]*Session, 0, len(cw.server.sessions))
	for _, sess := range cw.server.sessions {
		sessions = append(sessions, sess)
	}
	cw.server.sessionMu.Unlock()

	for _, sess := range sessions {
		sess.ReloadWorkflows(wfs)
	}
}

// reloadLanguages re-reads languages.json, rebuilds the brain ext→language map
// and restarts the LSP pool so subsequent operations use the new configuration.
// The VFS/formatter consumers read languages.json fresh on every call, so they
// need no explicit refresh.
func (cw *configWatcher) reloadLanguages() {
	paths := []string{cw.langPath}
	brain.ReloadLanguageMap(paths)
	lsp.ReloadPool(cw.langPath)
	LogInfo("config watcher: reloaded languages from %s", cw.langPath)
}

// reloadJobs asks the scheduler to re-read the job spec directory.
func (cw *configWatcher) reloadJobs() {
	if cw.server.jobScheduler != nil {
		LogInfo("config watcher: job specs changed, reloading scheduler")
		cw.server.jobScheduler.Reload()
	}
}
