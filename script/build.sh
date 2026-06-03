#!/usr/bin/env bash
# build.sh — compile vix + vixd for darwin-arm64 + linux-amd64 + linux-arm64
# and drop the loose binaries into ./bin/ (or -o <dir>).
#
# This is the single build entry point. Callers:
#   • script/release.sh       — runs this with --version, then hands off to publish.sh
#
# Tarballs, checksums, GPG signing, and Homebrew formula generation are NOT
# done here. They live in script/publish.sh, which reads from this script's
# output dir.
#
# Usage:
#   ./build.sh                       # version=dev, output=<repo>/bin
#   ./build.sh --version v0.2.0      # embed v0.2.0 into the binary (-X main.Version)
#   ./build.sh --force               # rebuild even if .build-commit matches HEAD
#   ./build.sh -o /tmp/vix-out       # override output dir
#
# Output:
#   <out>/vix-darwin-arm64    <out>/vixd-darwin-arm64
#   <out>/vix-linux-amd64     <out>/vixd-linux-amd64
#   <out>/vix-linux-arm64     <out>/vixd-linux-arm64
#   <out>/.build-commit       # git HEAD of the vix repo at build time

set -euo pipefail

# ── Parse args ────────────────────────────────────────────────────────────────
VERSION="dev"
FORCE=0
OUT_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      VERSION="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    -o)
      OUT_DIR="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '/^# Usage:/,/^$/p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *)
      echo "Error: unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
if [[ -z "$OUT_DIR" ]]; then
  OUT_DIR="$ROOT_DIR/bin"
fi

# ── Colors (disabled when stdout is not a tty) ───────────────────────────────
if [ -t 1 ]; then
  C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'
  C_BLUE=$'\033[34m'; C_GREEN=$'\033[32m'
  C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'; C_DIM=$'\033[2m'
else
  C_RESET=""; C_BOLD=""; C_BLUE=""; C_GREEN=""; C_YELLOW=""; C_RED=""; C_DIM=""
fi

# ── Dirty-tree check ─────────────────────────────────────────────────────────
# TTY: prompt to continue. non-TTY (make build / CI): warn-and-proceed so a
# non-interactive caller never blocks. --force does NOT bypass this — its job
# is the staleness check below.
if [[ -n "$(git -C "$ROOT_DIR" status --porcelain 2>/dev/null)" ]]; then
  if [ -t 0 ]; then
    echo "${C_YELLOW}!!${C_RESET} ${C_BOLD}$ROOT_DIR${C_RESET} has uncommitted changes:"
    git -C "$ROOT_DIR" status --short
    read -r -p "Continue anyway? [y/N] " ans
    if [[ ! "$ans" =~ ^[Yy]$ ]]; then
      echo "Aborted."
      exit 1
    fi
  else
    echo "${C_YELLOW}warning:${C_RESET} $ROOT_DIR has uncommitted changes, proceeding (non-interactive)." >&2
  fi
fi

CURRENT_COMMIT="$(git -C "$ROOT_DIR" rev-parse HEAD 2>/dev/null || echo "unknown")"
STAMP_FILE="$OUT_DIR/.build-commit"

# ── Staleness check ──────────────────────────────────────────────────────────
# Skip unless any of: --force, --version (changes embedded ldflag), missing
# binary, or commit moved. Passing --force forces a rebuild even when
# nothing changed.
if [[ $FORCE -eq 0 && "$VERSION" == "dev" \
    && -f "$STAMP_FILE" \
    && -f "$OUT_DIR/vix-darwin-arm64" && -f "$OUT_DIR/vixd-darwin-arm64" \
    && -f "$OUT_DIR/vix-linux-amd64"  && -f "$OUT_DIR/vixd-linux-amd64" \
    && -f "$OUT_DIR/vix-linux-arm64"  && -f "$OUT_DIR/vixd-linux-arm64" \
    && "$(cat "$STAMP_FILE")" == "$CURRENT_COMMIT" ]]; then
  echo "${C_GREEN}==>${C_RESET} vix binaries up to date (commit ${C_BOLD}${CURRENT_COMMIT:0:12}${C_RESET}), skipping."
  echo "    ${C_DIM}Run with --force to rebuild anyway.${C_RESET}"
  exit 0
fi

mkdir -p "$OUT_DIR"

# ── PostHog analytics key (embedded at build time) ────────────────────────────
# Nothing loads .env at runtime, so the key must be baked into the binary via
# -ldflags -X. Read it exclusively from .env. Without it, analytics is inert
# (telemetry.Init bails when the embedded key is empty). Release builds (a
# --version was passed) MUST have the key — bail loudly. Dev builds (version=dev)
# are allowed to ship without analytics.
VIX_POSTHOG_API_KEY=""
if [[ -f "$ROOT_DIR/.env" ]]; then
  VIX_POSTHOG_API_KEY="$(grep '^VIX_POSTHOG_API_KEY=' "$ROOT_DIR/.env" | head -n1 | cut -d= -f2-)"
fi
TELEMETRY_PKG="github.com/get-vix/vix/internal/telemetry"
KEY_LDFLAG=""
if [[ -n "$VIX_POSTHOG_API_KEY" ]]; then
  KEY_LDFLAG="-X ${TELEMETRY_PKG}.embeddedAPIKey=${VIX_POSTHOG_API_KEY}"
elif [[ "$VERSION" != "dev" ]]; then
  echo "${C_RED}✗${C_RESET} VIX_POSTHOG_API_KEY not found in $ROOT_DIR/.env — refusing to build release ${VERSION} without analytics." >&2
  exit 1
else
  echo "${C_YELLOW}warning:${C_RESET} VIX_POSTHOG_API_KEY not in .env — dev build will emit no analytics." >&2
fi
export KEY_LDFLAG

echo "${C_BLUE}==>${C_RESET} ${C_BOLD}Building vix${C_RESET} (darwin-arm64 + linux-amd64 + linux-arm64), version ${C_BOLD}${VERSION}${C_RESET}, commit ${C_BOLD}${CURRENT_COMMIT:0:12}${C_RESET}"

# ── Launch all three builds in parallel ──────────────────────────────────────
# darwin-arm64 runs natively (no docker). Each linux arch runs in its own
# `docker build` pipeline on golang:1.26-alpine — CGO on for tree-sitter,
# netgo+osusergo tags + static extldflags so the binary is fully
# self-contained (no glibc/NSS runtime deps). The Dockerfile is a heredoc so
# BuildKit caches `go mod download` in its own layer (module graph survives
# between releases) with a 3x retry for flaky proxy.

darwin_log="$(mktemp)"
amd64_log="$(mktemp)"
arm64_log="$(mktemp)"

# darwin-arm64 — native build
(
  CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
    go build -C "$ROOT_DIR" -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} ${KEY_LDFLAG}" \
      -o "$OUT_DIR/vix-darwin-arm64" ./cmd/vix \
  && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
    go build -C "$ROOT_DIR" -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} ${KEY_LDFLAG}" \
      -o "$OUT_DIR/vixd-darwin-arm64" ./cmd/vixd
) >"$darwin_log" 2>&1 &
darwin_pid=$!

# linux-amd64 — docker build
build_linux_docker() {
  local arch="$1" label="$2" logfile="$3"
  local tag="vix-build-${label}"
  local create_name="vix-extract-${label}-$$"
  # Unquoted heredoc — ${VERSION} expanded by the shell before docker sees it.
  docker build --platform "linux/${arch}" -f - -t "$tag" "$ROOT_DIR" <<DOCKERFILE >"$logfile" 2>&1
FROM golang:1.26-alpine
RUN apk add --no-cache build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download \
 || (sleep 5  && go mod download) \
 || (sleep 15 && go mod download)
COPY . .
RUN mkdir -p /out \
    && go build -trimpath -tags 'netgo osusergo' \
         -ldflags="-s -w -linkmode external -extldflags '-static' -X main.Version=${VERSION} ${KEY_LDFLAG}" \
         -o /out/vix ./cmd/vix \
    && go build -trimpath -tags 'netgo osusergo' \
         -ldflags="-s -w -linkmode external -extldflags '-static' -X main.Version=${VERSION} ${KEY_LDFLAG}" \
         -o /out/vixd ./cmd/vixd
DOCKERFILE
  docker create --name "$create_name" "$tag" true >>"$logfile" 2>&1
  docker cp "$create_name":/out/vix  "$OUT_DIR/vix-${label}"  >>"$logfile" 2>&1
  docker cp "$create_name":/out/vixd "$OUT_DIR/vixd-${label}" >>"$logfile" 2>&1
  docker rm "$create_name" >>"$logfile" 2>&1
}

build_linux_docker amd64 linux-amd64 "$amd64_log" &
amd64_pid=$!
build_linux_docker arm64 linux-arm64 "$arm64_log" &
arm64_pid=$!

# ── Live 3-column spinner ────────────────────────────────────────────────────
parallel_start=$SECONDS
frames=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')
darwin_done=0; amd64_done=0; arm64_done=0
darwin_elapsed=0; amd64_elapsed=0; arm64_elapsed=0
i=0

if [ -t 1 ]; then
  printf '\033[?25l'  # hide cursor
  while [ $darwin_done -eq 0 ] || [ $amd64_done -eq 0 ] || [ $arm64_done -eq 0 ]; do
    frame="${frames[i++ % ${#frames[@]}]}"

    if [ $darwin_done -eq 0 ] && ! kill -0 "$darwin_pid" 2>/dev/null; then
      darwin_done=1; darwin_elapsed=$((SECONDS - parallel_start))
    fi
    if [ $amd64_done -eq 0 ] && ! kill -0 "$amd64_pid" 2>/dev/null; then
      amd64_done=1; amd64_elapsed=$((SECONDS - parallel_start))
    fi
    if [ $arm64_done -eq 0 ] && ! kill -0 "$arm64_pid" 2>/dev/null; then
      arm64_done=1; arm64_elapsed=$((SECONDS - parallel_start))
    fi

    fmt_status() {
      local done="$1" elapsed="$2" label="$3"
      if [ "$done" -eq 0 ]; then
        printf "%s%s%s %s %s(%ss)%s" "$C_BLUE" "$frame" "$C_RESET" "$label" "$C_DIM" "$((SECONDS - parallel_start))" "$C_RESET"
      else
        printf "%s✓%s %s %s(%ss)%s" "$C_GREEN" "$C_RESET" "$label" "$C_DIM" "$elapsed" "$C_RESET"
      fi
    }

    printf "\r\033[K  %s    %s    %s" \
      "$(fmt_status $darwin_done $darwin_elapsed darwin-arm64)" \
      "$(fmt_status $amd64_done $amd64_elapsed linux-amd64)" \
      "$(fmt_status $arm64_done $arm64_elapsed linux-arm64)"
    sleep 0.1
  done
  printf '\033[?25h\r\033[K'  # restore cursor, clear line
else
  echo "==> Building darwin-arm64 + linux-amd64 + linux-arm64 in parallel"
fi

# ── Collect results ──────────────────────────────────────────────────────────
wait "$darwin_pid"; darwin_rc=$?
wait "$amd64_pid";  amd64_rc=$?
wait "$arm64_pid";  arm64_rc=$?

report() {
  local rc="$1" elapsed="$2" label="$3" logfile="$4"
  if [ "$rc" -ne 0 ]; then
    echo "${C_RED}✗${C_RESET} ${C_BOLD}${label}${C_RESET} ${C_DIM}(${elapsed}s)${C_RESET}"
    cat "$logfile"
  else
    echo "${C_GREEN}✓${C_RESET} ${label} ${C_DIM}(${elapsed}s)${C_RESET}"
  fi
}
report $darwin_rc $darwin_elapsed darwin-arm64 "$darwin_log"
report $amd64_rc  $amd64_elapsed  linux-amd64  "$amd64_log"
report $arm64_rc  $arm64_elapsed  linux-arm64  "$arm64_log"

parallel_elapsed=$((SECONDS - parallel_start))
echo "${C_DIM}    parallel wall clock: ${parallel_elapsed}s${C_RESET}"
rm -f "$darwin_log" "$amd64_log" "$arm64_log"

if [ $darwin_rc -ne 0 ] || [ $amd64_rc -ne 0 ] || [ $arm64_rc -ne 0 ]; then
  exit 1
fi

# ── Stamp ─────────────────────────────────────────────────────────────────────
echo "$CURRENT_COMMIT" > "$STAMP_FILE"
echo "${C_GREEN}==>${C_RESET} ${C_BOLD}Build complete${C_RESET} — binaries → ${OUT_DIR} (commit ${CURRENT_COMMIT:0:12})"
