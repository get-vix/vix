// Package artifact defines the on-disk schema the e2e harness emits per test
// and the report CLI consumes. Keeping it in its own package lets the harness
// (which runs inside `go test`) and the standalone `report` binary share one
// definition without importing each other.
//
// The contract is "tests never render; they append immutable, slug-keyed
// artifacts; one idempotent pass renders them all". Two files are written per
// test: a <slug>.start.json marker at test start, and a <slug>.json final
// record at test end. A start with no matching final is reported as a crash.
package artifact

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Status is a test's terminal state in the report.
type Status string

const (
	StatusRunning Status = "running" // start marker only; no final written yet
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
	StatusCrashed Status = "crashed" // synthesised by the renderer: start with no final
)

// Screenshot is one labelled capture taken during a test.
type Screenshot struct {
	Label   string `json:"label"`    // human label, e.g. "after write"
	Text    string `json:"text"`     // plain text screen grid (the assertion artifact)
	PNGPath string `json:"png_path"` // path, relative to the report root, to the rendered image
	Order   int    `json:"order"`    // capture order within the test
}

// Result is the full per-test record. The start marker is the same struct with
// Status=running and the dynamic fields (Screenshots, Diagnostics, DurationMS)
// left zero.
type Result struct {
	// Identity / navigation.
	Category    string `json:"category"`    // top-level group, e.g. "ui"
	Subcategory string `json:"subcategory"` // e.g. "ui.threads"
	Name        string `json:"name"`        // Go test func name
	Description string `json:"description"` // one-line, author-supplied
	Wire        string `json:"wire"`        // "messages" | "responses" | "chat_completions"
	Variant     string `json:"variant"`     // optional extra config discriminator

	// Source location + body for the collapsible implementation view.
	SourceFile string `json:"source_file"` // absolute path at author time (basename used by report)
	SourceLine int    `json:"source_line"`
	Source     string `json:"source,omitempty"` // the test function's Go source

	// Outcome.
	Status     Status `json:"status"`
	DurationMS int64  `json:"duration_ms"`

	// Evidence.
	Screenshots []Screenshot `json:"screenshots,omitempty"`
	Diagnostics string       `json:"diagnostics,omitempty"` // auto-dumped on failure/timeout
}

// slugRe strips anything that would be awkward in a path segment.
var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Slug is the stable, collision-free key for a test variant: it is reused
// across runs so a re-run overwrites in place, and is unique per
// wire/variant so parallel/sequential tests never clobber each other.
func (r Result) Slug() string {
	sub := r.Subcategory
	if sub == "" {
		sub = r.Category
	}
	base := slugRe.ReplaceAllString(r.Name, "_")
	disc := r.Wire
	if r.Variant != "" {
		disc += "_" + r.Variant
	}
	if disc != "" {
		base += "." + slugRe.ReplaceAllString(disc, "_")
	}
	// A short hash guards against two tests colliding after slug sanitisation.
	sum := sha1.Sum([]byte(r.Category + "\x00" + r.Subcategory + "\x00" + r.Name + "\x00" + disc))
	return slugRe.ReplaceAllString(sub, "_") + "/" + base + "-" + hex.EncodeToString(sum[:])[:8]
}

// ResultsDir / ImagesDir are the canonical subdirectories under the report root.
func ResultsDir(root string) string { return filepath.Join(root, "results") }
func ImagesDir(root string) string  { return filepath.Join(root, "images") }

// WriteStart writes the <slug>.start.json marker atomically.
func WriteStart(root string, r Result) error {
	r.Status = StatusRunning
	return writeJSON(filepath.Join(ResultsDir(root), r.Slug()+".start.json"), r)
}

// WriteFinal writes the <slug>.json final record atomically.
func WriteFinal(root string, r Result) error {
	return writeJSON(filepath.Join(ResultsDir(root), r.Slug()+".json"), r)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path) // atomic on the same filesystem
}

// Manifest is the run-level header written alongside results.
type Manifest struct {
	RunID     string `json:"run_id"`
	GitCommit string `json:"git_commit"`
	Timestamp string `json:"timestamp"`
	Shards    int    `json:"shards"`
}

// Load reads every final result plus any start-only (crashed) markers from a
// report root, defensively skipping unparsable files (the renderer is a
// filesystem boundary). Results are returned sorted by category/subcategory/name.
func Load(root string) ([]Result, error) {
	dir := ResultsDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	finals := map[string]Result{}
	starts := map[string]Result{}
	walk := func(d string) {
		_ = filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			var r Result
			if json.Unmarshal(data, &r) != nil {
				return nil // quarantine corrupt/partial files
			}
			if strings.HasSuffix(p, ".start.json") {
				starts[r.Slug()] = r
			} else if strings.HasSuffix(p, ".json") {
				finals[r.Slug()] = r
			}
			return nil
		})
	}
	_ = entries
	walk(dir)

	out := make([]Result, 0, len(finals)+len(starts))
	for slug, r := range finals {
		delete(starts, slug)
		out = append(out, r)
	}
	for _, r := range starts { // start with no final = crashed
		r.Status = StatusCrashed
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Subcategory != b.Subcategory {
			return a.Subcategory < b.Subcategory
		}
		return a.Name < b.Name
	})
	return out, nil
}
