// Command perftool orchestrates vix's performance benchmarks. It is the single
// entry point behind the perf Makefile targets:
//
//	perftool gen-corpus   generate/validate the on-disk benchmark corpora
//	perftool run          run benchmarks, write perf/results/<commit>.txt, compare (no commit)
//	perftool baseline     run benchmarks and write perf/results/baseline.txt (commit it yourself)
//	perftool smoke        run every benchmark once (-benchtime=1x) to guard against breakage
//	perftool gate         verify perf/results/<HEAD>.txt exists and the tree is clean; exit 1 if not
//	perftool record       commit new perf/results/*.txt files (used by `make release`)
//
// The reusable, unit-tested logic (corpus shapes, the release gate, the
// previous-result picker) lives in internal/perf; this file is the git /
// go-test / benchstat glue around it.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/get-vix/vix/internal/perf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	root, err := repoRoot()
	if err != nil {
		fatal("locate repo root: %v", err)
	}
	resultsDir := filepath.Join(root, "perf", "results")
	corpusDir := filepath.Join(root, "perf", "corpus")

	switch os.Args[1] {
	case "gen-corpus":
		cmdGenCorpus(corpusDir)
	case "run":
		cmdRun(root, resultsDir, corpusDir, false)
	case "baseline":
		cmdRun(root, resultsDir, corpusDir, true)
	case "smoke":
		cmdSmoke(root)
	case "gate":
		cmdGate(resultsDir)
	case "record":
		cmdRecord(root, resultsDir)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

const usage = `usage: perftool <gen-corpus|run|baseline|smoke|gate|record>`

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "perftool: "+format+"\n", a...)
	os.Exit(1)
}

// --- subcommands ---

func cmdGenCorpus(corpusDir string) {
	huge := os.Getenv("PERF_HUGE") == "1"
	bigBytes := 0
	if mb, err := strconv.Atoi(os.Getenv("PERF_BIG_MB")); err == nil && mb > 0 {
		bigBytes = mb << 20
	}
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		fatal("mkdir corpus: %v", err)
	}
	for _, spec := range perf.DefaultCorpora(huge, bigBytes) {
		fmt.Printf("==> corpus %-6s (%d files) ... ", spec.Name, spec.NumFiles)
		n, err := perf.GenerateCorpus(corpusDir, spec)
		if err != nil {
			fmt.Println("FAILED")
			fatal("generate %s: %v", spec.Name, err)
		}
		if n == 0 {
			fmt.Println("up to date")
		} else {
			fmt.Printf("wrote %d files\n", n)
		}
	}
	fmt.Printf("corpus root: %s\n", corpusDir)
}

func cmdRun(root, resultsDir, corpusDir string, baseline bool) {
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		fatal("mkdir results: %v", err)
	}
	commit := gitShort(root)
	name := perf.ResultFileName(commit)
	if baseline {
		name = "baseline.txt"
	}
	out := filepath.Join(resultsDir, name)

	fmt.Printf("==> running benchmarks (count=%s) into %s\n", benchCount(), rel(root, out))
	data, err := runBenchmarks(root, corpusDir, benchArgs()...)
	if err != nil {
		// Persist whatever we captured to aid debugging, then fail.
		_ = os.WriteFile(out+".partial", data, 0o644)
		fatal("benchmarks failed: %v (partial output in %s.partial)", err, rel(root, out))
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fatal("write results: %v", err)
	}

	if baseline {
		fmt.Printf("\nBaseline written to %s — commit it as the frozen reference.\n", rel(root, out))
		return
	}
	compare(root, resultsDir, out, commit)
	fmt.Printf("\nResults written to %s (not committed — `make release` records it).\n", rel(root, out))
}

func cmdSmoke(root string) {
	fmt.Println("==> smoke: running every benchmark once (-benchtime=1x)")
	args := append([]string{"test", "-run=^$", "-bench=.", "-benchmem", "-benchtime=1x"}, perf.BenchPackages...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatal("smoke run failed: %v", err)
	}
	fmt.Println("smoke OK")
}

func cmdGate(resultsDir string) {
	root := filepath.Dir(filepath.Dir(resultsDir))
	res := perf.Gate(resultsDir, gitShort(root), treeDirty(root))
	if !res.OK {
		fatal("release gate: %s", res.Reason)
	}
	fmt.Printf("release gate: %s\n", res.Reason)
}

func cmdRecord(root, resultsDir string) {
	// Stage any new/changed result files (the frozen baseline is committed
	// separately by the maintainer; per-commit result files are added here).
	if _, err := git(root, "add", resultsDir); err != nil {
		fatal("git add results: %v", err)
	}
	staged, err := git(root, "diff", "--cached", "--name-only", "--", resultsDir)
	if err != nil {
		fatal("git diff --cached: %v", err)
	}
	if strings.TrimSpace(staged) == "" {
		fmt.Println("record: no new perf results to commit")
		return
	}
	msg := fmt.Sprintf("perf: record benchmark results for %s\n\nCo-authored-by: vix <290354907+vix-agent@users.noreply.github.com>", gitShort(root))
	if _, err := git(root, "commit", "-m", msg); err != nil {
		fatal("git commit results: %v", err)
	}
	fmt.Printf("record: committed perf results\n%s", staged)
}

// --- benchmark execution ---

func benchCount() string {
	if c := os.Getenv("COUNT"); c != "" {
		return c
	}
	return "10"
}

func benchArgs() []string {
	args := []string{"test", "-run=^$", "-bench=.", "-benchmem", "-count=" + benchCount()}
	return append(args, perf.BenchPackages...)
}

// runBenchmarks runs `go test -bench` for the benchmark packages and returns
// the captured stdout, with stray stdlib-log lines filtered out (go test funnels
// the inner test binary's stderr onto its own stdout, so a defensive filter
// keeps the committed results benchstat-parseable). The corpus root is exported
// so the disk benchmarks find their fixtures.
func runBenchmarks(root, corpusDir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), perf.CorpusEnv+"="+corpusDir)
	cmd.Stderr = os.Stderr // stream failures/logs live; keep stdout for the file
	out, err := cmd.Output()
	return filterLogLines(out), err
}

// logLineRe matches the default stdlib log prefix ("2006/01/02 15:04:05 ").
var logLineRe = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// filterLogLines drops any stdlib-log lines that leaked into the benchmark
// output so the result file stays clean for benchstat.
func filterLogLines(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if logLineRe.MatchString(ln) {
			continue
		}
		kept = append(kept, ln)
	}
	return []byte(strings.Join(kept, "\n"))
}

// compare prints a benchstat comparison of the current result against the
// frozen baseline and the previous committed result (whichever exist).
func compare(root, resultsDir, current, commit string) {
	if _, err := exec.LookPath("benchstat"); err != nil {
		fmt.Println("\n(benchstat not found — install with `go install golang.org/x/perf/cmd/benchstat@latest` for a delta report)")
		return
	}
	var files []string
	if baseline := filepath.Join(resultsDir, "baseline.txt"); fileExists(baseline) {
		files = append(files, baseline)
	}
	if prev := perf.PreviousResultFile(committedResultsNewestFirst(root, resultsDir), commit); prev != "" {
		if !contains(files, prev) {
			files = append(files, prev)
		}
	}
	files = append(files, current)
	if len(files) < 2 {
		fmt.Println("\n(no baseline or previous result yet — nothing to compare against)")
		return
	}
	fmt.Printf("\n==> benchstat %s\n", strings.Join(relAll(root, files), " "))
	cmd := exec.Command("benchstat", files...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// committedResultsNewestFirst lists per-commit result files tracked in git,
// newest first by the commit that introduced them (falling back to file mtime
// when git history is unavailable).
func committedResultsNewestFirst(root, resultsDir string) []string {
	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		return nil
	}
	type dated struct {
		path string
		when int64
	}
	var out []dated
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		p := filepath.Join(resultsDir, e.Name())
		when := int64(0)
		if ts, err := git(root, "log", "-1", "--format=%ct", "--", p); err == nil && strings.TrimSpace(ts) != "" {
			when, _ = strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
		}
		if when == 0 {
			if info, err := e.Info(); err == nil {
				when = info.ModTime().Unix()
			}
		}
		out = append(out, dated{p, when})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].when > out[j].when })
	paths := make([]string, len(out))
	for i, d := range out {
		paths[i] = d.path
	}
	return paths
}

// --- git / fs helpers ---

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from working directory")
		}
		dir = parent
	}
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	return string(out), err
}

func gitShort(root string) string {
	out, err := git(root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// treeDirty reports whether tracked files have uncommitted changes. Untracked
// files (including the just-written perf/results/<commit>.txt and the gitignored
// corpus) are intentionally ignored — they don't change the code being released.
func treeDirty(root string) bool {
	out, err := git(root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

func relAll(root string, ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = rel(root, p)
	}
	return out
}
