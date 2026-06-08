package config

import (
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

//go:embed defaults/*
var defaultFiles embed.FS

// BootstrapHomeVixDir writes default config, agent, and prompt files into
// homeVixDir when settings.json is absent (first run). Existing files are
// never overwritten.
//
// The split config files under config/ (workflow.json, languages.json) are
// always ensured, even when settings.json already exists, so users upgrading
// from a build that stored workflows/languages inline in settings.json still
// receive the defaults instead of silently losing those features.
func BootstrapHomeVixDir(homeVixDir string) error {
	if err := ensureConfigDefaults(homeVixDir); err != nil {
		log.Printf("[config] bootstrap: failed to seed config defaults: %v", err)
	}

	configPath := filepath.Join(homeVixDir, "settings.json")
	if _, err := os.Stat(configPath); err == nil {
		return nil // already bootstrapped
	}

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
