// Module github.com/get-vix/vix/e2e is the containerised end-to-end test
// suite for vix. It is a SEPARATE Go module on purpose: a nested module is
// excluded from the parent's `./...`, so `go test ./...` / `make test` at the
// repo root never compile or run these tests. The only entry points are
// `make test-e2e` (the documented pre-release gate) and, for local dev,
// `cd e2e && VIX_E2E=1 go test ./...` with prebuilt vix/vixd binaries present.
module github.com/get-vix/vix/e2e

go 1.26

require github.com/alecthomas/chroma/v2 v2.26.1

require (
	github.com/dlclark/regexp2/v2 v2.1.1 // indirect
	golang.org/x/net v0.56.0 // indirect
)
