// Package perf holds the pure, unit-testable helpers behind vix's performance
// benchmark tooling: synthetic on-disk corpus generation and the release-time
// gate that verifies the current commit was benchmarked before a release.
//
// The orchestration (running `go test -bench`, invoking git and benchstat)
// lives in cmd/perftool; everything here is side-effect-light and covered by
// unit tests so the release gate and corpus shapes can't silently drift.
package perf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

// BenchPackages is the set of packages that carry Benchmark* functions. Both
// `make test-perf` and the smoke run drive exactly these.
var BenchPackages = []string{
	"./internal/daemon",
	"./internal/daemon/brain",
	"./internal/daemon/llm",
}

// CorpusEnv is the environment variable the disk benchmarks read to locate the
// generated corpus root. When unset (or the corpus is missing) those
// benchmarks skip themselves rather than fabricate data.
const CorpusEnv = "VIX_PERF_CORPUS"

// CorpusSpec describes one synthetic on-disk corpus for the disk benchmarks.
type CorpusSpec struct {
	Name     string   // subdirectory under the corpus root
	NumFiles int      // number of source files to materialize
	FileSize int      // approximate bytes per generated file
	Exts     []string // file extensions cycled through (e.g. ".go", ".py")
}

// DefaultCorpora returns the standard benchmark corpora. bigFileBytes sizes each
// file in the "big" corpus (10 large files). When huge is true the "many"
// corpus is scaled to 1,000,000 tiny files — opt-in, since generating it is
// minutes of I/O and gigabytes on disk.
func DefaultCorpora(huge bool, bigFileBytes int) []CorpusSpec {
	if bigFileBytes <= 0 {
		bigFileBytes = 20 << 20 // 20 MiB default
	}
	many := 100_000
	if huge {
		many = 1_000_000
	}
	return []CorpusSpec{
		{Name: "small", NumFiles: 1000, FileSize: 2 << 10, Exts: []string{".go", ".py", ".js", ".ts"}},
		{Name: "big", NumFiles: 10, FileSize: bigFileBytes, Exts: []string{".go"}},
		{Name: "many", NumFiles: many, FileSize: 200, Exts: []string{".go"}},
	}
}

// signature is a stable fingerprint of a spec; a corpus is regenerated only
// when its spec changes.
func (s CorpusSpec) signature() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s", s.Name, s.NumFiles, s.FileSize, strings.Join(s.Exts, ","))))
	return hex.EncodeToString(h[:8])
}

const corpusMarker = ".corpus-done"

// GenerateCorpus materializes spec under root/<spec.Name>. It is idempotent: a
// matching marker from a previous run short-circuits with written==0. A spec
// change (or a missing marker) triggers a clean regeneration. Files are fanned
// across up to 256 subdirectories so a single directory never holds the whole
// corpus. Content is deterministic (seeded per corpus name) so runs compare.
func GenerateCorpus(root string, spec CorpusSpec) (written int, err error) {
	if spec.NumFiles <= 0 || len(spec.Exts) == 0 {
		return 0, fmt.Errorf("perf: invalid corpus spec %q", spec.Name)
	}
	dir := filepath.Join(root, spec.Name)
	marker := filepath.Join(dir, corpusMarker)
	sig := spec.signature()
	if b, rerr := os.ReadFile(marker); rerr == nil && strings.TrimSpace(string(b)) == sig {
		return 0, nil
	}
	if err = os.RemoveAll(dir); err != nil {
		return 0, err
	}

	subdirs := spec.NumFiles
	if subdirs > 256 {
		subdirs = 256
	}
	for i := 0; i < subdirs; i++ {
		if err = os.MkdirAll(filepath.Join(dir, fmt.Sprintf("pkg%03d", i)), 0o755); err != nil {
			return written, err
		}
	}

	rng := rand.New(rand.NewSource(int64(spec.signatureSeed())))
	for i := 0; i < spec.NumFiles; i++ {
		sub := fmt.Sprintf("pkg%03d", i%subdirs)
		ext := spec.Exts[i%len(spec.Exts)]
		name := fmt.Sprintf("file_%08d%s", i, ext)
		path := filepath.Join(dir, sub, name)
		if err = os.WriteFile(path, genContent(rng, spec.FileSize, ext), 0o644); err != nil {
			return written, err
		}
		written++
	}
	if err = os.WriteFile(marker, []byte(sig), 0o644); err != nil {
		return written, err
	}
	return written, nil
}

func (s CorpusSpec) signatureSeed() uint32 {
	h := sha256.Sum256([]byte(s.Name))
	return uint32(h[0]) | uint32(h[1])<<8 | uint32(h[2])<<16 | uint32(h[3])<<24
}

// genContent builds ~size bytes of plausible, non-binary source text for the
// given extension, including a few import lines so the import-graph benchmark
// has real material when it runs over the corpus.
func genContent(rng *rand.Rand, size int, ext string) []byte {
	var b strings.Builder
	b.Grow(size + 64)
	switch ext {
	case ".py":
		b.WriteString("import os\nimport sys\nfrom collections import defaultdict\n\n")
	case ".js", ".ts":
		b.WriteString("import fs from 'fs';\nimport path from 'path';\n\n")
	default: // .go and everything else
		b.WriteString("package pkg\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\n")
	}
	line := "\tx := strings.Repeat(fmt.Sprintf(\"%d\", 42), 3) // synthetic benchmark line\n"
	for b.Len() < size {
		b.WriteString(line)
		if rng.Intn(16) == 0 {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

// ResultFileName returns the results filename for a commit hash.
func ResultFileName(commit string) string { return commit + ".txt" }

// GateResult reports whether a release may proceed and why.
type GateResult struct {
	OK     bool
	Reason string
}

// Gate decides whether `make release` may proceed: the working tree must be
// clean (tracked files) and a results file for headCommit must already exist on
// disk (i.e. the developer ran `make test-perf` on this commit). It never
// touches git or the network — callers pass headCommit and treeDirty in.
func Gate(resultsDir, headCommit string, treeDirty bool) GateResult {
	if strings.TrimSpace(headCommit) == "" {
		return GateResult{false, "could not determine HEAD commit"}
	}
	if treeDirty {
		return GateResult{false, "working tree has uncommitted (tracked) changes — commit them, then run `make test-perf`"}
	}
	p := filepath.Join(resultsDir, ResultFileName(headCommit))
	if _, err := os.Stat(p); err != nil {
		return GateResult{false, fmt.Sprintf("no perf results for commit %s (%s) — run `make test-perf` on this commit first", headCommit, p)}
	}
	return GateResult{true, fmt.Sprintf("perf results present for commit %s", headCommit)}
}

// PreviousResultFile picks the most-recent prior result from candidates (ordered
// newest-first, e.g. by git commit date), skipping the current commit's file
// and the frozen baseline. Returns "" when there is no prior result. Paths are
// returned verbatim from the input.
func PreviousResultFile(candidatesNewestFirst []string, currentCommit string) string {
	cur := ResultFileName(currentCommit)
	for _, c := range candidatesNewestFirst {
		base := filepath.Base(c)
		if base == cur || base == "baseline.txt" {
			continue
		}
		return c
	}
	return ""
}
