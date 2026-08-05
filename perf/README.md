# Performance benchmarks

Go `testing.B` benchmarks for vix's hot paths, plus the tooling that records a
result per commit and compares releases with
[`benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat).

The model is always stubbed (an `httptest` server or `fakeCompactionLLM`), so
benchmarks never hit the network. Disk work runs against **real** isolated temp
dirs and generated corpora — not an in-memory FS — so nothing extra ships in the
production binaries.

## Layout

```
perf/
  results/
    baseline.txt        # frozen first-run reference (committed once)
    <short-commit>.txt   # one result file per benchmarked commit
  corpus/               # generated fixtures (gitignored; see `make perf-corpus`)
```

## Benchmarks

| Benchmark | Package | Measures | Stub |
|-----------|---------|----------|------|
| `BenchmarkScanProject/{small,big,many}` | `internal/daemon/brain` | tree walk + read + hash | real corpus |
| `BenchmarkExtractFileImports` | `internal/daemon/brain` | import extraction (regex, in-memory) | none |
| `BenchmarkLLMStreamDecode` | `internal/daemon/llm` | SSE stream decode + message assembly | `httptest` server |
| `BenchmarkAgentTurn` | `internal/daemon` | turn scaffolding + record build + persist | `fakeCompactionLLM` + temp dir |
| `BenchmarkThreadStoreSaveLoad/{1,100,10000}` | `internal/daemon` | save + full list round trip | temp dir |
| `BenchmarkAccessStatsLog` / `BenchmarkAccessStatsTopFiles` | `internal/daemon` | SQLite insert / top-N query | in-memory SQLite |

## Workflow

```bash
make perf-corpus        # generate the on-disk corpora once (idempotent)
make test-perf          # run benchmarks, write results/<commit>.txt, print benchstat vs baseline + previous
                        #   (does NOT commit)
make perf-baseline      # run once and write results/baseline.txt — commit it as the frozen reference
make perf-smoke         # run every benchmark once (-benchtime=1x) as a fast breakage guard
```

`make test-perf` writes `results/<commit>.txt` for the current `HEAD`. That file
is the "this commit was benchmarked" marker: **`make release` refuses to proceed
until it exists** (and the tree is clean), then commits it as the release's
recorded result. Use `COUNT=N` to change the repetition count (default 10;
higher = stabler deltas, slower).

Corpus sizing knobs: `PERF_BIG_MB` (MiB per "big" file, default 20) and
`PERF_HUGE=1` (scale the "many" corpus to 1,000,000 files — slow, GBs on disk).

Install benchstat for the delta report:

```bash
go install golang.org/x/perf/cmd/benchstat@latest
```

The reusable logic (corpus shapes, the release gate, the previous-result picker)
lives in `internal/perf` and is unit-tested; the orchestration is
`cmd/perftool`.
