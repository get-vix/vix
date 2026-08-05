package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/get-vix/vix/internal/config"
)

// PluginConfig type lives in package llm (see llm.go) — this file only
// hosts the discovery/exec/merge logic and the SDK header-strip middleware.

// NewPluginSource returns a PluginSource that discovers and runs the
// executable plugin files in dirs every time an LLM client is built. Each
// plugin receives the target provider, model, and credential metadata (never
// the secret itself) as a JSON object on stdin — {"version", "model",
// "provider", "credential_source"} — so a plugin can emit "{}" for
// providers/credentials it does not target.
func NewPluginSource(dirs []string, version string) PluginSource {
	return func(provider, model string, cred config.Credential) PluginConfig {
		return loadPlugins(dirs, version, provider, model, cred)
	}
}

// loadPlugins discovers and runs all executable plugin files found in dirs,
// merges their output, and returns the combined PluginConfig.
// Errors (non-zero exit, timeout, invalid JSON) are logged and skipped.
func loadPlugins(dirs []string, version, provider, model string, cred config.Credential) PluginConfig {
	input, _ := json.Marshal(map[string]string{
		"version":           version,
		"model":             model,
		"provider":          provider,
		"credential_source": string(cred.Source),
	})

	var merged PluginConfig
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Dir doesn't exist or can't be read — skip silently.
			continue
		}
		// Sort for deterministic order.
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, e := range entries {
			name := e.Name()
			// Skip hidden and disabled entries.
			if name[0] == '.' || name[0] == '_' {
				continue
			}
			if e.IsDir() {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			// Skip non-executable files.
			if fi.Mode()&0o111 == 0 {
				continue
			}
			path := dir + "/" + name
			result, err := runPlugin(path, input)
			if err != nil {
				log.Printf("[vixd] plugin %s failed: %v", path, err)
				continue
			}
			mergePluginConfig(&merged, result)
			log.Printf("[vixd] plugin loaded: %s (provider=%s cred=%s)", path, provider, cred.Source)
		}
	}
	return merged
}

// runPlugin executes the plugin at path, writes input to its stdin, and
// returns the parsed PluginConfig from its stdout. Enforces a 5-second timeout.
func runPlugin(path string, input []byte) (PluginConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// #nosec G204 — path comes from the user's own .vix/plugins/ directory.
	cmd := exec.CommandContext(ctx, path) //nolint:gosec
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		return PluginConfig{}, err
	}
	if len(out) == 0 {
		return PluginConfig{}, nil
	}
	var cfg PluginConfig
	if err := json.Unmarshal(out, &cfg); err != nil {
		return PluginConfig{}, err
	}
	return cfg, nil
}

// mergePluginConfig merges src into dst (last-writer-wins for headers,
// newline-join for system_prefix).
func mergePluginConfig(dst *PluginConfig, src PluginConfig) {
	for k, v := range src.Headers {
		if dst.Headers == nil {
			dst.Headers = make(map[string]*string)
		}
		dst.Headers[k] = v
	}
	if src.SystemPrefix != "" {
		if dst.SystemPrefix != "" {
			dst.SystemPrefix += "\n" + src.SystemPrefix
		} else {
			dst.SystemPrefix = src.SystemPrefix
		}
	}
}

// stripHeadersMiddleware returns an SDK middleware that removes the named
// headers from every outgoing HTTP request. Header names are canonicalized
// (e.g. "x-api-key" → "X-Api-Key") so the delete is case-insensitive.
func stripHeadersMiddleware(names []string) option.Middleware {
	canonical := make([]string, len(names))
	for i, n := range names {
		canonical[i] = http.CanonicalHeaderKey(n)
	}
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		for _, h := range canonical {
			req.Header.Del(h)
		}
		return next(req)
	}
}
