package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/get-vix/vix/internal/config"
)

// --- Interfaces ---

type grepRunner interface {
	Run(ctx context.Context, pattern, path, include, cwd string) (string, error)
}

type globRunner interface {
	Run(ctx context.Context, patterns, paths []string, cwd, typeFilter string, includeHidden bool, maxResults int) (string, error)
}

// errGlobMaxReached is returned from the builtin walker callback to stop
// traversal as soon as the user-supplied max_results cap is hit. It's swallowed
// by the Run method and reported as a capped-result to the caller — the
// walker does not bubble it up as a real error.
var errGlobMaxReached = fmt.Errorf("glob max_results reached")

// --- Grep backends ---

type systemGrepBackend struct{}

func (b *systemGrepBackend) Run(ctx context.Context, pattern, path, include, cwd string) (string, error) {
	LogInfo("[tool.grep] backend=grep cwd=%s pattern=%s path=%s include=%s", cwd, pattern, path, include)
	args := []string{"-rn", "-E"} // -E: extended regex (enables | + etc.); GNU grep follows symlinks by default
	if include != "" {
		args = append(args, fmt.Sprintf("--include=%s", include))
	}
	args = append(args, pattern)
	if path != "" {
		args = append(args, path)
	} else {
		args = append(args, ".")
	}

	cmd := exec.CommandContext(ctx, "grep", args...)
	cmd.Dir = cwd
	LogInfo("[tool.grep] exec: grep %s", strings.Join(args, " "))

	output, _ := cmd.CombinedOutput()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result := string(output)
	if len(result) > maxOutput {
		result = result[:maxOutput] + fmt.Sprintf("\n... (truncated at %d chars)", maxOutput)
	}
	if result == "" {
		result = "(no matches)"
	}
	return result, nil
}

type rgBackend struct{}

func (b *rgBackend) Run(ctx context.Context, pattern, path, include, cwd string) (string, error) {
	LogInfo("[tool.grep] backend=rg cwd=%s pattern=%s path=%s include=%s", cwd, pattern, path, include)
	args := []string{"-n", "--follow"} // --follow: follow symlinks
	if include != "" {
		args = append(args, fmt.Sprintf("--glob=%s", include))
	}
	args = append(args, pattern)
	if path != "" {
		args = append(args, path)
	} else {
		args = append(args, ".")
	}

	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = cwd
	LogInfo("[tool.grep] exec: rg %s", strings.Join(args, " "))

	output, _ := cmd.CombinedOutput()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result := string(output)
	if len(result) > maxOutput {
		result = result[:maxOutput] + fmt.Sprintf("\n... (truncated at %d chars)", maxOutput)
	}
	if result == "" {
		result = "(no matches)"
	}
	return result, nil
}

// --- Glob backends ---

// toStringList normalizes a JSON-decoded value into []string. It accepts a
// single string, a []any of strings, or a []string. Empty strings are dropped.
// Returns nil if the value is absent or of an unsupported type.
func toStringList(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(x))
		for _, s := range x {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

type builtinGlobBackend struct{}

func (b *builtinGlobBackend) Run(ctx context.Context, patterns, paths []string, cwd, typeFilter string, includeHidden bool, maxResults int) (string, error) {
	LogInfo("[tool.glob] backend=builtin cwd=%s patterns=%v paths=%v type=%s include_hidden=%v max_results=%d", cwd, patterns, paths, typeFilter, includeHidden, maxResults)
	if len(paths) == 0 {
		paths = []string{cwd}
	}

	// Track the originating base per match so hidden-detection stays accurate
	// when multiple search roots are provided. First-seen base wins.
	seenBase := make(map[string]string)
	capped := false
outer:
	for _, base := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fsys := os.DirFS(base)
		for _, pat := range patterns {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			err := doublestar.GlobWalk(fsys, pat, func(p string, d fs.DirEntry) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if len(seenBase) >= maxResults {
					return errGlobMaxReached
				}
				full := filepath.Join(base, filepath.FromSlash(p))
				if _, ok := seenBase[full]; !ok {
					seenBase[full] = base
				}
				return nil
			})
			if err != nil {
				if err == errGlobMaxReached {
					capped = true
					break outer
				}
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", err
			}
		}
	}

	matches := make([]string, 0, len(seenBase))
	for m := range seenBase {
		matches = append(matches, m)
	}

	if typeFilter == "f" || typeFilter == "d" {
		filtered := matches[:0]
		for _, m := range matches {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			info, statErr := os.Lstat(m)
			if statErr != nil {
				continue
			}
			isDir := info.IsDir()
			if (typeFilter == "f" && !isDir) || (typeFilter == "d" && isDir) {
				filtered = append(filtered, m)
			}
		}
		matches = filtered
	}

	if !includeHidden {
		filtered := matches[:0]
		for _, m := range matches {
			rel, relErr := filepath.Rel(seenBase[m], m)
			if relErr != nil {
				continue
			}
			hidden := false
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				if len(part) > 1 && part[0] == '.' {
					hidden = true
					break
				}
			}
			if !hidden {
				filtered = append(filtered, m)
			}
		}
		matches = filtered
	}

	sort.Strings(matches)
	if len(matches) > maxResults {
		matches = matches[:maxResults]
		capped = true
	}

	if len(matches) == 0 {
		return "(no matches)", nil
	}
	result := strings.Join(matches, "\n")
	if capped {
		result += fmt.Sprintf("\n... (capped at %d results — raise max_results or narrow the pattern if more are needed)", maxResults)
	}
	return result, nil
}

type fdGlobBackend struct{}

func (b *fdGlobBackend) Run(ctx context.Context, patterns, paths []string, cwd, typeFilter string, includeHidden bool, maxResults int) (string, error) {
	LogInfo("[tool.glob] backend=fd cwd=%s patterns=%v paths=%v type=%s include_hidden=%v max_results=%d", cwd, patterns, paths, typeFilter, includeHidden, maxResults)
	searchPaths := paths
	if len(searchPaths) == 0 {
		searchPaths = []string{"."}
	}

	// fd accepts a single pattern argument with any number of trailing search
	// paths. To union multiple patterns, invoke fd once per pattern and merge
	// results via a dedup map.
	seen := make(map[string]struct{})
	for _, pat := range patterns {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(seen) >= maxResults {
			break
		}
		args := []string{"--glob", "--follow", "--max-results", fmt.Sprintf("%d", maxResults)} // --follow: follow symlinks
		if includeHidden {
			args = append(args, "--hidden")
		}
		switch typeFilter {
		case "f":
			args = append(args, "--type", "f")
		case "d":
			args = append(args, "--type", "d")
		}
		args = append(args, pat)
		args = append(args, searchPaths...)

		cmd := exec.CommandContext(ctx, "fd", args...)
		cmd.Dir = cwd
		output, _ := cmd.CombinedOutput()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
			if line != "" {
				seen[line] = struct{}{}
			}
		}
	}

	lines := make([]string, 0, len(seen))
	for l := range seen {
		lines = append(lines, l)
	}
	sort.Strings(lines)
	capped := false
	if len(lines) > maxResults {
		lines = lines[:maxResults]
		capped = true
	}
	if len(lines) == 0 {
		return "(no matches)", nil
	}
	result := strings.Join(lines, "\n")
	if capped {
		result += fmt.Sprintf("\n... (capped at %d results — raise max_results or narrow the pattern if more are needed)", maxResults)
	}
	if len(result) > maxOutput {
		result = result[:maxOutput] + fmt.Sprintf("\n... (truncated at %d chars)", maxOutput)
	}
	return result, nil
}

// --- Factory functions ---

func logToolFound(name string)    { log.Printf("\033[32m[tools] ✓ %s found\033[0m", name) }
func logToolMissing(name string)  { log.Printf("\033[31m[tools] ✗ %s not found in PATH\033[0m", name) }

func newGrepRunner(backend string) grepRunner {
	switch backend {
	case "rg":
		if _, err := exec.LookPath("rg"); err != nil {
			logToolMissing("rg (ripgrep)")
			LogWarn("falling back to system grep")
			return &systemGrepBackend{}
		}
		logToolFound("rg (ripgrep)")
		return &rgBackend{}
	default:
		return &systemGrepBackend{}
	}
}

func newGlobRunner(backend string) globRunner {
	switch backend {
	case "fd":
		if _, err := exec.LookPath("fd"); err != nil {
			logToolMissing("fd")
			LogWarn("falling back to builtin glob")
			return &builtinGlobBackend{}
		}
		logToolFound("fd")
		return &fdGlobBackend{}
	default:
		return &builtinGlobBackend{}
	}
}

// --- Config loader ---

// toolsConfigFile is the structure for loading the tools section from settings.json.
type toolsConfigFile struct {
	Tools config.ToolsConfig `json:"tools"`
}

// loadToolsConfig merges tools config from the given settings.json paths.
// Later entries override earlier ones.
func loadToolsConfig(settingsPaths []string) config.ToolsConfig {
	var result config.ToolsConfig

	for _, configPath := range settingsPaths {
		if configPath == "" {
			continue
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		var cf toolsConfigFile
		if err := json.Unmarshal(data, &cf); err != nil {
			LogWarn("Failed to parse tools config from %s: %v", configPath, err)
			continue
		}
		// Override with non-empty values
		if cf.Tools.Grep.Backend != "" {
			result.Grep = cf.Tools.Grep
		}
		if cf.Tools.Glob.Backend != "" {
			result.Glob = cf.Tools.Glob
		}
	}

	return result
}
