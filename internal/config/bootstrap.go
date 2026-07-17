package config

import (
	"bytes"
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

//go:embed defaults/*
var defaultFiles embed.FS

// managedDefaultFiles are the .vix-relative files that vix owns: on a version
// change they are overwritten with the embedded defaults (the previous copy is
// preserved as <name>.bak). Prompt and agent files are appended dynamically
// (managedTreeFiles) so shipped workflows always find the prompt/agent
// revisions they were written for. User state (the chosen chat model, update
// bookkeeping) lives in state.json, which is never managed.
var managedDefaultFiles = []string{
	"settings.json",
	"config/workflow.json",
	"config/languages.json",
	"skills/jobs/SKILL.md",
	"skills/vix-help/SKILL.md",
	"skills/vix-help/references/vix-manual.md",
}

// BootstrapHomeVixDir writes default config, agent, and prompt files into
// homeVixDir. version is the running binary's build version ("dev" for
// unreleased builds).
//
// Behaviour:
//   - First run (no settings.json): seed everything from the embedded
//     defaults and record the version in state.json.
//   - Version change (state.json defaults_version differs from version,
//     including a missing one): overwrite the managed files (settings.json,
//     config/*.json, prompts/**, agents/**) with the embedded defaults,
//     preserving each replaced file as <name>.bak, then record the version.
//   - Same version: only re-seed managed config files that went missing.
func BootstrapHomeVixDir(homeVixDir, version string) error {
	configPath := filepath.Join(homeVixDir, "settings.json")
	if _, err := os.Stat(configPath); err != nil {
		// First run: full seed, then stamp.
		if err := seedAllDefaults(homeVixDir); err != nil {
			return err
		}
		stampDefaultsVersion(homeVixDir, version)
		return nil
	}

	if marker := defaultsVersion(homeVixDir); marker != version {
		log.Printf("[config] defaults version %q -> %q: refreshing managed defaults in %s", marker, version, homeVixDir)
		if err := refreshManagedDefaults(homeVixDir); err != nil {
			log.Printf("[config] bootstrap: failed to refresh managed defaults: %v", err)
		} else {
			stampDefaultsVersion(homeVixDir, version)
		}
		return nil
	}

	// Same version: keep the absent-only safety net for the split config
	// files (e.g. a user deleted workflow.json to reset it).
	if err := ensureConfigDefaults(homeVixDir); err != nil {
		log.Printf("[config] bootstrap: failed to seed config defaults: %v", err)
	}
	return nil
}

// seedAllDefaults walks the embedded defaults tree and writes every file that
// does not already exist (first-run bootstrap).
func seedAllDefaults(homeVixDir string) error {
	return fs.WalkDir(defaultFiles, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Strip the "defaults/" prefix to get the target relative path.
		rel, _ := filepath.Rel("defaults", path)
		target := filepath.Join(homeVixDir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := defaultFiles.ReadFile(path)
		if err != nil {
			return err
		}

		// O_CREATE|O_EXCL: create only if it doesn't already exist.
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				return nil // skip existing files
			}
			return err
		}
		defer f.Close()

		if _, err := f.Write(data); err != nil {
			return err
		}

		log.Printf("[config] bootstrap: wrote %s", target)
		return nil
	})
}

// refreshManagedDefaults overwrites the managed files with the embedded
// defaults after a version change. Files whose content already matches the
// default are left untouched; replaced files are first copied to <name>.bak
// (clobbering any previous .bak).
func refreshManagedDefaults(homeVixDir string) error {
	files := append([]string(nil), managedDefaultFiles...)
	trees, err := managedTreeFiles()
	if err != nil {
		return err
	}
	files = append(files, trees...)

	for _, rel := range files {
		data, err := defaultFiles.ReadFile("defaults/" + rel)
		if err != nil {
			return err
		}
		target := filepath.Join(homeVixDir, filepath.FromSlash(rel))

		current, readErr := os.ReadFile(target)
		if readErr == nil && bytes.Equal(current, data) {
			continue // already up to date — no write, no .bak churn
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if readErr == nil {
			if err := os.WriteFile(target+".bak", current, 0o644); err != nil {
				return err
			}
		}
		if err := writeFileAtomic(target, data); err != nil {
			return err
		}
		log.Printf("[config] bootstrap: refreshed %s (previous saved as .bak)", target)
	}
	return nil
}

// managedTreeFiles lists every embedded defaults/prompts/** and
// defaults/agents/** file as a .vix-relative slash path. Both trees are fully
// vix-managed — agents carry no user state (the chat model choice lives in
// state.json) — so they are refreshed on version change like the rest of the
// managed defaults.
func managedTreeFiles() ([]string, error) {
	var out []string
	for _, root := range []string{"defaults/prompts", "defaults/agents"} {
		err := fs.WalkDir(defaultFiles, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			out = append(out, strings.TrimPrefix(path, "defaults/"))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// writeFileAtomic writes data via a temp file + rename so a crash mid-write
// never leaves a truncated config file.
func writeFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, target)
}

// defaultsVersion returns the defaults version recorded in <dir>/state.json,
// or "" when absent.
func defaultsVersion(homeVixDir string) string {
	return ReadState(filepath.Join(homeVixDir, "state.json")).DefaultsVersion
}

// stampDefaultsVersion records version in <dir>/state.json, preserving the
// other state fields. Best-effort: a failure only means the next startup
// re-runs the refresh.
func stampDefaultsVersion(homeVixDir, version string) {
	p := filepath.Join(homeVixDir, "state.json")
	st := ReadState(p)
	st.DefaultsVersion = version
	if err := WriteState(p, st); err != nil {
		log.Printf("[config] bootstrap: failed to write %s: %v", p, err)
	}
}

// ensureConfigDefaults writes the embedded config/ defaults (workflow.json,
// languages.json) into homeVixDir/config when they are absent. Unlike the
// first-run walk, this runs on every startup so an existing install that
// predates the split files still gets seeded. Existing files are never
// overwritten.
func ensureConfigDefaults(homeVixDir string) error {
	entries, err := defaultFiles.ReadDir("defaults/config")
	if err != nil {
		return err
	}
	dir := filepath.Join(homeVixDir, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := defaultFiles.ReadFile("defaults/config/" + e.Name())
		if err != nil {
			return err
		}
		target := filepath.Join(dir, e.Name())
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		f.Close()
		log.Printf("[config] bootstrap: wrote %s", target)
	}
	return nil
}
