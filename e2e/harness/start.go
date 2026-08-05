// Package harness is the imperative driver for vix end-to-end tests. A test
// calls Start to get an isolated environment (its own HOME, workdir, socket,
// vixd, vix-on-a-PTY and a mock LLM server), then acts as both the model
// (h.Mock) and the user (h.UI) while inspecting the filesystem (h.FS) and the
// daemon (h.Daemon). It is consumed only from the e2e module's scenarios; it
// never imports vix's internal packages — it drives the compiled binaries.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/artifact"
)

// Meta is the per-test descriptor that drives report navigation and the
// collapsible implementation view.
type Meta struct {
	Category    string // top-level group, e.g. "ui", "files", "sandbox"
	Subcategory string // e.g. "ui.threads"
	Description string // one-line: what this test verifies
	Wire        Wire   // which LLM wire dialect to exercise (default Messages)
	Variant     string // optional extra discriminator for matrix runs
}

// Wire selects the provider wire dialect the mock serves for a test.
type Wire string

const (
	WireMessages        Wire = "messages"
	WireResponses       Wire = "responses"
	WireChatCompletions Wire = "chat_completions"
)

// Harness is the live test environment. Fields are exposed as the imperative
// handles a scenario drives.
type Harness struct {
	t    *testing.T
	ctx  context.Context
	meta Meta

	Mock   *Mock
	UI     *UI
	FS     *FS
	Daemon *Daemon

	root      string // report output root
	home      string
	workdir   string
	socket    string
	logFile   string
	clientLog string

	startedAt time.Time

	cfg  *config // retained so Restart can re-spawn with the same environment
	vixd *exec.Cmd

	result artifact.Result
}

// Option configures the environment before vixd/vix start.
type Option func(*config)

// configVersion mirrors internal/daemon CurrentConfigVersion. LoadProjectConfig
// silently ignores any settings.json whose "version" field doesn't match, so the
// harness must stamp the project settings it writes.
const configVersion = 1

// withConfigVersion ensures a settings JSON object carries "version":configVersion.
// Best-effort: a non-object or unparseable body is returned unchanged.
func withConfigVersion(s string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return s
	}
	if _, ok := m["version"]; !ok {
		m["version"] = configVersion
	}
	b, err := json.Marshal(m)
	if err != nil {
		return s
	}
	return string(b)
}

type config struct {
	env          map[string]string
	settings     string   // raw settings.json content (optional)
	providers    string   // raw providers.json content (optional)
	denyPaths    []string // workdir-relative paths to add to deny_list (expanded to abs)
	model        string   // thread model spec written to state.json (optional)
	fixture      string   // dir copied into the workdir
	homeFiles    []homeFile
	workdirFiles []homeFile
	cols         int
	rows         int
	tuiArgs      []string // extra CLI flags appended to the `vix` TUI launch
}

// homeFile seeds a file under the per-test HOME before vixd starts. Content
// supports placeholders, expanded against the per-test layout:
//
//	{{WORKDIR}}  → the absolute per-test workdir
//	{{HOME}}     → the absolute per-test HOME
//	{{MOCK_URL}} → the mock LLM server base URL (no trailing /v1)
//
// Used e.g. to drop a ~/.vix/jobs/<id>/job.json spec for the scheduled-jobs
// engine, or a ~/.vix/.env for credential-resolution scenarios.
type homeFile struct{ rel, content string }

// WithEnv sets an extra environment variable on the spawned vix/vixd processes.
// The value supports the {{WORKDIR}}, {{HOME}} and {{MOCK_URL}} placeholders
// (e.g. WithEnv("LLAMACPP_BASE_URL", "{{MOCK_URL}}/v1")).
func WithEnv(k, v string) Option { return func(c *config) { c.env[k] = v } }

// WithWebUI re-enables the mission-control web UI (disabled by default in the
// harness) on a fixed local port. The daemon then reports a non-empty
// WhiteboardBase in thread_started, which the TUI needs to render the "See it on
// the whiteboard" link for mermaid diagrams. The web server need not be
// reachable — the link is just a URL — so a taken port is harmless (the daemon
// logs the bind failure and carries on).
func WithWebUI() Option {
	return func(c *config) {
		c.env["VIX_NO_MISSION_CONTROL"] = "0"
		c.env["VIX_WEB_PORT"] = "47318"
	}
}

// WithSettings writes raw JSON to the project-level .vix/settings.json (under the
// workdir), so it survives vixd's HOME bootstrap. Use for deny_list, features…
// The content supports the {{WORKDIR}}, {{HOME}} and {{MOCK_URL}} placeholders
// (e.g. a deny_list path of "{{HOME}}/.ssh").
func WithSettings(json string) Option { return func(c *config) { c.settings = json } }

// WithProviders writes raw JSON to ~/.vix/providers.json (custom wires/models).
func WithProviders(json string) Option { return func(c *config) { c.providers = json } }

// WithDenyPath adds a workdir-relative path to the deny_list (expanded to an
// absolute path under the per-test workdir) in the project-level .vix/settings.json.
// Convenience for sandbox scenarios so the test doesn't need the temp workdir
// path up front. Mutually exclusive with WithSettings for now.
func WithDenyPath(rel ...string) Option {
	return func(c *config) { c.denyPaths = append(c.denyPaths, rel...) }
}

// WithModel pins the thread model spec (e.g. "openai/gpt-4o") by writing
// ~/.vix/state.json, which the daemon reads when resolving the thread model.
// Used by the wire matrix to route a scenario through a given provider's wire.
func WithModel(spec string) Option { return func(c *config) { c.model = spec } }

// WithWorkdirFixture seeds the per-test workdir from a template directory
// (path relative to the scenarios package, i.e. the test's cwd).
func WithWorkdirFixture(dir string) Option { return func(c *config) { c.fixture = dir } }

// WithTermSize fixes the PTY dimensions (affects layout and screenshots).
func WithTermSize(cols, rows int) Option {
	return func(c *config) { c.cols, c.rows = cols, rows }
}

// WithVixArgs appends extra CLI flags to the `vix` TUI launch command (e.g.
// "-disable-automatic-write-permission" so write-class tools surface a
// confirmation prompt). Flags are passed verbatim after the harness-managed
// -socket-path / -workdir args.
func WithVixArgs(args ...string) Option {
	return func(c *config) { c.tuiArgs = append(c.tuiArgs, args...) }
}

// WithHomeFile seeds a file under the per-test HOME before vixd starts (e.g. a
// scheduled-job spec at ".vix/jobs/<id>/job.json", or ".vix/.env"). The content
// may use the {{WORKDIR}}, {{HOME}} and {{MOCK_URL}} placeholders, expanded to
// the per-test values.
func WithHomeFile(rel, content string) Option {
	return func(c *config) { c.homeFiles = append(c.homeFiles, homeFile{rel: rel, content: content}) }
}

// WithWorkdirFile seeds a file under the per-test workdir before vixd starts
// (e.g. a CWD ".env" for credential resolution, or a source fixture). The
// content supports the same placeholders as WithHomeFile.
func WithWorkdirFile(rel, content string) Option {
	return func(c *config) { c.workdirFiles = append(c.workdirFiles, homeFile{rel: rel, content: content}) }
}

// Start builds an isolated environment and brings up mock → vixd → vix. It
// registers all teardown via t.Cleanup and skips (rather than fails) when the
// environment isn't an e2e runner. Never call os.Exit between Start and the
// end of the test, or the final artifact won't be written.
func Start(t *testing.T, meta Meta, opts ...Option) *Harness {
	t.Helper()
	requireE2E(t)
	shardGate(t)

	if meta.Wire == "" {
		meta.Wire = WireMessages
	}
	_, file, line, _ := runtime.Caller(1)
	cfg := &config{env: map[string]string{}, cols: 120, rows: 40}
	for _, o := range opts {
		o(cfg)
	}

	h := &Harness{
		t:         t,
		meta:      meta,
		root:      reportRoot(),
		startedAt: time.Now(),
		result: artifact.Result{
			Category:    meta.Category,
			Subcategory: meta.Subcategory,
			Name:        t.Name(),
			Description: meta.Description,
			Wire:        string(meta.Wire),
			Variant:     meta.Variant,
			SourceFile:  file,
			SourceLine:  line,
			Source:      captureSource(file, t.Name()),
		},
	}

	// Per-test deadline drives every blocking wait (Mock.Next, UI.WaitFor…).
	deadline, ok := t.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	h.ctx = ctx
	t.Cleanup(cancel)

	// Write the start marker immediately so a crash still surfaces this test.
	if err := artifact.WriteStart(h.root, h.result); err != nil {
		t.Logf("e2e: write start marker: %v", err)
	}

	// The mock must exist before buildEnv so seeded files can reference its URL
	// via the {{MOCK_URL}} placeholder, and before daemonEnv points the
	// providers at it.
	h.Mock = newMock()
	t.Cleanup(h.Mock.close)

	h.cfg = cfg
	h.buildEnv(cfg)

	// FS + Daemon must exist before startDaemon: its failure paths use
	// h.Daemon.LogTail for diagnostics.
	h.FS = &FS{root: h.workdir}
	h.Daemon = &Daemon{h: h}
	h.startDaemon(cfg)
	h.startTUI(cfg)

	t.Cleanup(h.finish)
	return h
}

// HomePath resolves a path under the per-test HOME (e.g.
// ".vix/threads/open"). Lets a scenario inspect daemon-written state that
// lives outside the workdir, such as persisted thread records.
func (h *Harness) HomePath(rel ...string) string {
	return filepath.Join(append([]string{h.home}, rel...)...)
}

// RunCLI runs the vix binary as a one-shot subcommand against this test's
// daemon, with the per-test HOME, socket, and env. It returns the combined
// stdout+stderr and any exit error. Used to drive the out-of-band verbs
// `vix job run <id>` and `vix hook trigger <id>` (not the TUI).
func (h *Harness) RunCLI(args ...string) (string, error) {
	h.t.Helper()
	bin, err := vixBinary()
	if err != nil {
		h.t.Fatalf("e2e: resolve vix binary: %v", err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = h.daemonEnv(h.cfg, nil)
	cmd.Dir = h.workdir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// requireE2E skips unless the runner opted in and both binaries are resolvable.
func requireE2E(t *testing.T) {
	if os.Getenv("VIX_E2E") != "1" {
		t.Skip("e2e: set VIX_E2E=1 and run via `make test-e2e` (skipped under plain `go test`)")
	}
	if _, err := vixBinary(); err != nil {
		t.Skipf("e2e: %v", err)
	}
	if _, err := vixdBinary(); err != nil {
		t.Skipf("e2e: %v", err)
	}
}

// buildEnv lays out the per-test temp tree and synthetic ~/.vix.
func (h *Harness) buildEnv(cfg *config) {
	t := h.t
	tmp := t.TempDir()
	h.home = filepath.Join(tmp, "home")
	h.workdir = filepath.Join(tmp, "work")
	mustMkdir(t, filepath.Join(h.home, ".vix"))
	mustMkdir(t, h.workdir)
	mustWrite(t, filepath.Join(h.home, ".vix", "e2e-mock-ca.pem"), string(h.Mock.CACertificatePEM()))

	logs := filepath.Join(tmp, "logs")
	mustMkdir(t, logs)
	h.logFile = filepath.Join(logs, "vixd.log")

	// Socket lives in a short dir to stay under the sun_path length limit.
	sockDir, err := os.MkdirTemp("", "vxs")
	if err != nil {
		t.Fatalf("e2e: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	h.socket = filepath.Join(sockDir, "d.sock")

	// settings.json is seeded at the PROJECT level (workdir/.vix), not HOME.
	// vixd's first-run bootstrap treats ~/.vix/settings.json as a managed
	// default and overwrites it (saving a .bak) when the HOME has no matching
	// defaults_version stamp — which clobbers anything the harness writes there.
	// Project config is never bootstrapped, and deny_list/settings are merged
	// (deny_list unioned) across home + project, so the project copy survives
	// and takes effect. The "version" stamp is required: LoadProjectConfig
	// silently ignores any settings.json whose version != CurrentConfigVersion.
	if cfg.settings != "" {
		mustWrite(t, filepath.Join(h.workdir, ".vix", "settings.json"), h.expandPlaceholders(withConfigVersion(cfg.settings)))
	} else if len(cfg.denyPaths) > 0 {
		abs := make([]string, len(cfg.denyPaths))
		for i, rel := range cfg.denyPaths {
			abs[i] = filepath.Join(h.workdir, rel)
		}
		blob, _ := json.Marshal(map[string]any{
			"version":   configVersion,
			"deny_list": map[string]any{"paths": abs},
		})
		mustWrite(t, filepath.Join(h.workdir, ".vix", "settings.json"), string(blob))
	}
	mustWrite(t, filepath.Join(h.home, ".vix", "providers.json"), h.providerOverlay(cfg.providers))
	if cfg.fixture != "" {
		copyTree(t, cfg.fixture, h.workdir)
	}
	if cfg.model != "" {
		blob, _ := json.Marshal(map[string]any{"model": cfg.model})
		mustWrite(t, filepath.Join(h.home, ".vix", "state.json"), string(blob))
	}
	for _, hf := range cfg.homeFiles {
		mustWrite(t, filepath.Join(h.home, hf.rel), h.expandPlaceholders(hf.content))
	}
	for _, wf := range cfg.workdirFiles {
		mustWrite(t, filepath.Join(h.workdir, wf.rel), h.expandPlaceholders(wf.content))
	}
}

// providerOverlay routes the built-in cloud wires to this test's real HTTPS
// server while leaving credentials in the container's real daz-secrets
// provider process. Any scenario-specific provider entries are retained.
func (h *Harness) providerOverlay(custom string) string {
	doc := map[string]any{"schema_version": 1, "providers": []any{}}
	if custom != "" {
		if err := json.Unmarshal([]byte(h.expandPlaceholders(custom)), &doc); err != nil {
			h.t.Fatalf("e2e: parse providers overlay: %v", err)
		}
	}
	providers, _ := doc["providers"].([]any)
	for _, id := range []string{"anthropic", "openai", "minimax"} {
		providers = append(providers, map[string]any{
			"id":        id,
			"inference": map[string]any{"base_url": h.Mock.BaseURL() + "/v1"},
		})
	}
	doc["providers"] = providers
	blob, err := json.Marshal(doc)
	if err != nil {
		h.t.Fatalf("e2e: encode providers overlay: %v", err)
	}
	return string(blob)
}

// expandPlaceholders substitutes the per-test {{WORKDIR}}, {{HOME}} and
// {{MOCK_URL}} tokens in seeded file content.
func (h *Harness) expandPlaceholders(s string) string {
	s = strings.ReplaceAll(s, "{{WORKDIR}}", h.workdir)
	s = strings.ReplaceAll(s, "{{HOME}}", h.home)
	if h.Mock != nil {
		s = strings.ReplaceAll(s, "{{MOCK_URL}}", h.Mock.BaseURL())
	}
	return s
}

// daemonEnv composes the environment for the spawned processes.
func (h *Harness) daemonEnv(cfg *config, extra map[string]string) []string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	// Isolation + redirection (override anything inherited). Credentials are
	// supplied by the container's daz-secrets provider, never this environment.
	env["HOME"] = h.home
	// Trust only the harness TLS endpoint inside the isolated test process. The
	// provider overlay still goes through production's HTTPS-only validation.
	env["SSL_CERT_FILE"] = filepath.Join(h.home, ".vix", "e2e-mock-ca.pem")
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		delete(env, key)
	}
	env["VIX_SOCKET_PATH"] = h.socket
	env["VIX_NO_MISSION_CONTROL"] = "1"
	env["VIX_WEB_PORT"] = "0"
	env["VIX_LOG_DIR"] = filepath.Dir(h.logFile)
	env["VIX_DISABLE_JOBS"] = "1"
	env["TERM"] = "xterm-256color"
	env["CLICOLOR_FORCE"] = "1"
	for k, v := range cfg.env {
		env[k] = h.expandPlaceholders(v)
	}
	for k, v := range extra {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// finish records the final artifact (status, screenshots, diagnostics).
func (h *Harness) finish() {
	h.result.DurationMS = time.Since(h.startedAt).Milliseconds()
	switch {
	case h.t.Skipped():
		h.result.Status = artifact.StatusSkipped
	case h.t.Failed():
		h.result.Status = artifact.StatusFailed
		h.result.Diagnostics = h.diagnostics()
	default:
		h.result.Status = artifact.StatusPassed
	}
	if err := artifact.WriteFinal(h.root, h.result); err != nil {
		h.t.Logf("e2e: write final artifact: %v", err)
	}
	// Tear down the TUI first: on failure this SIGQUITs vix to dump goroutine
	// stacks into its stderr (vix-client.log) — so persist the logs AFTER.
	if h.t.Failed() {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		dir := filepath.Join(h.root, "logs")
		_ = os.MkdirAll(dir, 0o755)
		base := h.result.Slug()[strings.IndexByte(h.result.Slug(), '/')+1:]
		_ = os.WriteFile(filepath.Join(dir, base+".harness-stacks.txt"), buf[:n], 0o644)
	}
	if h.UI != nil {
		h.UI.close()
	}
	// Persist the full vixd log alongside the report for post-mortem (the
	// in-container temp log is otherwise discarded on teardown).
	if data, err := os.ReadFile(h.logFile); err == nil {
		dir := filepath.Join(h.root, "logs")
		_ = os.MkdirAll(dir, 0o755)
		base := h.result.Slug()[strings.IndexByte(h.result.Slug(), '/')+1:]
		_ = os.WriteFile(filepath.Join(dir, base+".vixd.log"), data, 0o644)
		if cdata, cerr := os.ReadFile(h.clientLog); cerr == nil {
			_ = os.WriteFile(filepath.Join(dir, base+".vix-client.log"), cdata, 0o644)
		}
	}
	if h.vixd != nil && h.vixd.Process != nil {
		_ = h.vixd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = h.vixd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = h.vixd.Process.Kill()
		}
	}
}

// diagnostics assembles the on-failure dump: final screen, vixd log, requests.
func (h *Harness) diagnostics() string {
	var b strings.Builder
	if h.UI != nil {
		fmt.Fprintf(&b, "=== final screen ===\n%s\n", h.UI.Snapshot())
	}
	fmt.Fprintf(&b, "=== sandbox mode === %s\n", h.Daemon.SandboxMode())
	fmt.Fprintf(&b, "=== vixd log (tail) ===\n%s\n", h.Daemon.LogTail(120))
	reqs := h.Mock.Requests()
	fmt.Fprintf(&b, "=== mock requests (%d) ===\n", len(reqs))
	for i, r := range reqs {
		fmt.Fprintf(&b, "[%d] user=%q toolResult=%q\n", i, truncate(r.LastUserText(), 200), truncate(r.LastToolResult(), 200))
	}
	return b.String()
}

// addScreenshot appends a capture to the result and is called by UI.Shot.
func (h *Harness) addScreenshot(s artifact.Screenshot) {
	s.Order = len(h.result.Screenshots)
	h.result.Screenshots = append(h.result.Screenshots, s)
}
