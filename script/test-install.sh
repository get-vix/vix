#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 [--local | --tap <owner/tap>]"
  echo ""
  echo "  --local          Install from local dist/ tarballs and formula (default)"
  echo "  --tap owner/tap  Install via 'brew tap owner/tap && brew install vix'"
  echo ""
  echo "  e.g. $0 --local"
  echo "  e.g. $0 --tap get-vix/tap"
  exit 1
}

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"

MODE="local"
TAP=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --local)
      MODE="local"
      shift
      ;;
    --tap)
      MODE="tap"
      TAP="$2"
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "Unknown argument: $1"
      usage
      ;;
  esac
done

# Detect platform
ARCH="$(uname -m)"
if [[ "$ARCH" == "arm64" || "$ARCH" == "aarch64" ]]; then
  PLATFORM="linux/arm64"
else
  PLATFORM="linux/amd64"
fi

if [[ "$MODE" == "local" ]]; then
  if [[ ! -f "$DIST_DIR/vix-local.rb" ]]; then
    echo "Missing $DIST_DIR/vix-local.rb — run release.sh first."
    exit 1
  fi

  echo "==> Starting test container (local install)"
  docker run --rm -it \
    --platform "$PLATFORM" \
    -v "$DIST_DIR:/tmp/dist:ro" \
    homebrew/brew:latest \
    bash -c "
      mkdir -p \$(brew --repo)/Library/Taps/local/homebrew-vix/Formula
      cp /tmp/dist/vix-local.rb \$(brew --repo)/Library/Taps/local/homebrew-vix/Formula/vix.rb
      echo '==> Installing vix via Homebrew...'
      brew install vix
      echo ''
      echo '==> Done! Type \"vix\" to test.'
      echo ''
      echo '  Configure a daz-secrets provider before exercising an LLM.'
      echo ''
      exec bash
    "
else
  echo "==> Starting test container (tap install) with brew tap $TAP"
  docker run --rm -it \
    --platform "$PLATFORM" \
    homebrew/brew:latest \
    bash -c "
      echo '==> brew tap $TAP'
      brew tap $TAP
      echo '==> brew install vix'
      brew install vix
      echo ''
      echo '==> Done! Type \"vix\" to test.'
      echo ''
      echo '  Configure a daz-secrets provider before exercising an LLM.'
      echo ''
      exec bash
    "
fi
