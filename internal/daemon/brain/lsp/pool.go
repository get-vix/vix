package lsp

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// ServerConfig describes how to launch an LSP server for a language.
type ServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// FormatterConfig describes how to run a code formatter for a language.
// The formatter is invoked with the file path appended to Args (if any),
// and it must write the formatted output to stdout.
type FormatterConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// VFSConfig controls per-language virtual filesystem behaviour.
type VFSConfig struct {
	Enable       bool `json:"enable"`
	KeepComments bool `json:"keep_comments"`
}

// LanguageConfig groups a display name, file extensions, and an optional LSP server config.
type LanguageConfig struct {
	Name       string           `json:"name"`
	Extensions []string         `json:"extensions"`
	LSP        *ServerConfig    `json:"lsp"`
	Formatter  *FormatterConfig `json:"formatter"`
	VFS        *VFSConfig       `json:"vfs,omitempty"`
}

const failCooldown = 30 * time.Second

// Pool manages long-lived LSP server subprocesses, one per language.
type Pool struct {
	mu         sync.Mutex
	clients    map[string]*Client
	configs    map[string]*ServerConfig
	formatters map[string]*FormatterConfig
	extMap     map[string]string    // maps file extension (e.g. ".go") to language name (e.g. "go")
	failedAt   map[string]time.Time // languages that failed to start + when
	rootDir    string
	ctx        context.Context

	// done is closed by Shutdown so the per-pool ctx watcher goroutine exits
	// when the pool is replaced (InitPool is called on every brain.init);
	// without it one watcher leaked per session for the daemon's lifetime.
	done      chan struct{}
	closeOnce sync.Once
}

var (
	globalPool   *Pool
	globalPoolMu sync.Mutex
)

// InitPool reads settings.json from the given paths in order, merges them
// (later entries override earlier by language Name), and creates the global
// pool. Servers are started lazily on first GetClient call.
func InitPool(ctx context.Context, rootDir string, settingsPaths ...string) {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	// Shut down any previously-initialized pool before replacing it so we don't
	// leak running LSP subprocesses (the prior pool's ctx-cancel goroutine only
	// fires on daemon shutdown, not on re-init / hot reload).
	if globalPool != nil {
		globalPool.Shutdown()
	}

	var merged []LanguageConfig
	for _, p := range settingsPaths {
		if p == "" {
			continue
		}
		langs := loadConfigsFromPath(p)
		for _, pl := range langs {
			found := false
			for i, hl := range merged {
				if hl.Name == pl.Name {
					merged[i] = pl
					found = true
					break
				}
			}
			if !found {
				merged = append(merged, pl)
			}
		}
	}

	// Build configs map (language name → ServerConfig), formatters map, and extMap (extension → language name)
	configs := make(map[string]*ServerConfig)
	formatters := make(map[string]*FormatterConfig)
	extMap := make(map[string]string)
	for _, lc := range merged {
		if lc.LSP != nil {
			configs[lc.Name] = lc.LSP
		}
		if lc.Formatter != nil {
			formatters[lc.Name] = lc.Formatter
		}
		for _, ext := range lc.Extensions {
			extMap[ext] = lc.Name
		}
	}

	LogInfo("LSP pool: %d language(s) configured", len(configs))
	for lang := range configs {
		LogInfo("LSP pool: %s → %s", lang, configs[lang].Command)
	}
	for lang := range formatters {
		LogInfo("LSP pool: formatter for %s → %s", lang, formatters[lang].Command)
	}

	p := &Pool{
		clients:    make(map[string]*Client),
		configs:    configs,
		formatters: formatters,
		extMap:     extMap,
		failedAt:   make(map[string]time.Time),
		rootDir:    rootDir,
		ctx:        ctx,
		done:       make(chan struct{}),
	}
	globalPool = p

	go func() {
		select {
		case <-ctx.Done():
			p.Shutdown()
		case <-p.done:
			// Pool replaced (Shutdown already ran); exit instead of parking
			// on the daemon-lifetime ctx forever.
		}
	}()
}

// GetPool returns the global pool (nil if not initialized).
func GetPool() *Pool {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()
	return globalPool
}

// ReloadPool rebuilds the global pool from the given language config paths,
// reusing the existing pool's context and root directory. It is a no-op if the
// pool has not been initialized yet (a later brain.init will pick up the new
// configs). Used by the daemon config watcher when config/languages.json
// changes on disk.
func ReloadPool(settingsPaths ...string) {
	globalPoolMu.Lock()
	prev := globalPool
	globalPoolMu.Unlock()
	if prev == nil {
		return
	}
	InitPool(prev.ctx, prev.rootDir, settingsPaths...)
}

// LanguageForExt returns the language name for a file extension (e.g. ".go" → "go").
// Returns "" if the extension is not found in the pool's extension map.
func (p *Pool) LanguageForExt(ext string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.extMap[ext]
}

// GetClient returns a running LSP client for the given language.
// Returns (nil, nil) if the language has no config.
func (p *Pool) GetClient(language string) (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check failure cooldown — allow retry after 30s
	if t, ok := p.failedAt[language]; ok {
		if time.Since(t) < failCooldown {
			return nil, nil
		}
		delete(p.failedAt, language)
	}

	// Return cached client if alive
	if c, ok := p.clients[language]; ok && c.ready {
		if c.Alive() {
			return c, nil
		}
		// Server died — remove stale client and restart below
		LogInfo("LSP pool: %s server died, restarting", language)
		delete(p.clients, language)
	}

	cfg, ok := p.configs[language]
	if !ok {
		return nil, nil
	}

	c, err := p.startClient(language, cfg)
	if err != nil {
		p.failedAt[language] = time.Now()
		return nil, err
	}
	return c, nil
}

// HasLSP returns whether a language has an LSP config entry.
func (p *Pool) HasLSP(language string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.configs[language]
	return ok
}

// GetFormatter returns the FormatterConfig for the given language, or nil if none is configured.
func (p *Pool) GetFormatter(language string) *FormatterConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.formatters[language]
}

// RootDir returns the project root directory this pool was initialized with.
func (p *Pool) RootDir() string {
	return p.rootDir
}

// ConfiguredLanguages returns the list of languages that have LSP configs.
func (p *Pool) ConfiguredLanguages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	langs := make([]string, 0, len(p.configs))
	for lang := range p.configs {
		langs = append(langs, lang)
	}
	return langs
}

// Shutdown closes all running LSP clients.
func (p *Pool) Shutdown() {
	p.closeOnce.Do(func() { close(p.done) })

	p.mu.Lock()
	defer p.mu.Unlock()

	for lang, c := range p.clients {
		LogInfo("LSP pool: shutting down %s", lang)
		c.Close()
	}
	p.clients = make(map[string]*Client)
}

func (p *Pool) startClient(language string, cfg *ServerConfig) (*Client, error) {
	LogInfo("LSP pool: starting %s (%s %v)", language, cfg.Command, cfg.Args)

	c, err := NewClient(p.ctx, language, p.rootDir, cfg.Command, cfg.Args...)
	if err != nil {
		return nil, err
	}

	if err := c.Initialize(); err != nil {
		c.Close()
		return nil, err
	}

	p.clients[language] = c
	LogInfo("LSP pool: %s ready", language)
	return c, nil
}

type configFile struct {
	Languages []LanguageConfig `json:"languages"`
}

// LoadLanguageConfigs reads a settings.json file and returns its language configs.
// Returns nil on any error.
func LoadLanguageConfigs(settingsPath string) []LanguageConfig {
	return loadConfigsFromPath(settingsPath)
}

// loadConfigsFromPath reads a settings.json file at the given path and returns the language configs.
// Returns an empty slice on any error.
func loadConfigsFromPath(settingsPath string) []LanguageConfig {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil
	}

	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		LogError("LSP pool: failed to parse %s: %v", settingsPath, err)
		return nil
	}

	return cfg.Languages
}
