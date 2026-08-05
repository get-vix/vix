package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
// thread (re-emitting event.workflows_available so the TUI refreshes its
// slash menu and Shift+Tab cycle); languages rebuild the brain ext→language
// map and restart the LSP pool.
//
// The watcher targets the home-level config directory only, matching the
// home-only resolution of these files. Config-dir override threads read their
// own config/ and are not hot-reloaded.
type configWatcher struct {
	server   *Server
	wfPath   string
	langPath string
	jobsDir  string
	hooksDir string
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
	// running: writing ~/.vix/jobs/<id>/job.json (by hand or by the model) takes
	// effect without a daemon restart. Each job lives in its own subdirectory,
	// so we watch the parent (to notice new job dirs) and every existing job dir
	// — fsnotify is non-recursive. Each job's state.json (machine-written run
	// state) sits inside its own subdir but is filtered out in the event loop so
	// its frequent writes never trigger a reload loop.
	if s.jobScheduler != nil {
		jobsDir := filepath.Join(s.homeVixDir, "jobs")
		if err := os.MkdirAll(jobsDir, 0o755); err == nil {
			cw.jobsDir = jobsDir
			cw.watchSpecTree(jobsDir)
		}
	}

	// Hot-reload the lifecycle-hooks spec directory too, when hooks are enabled:
	// writing ~/.vix/hooks/<id>/hook.json takes effect without a restart. Same
	// per-subdirectory layout as jobs.
	if s.hookRegistry != nil {
		hooksDir := filepath.Join(s.homeVixDir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o755); err == nil {
			cw.hooksDir = hooksDir
			cw.watchSpecTree(hooksDir)
		}
	}

	go cw.run(s.serverCtx)
	LogInfo("config watcher: watching %s", dir)
}

// watchSpecTree adds dir and each of its immediate subdirectories to the
// watcher. Jobs and hooks each keep one subdirectory per id holding the spec
// (job.json / hook.json); fsnotify is non-recursive, so both the parent (to
// notice newly created ids) and every existing id dir must be watched.
func (cw *configWatcher) watchSpecTree(dir string) {
	if err := cw.w.Add(dir); err != nil {
		LogError("config watcher: cannot watch %s: %v", dir, err)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if err := cw.w.Add(sub); err != nil {
			LogError("config watcher: cannot watch %s: %v", sub, err)
		}
	}
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
				if cw.jobsDir != "" && cw.handleSpecEvent(ev, cw.jobsDir, "job.json", cw.reloadJobs) {
					continue
				}
				if cw.hooksDir != "" {
					cw.handleSpecEvent(ev, cw.hooksDir, "hook.json", cw.reloadHooks)
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

// handleSpecEvent processes a filesystem event for a job/hook spec tree rooted
// at root. It returns true when the event belongs to that tree (so the caller
// stops looking), even when the event is deliberately ignored. A newly created
// id subdirectory is added to the watcher so its spec file (specFile, i.e.
// job.json / hook.json), written moments later, is seen. Each id's machine-
// written state.json (and its temp files) sits inside the id's own
// subdirectory; their frequent writes are filtered out so they never trigger a
// reload loop. Only the spec file itself drives a reload.
func (cw *configWatcher) handleSpecEvent(ev fsnotify.Event, root, specFile string, reload func()) bool {
	clean := filepath.Clean(ev.Name)
	if clean == root {
		return true
	}
	if !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return false
	}
	// Per-id runtime state (<id>/state.json and its temp files) lives inside a
	// watched subdirectory; its frequent writes must never trigger a reload
	// loop.
	parent := filepath.Dir(clean)
	if filepath.Dir(parent) == root && strings.HasPrefix(filepath.Base(clean), "state.") {
		return true
	}
	// A new id directory: start watching it so the spec file written inside is
	// picked up (fsnotify does not recurse).
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			cw.w.Add(clean)
			cw.schedule(root, reload)
			return true
		}
	}
	// Only the spec file itself (<id>/<specFile>) drives a reload. Other files a
	// run may write inside its own id directory — a memory file, scratch output —
	// must not trigger a spec reload (state.* is already excluded above).
	if filepath.Dir(parent) == root && filepath.Base(clean) != specFile {
		return true
	}
	cw.schedule(root, reload)
	return true
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
// thread.
func (cw *configWatcher) reloadWorkflows() {
	wfs := LoadWorkflowsFile(cw.wfPath)
	LogInfo("config watcher: reloaded %d workflow(s) from %s", len(wfs), cw.wfPath)

	cw.server.threadMu.Lock()
	threads := make([]*Thread, 0, len(cw.server.threads))
	for _, sess := range cw.server.threads {
		threads = append(threads, sess)
	}
	cw.server.threadMu.Unlock()

	for _, sess := range threads {
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

// reloadHooks asks the hook registry to re-read the hook spec directory.
func (cw *configWatcher) reloadHooks() {
	if cw.server.hookRegistry != nil {
		LogInfo("config watcher: hook specs changed, reloading registry")
		cw.server.hookRegistry.Reload()
	}
}
