#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <version> --repo <owner/repo> [--tap <owner/tap>] [--force] [--changelog <path>]"
  echo "  e.g. $0 v0.1.0 --repo get-vix/vix"
  echo ""
  echo "  --tap <owner/tap>    Homebrew tap repo (default: derives owner/homebrew-vix from --repo)"
  echo "  --force              Replace an existing release"
  echo "  --changelog <path>   Use contents of this file as the release changelog"
  echo "                       (skips the git-log derivation; confirmation prompt still shown)"
  exit 1
}

# Parse arguments
VERSION=""
REPO=""
TAP=""
FORCE=false
CHANGELOG_FILE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)
      REPO="$2"
      shift 2
      ;;
    --tap)
      TAP="$2"
      shift 2
      ;;
    --force)
      FORCE=true
      shift
      ;;
    --changelog)
      CHANGELOG_FILE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    *)
      if [[ -z "$VERSION" ]]; then
        VERSION="$1"
      else
        echo "Unknown argument: $1"
        usage
      fi
      shift
      ;;
  esac
done

if [[ -z "$VERSION" || -z "$REPO" ]]; then
  usage
fi

if [[ -n "$CHANGELOG_FILE" ]]; then
  if [[ ! -f "$CHANGELOG_FILE" ]]; then
    echo "!! --changelog file not found: $CHANGELOG_FILE"
    exit 1
  fi
  # Resolve to an absolute path so publish.sh can still find it regardless of cwd.
  CHANGELOG_FILE="$(cd "$(dirname "$CHANGELOG_FILE")" && pwd)/$(basename "$CHANGELOG_FILE")"
fi

# Derive tap from repo owner if not specified
if [[ -z "$TAP" ]]; then
  OWNER="${REPO%%/*}"
  TAP="${OWNER}/homebrew-vix"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Load DISCORD_WEBHOOK_URL from .env if not already set. Release must have a webhook.
if [[ -z "${DISCORD_WEBHOOK_URL:-}" ]]; then
  ENV_FILE="$ROOT_DIR/.env"
  if [[ -f "$ENV_FILE" ]]; then
    DISCORD_WEBHOOK_URL="$(grep '^DISCORD_WEBHOOK_URL=' "$ENV_FILE" | head -n1 | cut -d= -f2-)"
    export DISCORD_WEBHOOK_URL
  fi
fi

if [[ -z "${DISCORD_WEBHOOK_URL:-}" ]]; then
  echo "!! DISCORD_WEBHOOK_URL is not set. Configure it in your environment or in .env before releasing."
  exit 1
fi

# --- Step 0: Ensure git working tree is clean ---
echo "==> Checking git status..."
if [[ -n "$(git status --porcelain)" ]]; then
  echo "!! Git working tree is not clean. Please commit or stash your changes before releasing."
  git status --short
  exit 1
fi
echo "==> Git working tree is clean."
echo ""

# --- Step 1: Verify YubiKey and establish persistent SSH connection ---
echo "==> Checking YubiKey (SSH)..."

# ControlPersist=yes so the master outlives the build; the EXIT trap tears it down.
SSH_CONTROL_PATH="/tmp/ssh-vix-release-%h-%p-%r"
SSH_OPTS="-o ControlMaster=auto -o ControlPath=$SSH_CONTROL_PATH -o ControlPersist=yes"
export GIT_SSH_COMMAND="ssh $SSH_OPTS"

# Establish the master connection in the background (only YubiKey prompt)
ssh $SSH_OPTS -o ControlMaster=yes -N -f git@github.com

# Clean up SSH master connection on exit
cleanup_ssh() {
  ssh $SSH_OPTS -O exit git@github.com 2>/dev/null || true
}
trap cleanup_ssh EXIT

# Verify authentication works
SSH_OUTPUT=$(ssh $SSH_OPTS -T git@github.com 2>&1 || true)
if ! echo "$SSH_OUTPUT" | grep -qi "successfully authenticated"; then
  echo "!! SSH authentication to GitHub failed. Is your YubiKey inserted?"
  echo "   $SSH_OUTPUT"
  exit 1
fi
echo "==> YubiKey confirmed (SSH connection will be reused for all operations)."
echo ""

# --- Step 2: Build ---
# build.sh now produces only loose binaries in $ROOT_DIR/bin/.
# publish.sh picks them up and owns tarballs, SHA256, GPG, formula.
"$SCRIPT_DIR/build.sh" --version "$VERSION" --force

# --- Step 3: Publish ---
PUBLISH_ARGS=("$VERSION" --repo "$REPO")
if [[ "$FORCE" == true ]]; then
  PUBLISH_ARGS+=(--force)
fi
if [[ -n "$CHANGELOG_FILE" ]]; then
  PUBLISH_ARGS+=(--changelog "$CHANGELOG_FILE")
fi
"$SCRIPT_DIR/publish.sh" "${PUBLISH_ARGS[@]}"

# --- Step 4: Update tap ---
"$SCRIPT_DIR/update-tap.sh" "$VERSION" --tap "$TAP"

# --- Step 5: Tag the release commit (GPG-signed) ---
echo ""
echo "==> Tagging commit as $VERSION (GPG-signed)..."
if [[ "$FORCE" == true ]]; then
  git tag -d "$VERSION" 2>/dev/null || true
fi
# -s signs with user.signingkey (must be configured in git config). The
# YubiKey's OpenPGP applet will prompt for touch+PIN if gpg-agent hasn't
# cached it this session — expect a pause here on fresh runs.
echo "→  TOUCH your YubiKey when it blinks (PIN prompt will appear first if not cached)"
git tag -s "$VERSION" -m "Release $VERSION"
echo "==> Tagged and signed $VERSION."

# --- Done ---
echo ""
echo "================================================"
echo "  Vix $VERSION released successfully!"
echo ""
echo "  Install with:"
echo "    brew tap ${TAP%%/*}/vix"
echo "    brew install vix"
echo "================================================"
