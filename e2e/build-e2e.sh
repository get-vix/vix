#!/usr/bin/env bash
# build-e2e.sh — cross-compile the e2e test binary and report CLI for Linux,
# mirroring script/build.sh's "build on host, copy into the container" model.
#
# These are static (CGO_ENABLED=0) Linux binaries the Dockerfile COPYs in, so
# the runtime image needs no Go toolchain. The vix/vixd product binaries come
# separately from script/build.sh.
#
# Usage:
#   ./e2e/build-e2e.sh [amd64|arm64]    # default: host GOARCH
#
# Output:
#   e2e/bin/e2e.test    (compiled `go test -c ./scenarios`)
#   e2e/bin/report      (the report CLI)
#   e2e/bin/daz-secrets + e2e/bin/vix-e2e-secrets--ok (real process protocol)

set -euo pipefail

E2E_DIR="$(cd "$(dirname "$0")" && pwd)"
ARCH="${1:-$(go env GOARCH)}"

mkdir -p "$E2E_DIR/bin"

echo "==> Building e2e binaries for linux/${ARCH}"

DAZ_SECRETS_DIR="$(go list -mod=mod -m -f '{{.Dir}}' github.com/darrenoakey/daz-secrets)"

# -C enters the e2e module; -o paths are relative to it, so they land in e2e/bin.
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go test -C "$E2E_DIR" -c -o bin/e2e.test ./scenarios

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -C "$E2E_DIR" -trimpath -o bin/report ./cmd/report

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -C "$DAZ_SECRETS_DIR" -trimpath -o "$E2E_DIR/bin/daz-secrets" \
    ./cmd/daz-secrets

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -C "$DAZ_SECRETS_DIR" -trimpath -o "$E2E_DIR/bin/vix-e2e-secrets--ok" \
    ./cmd/daz-secrets-conformance-provider

echo "==> e2e binaries → $E2E_DIR/bin (e2e.test, report, daz-secrets, vix-e2e-secrets--ok)"
