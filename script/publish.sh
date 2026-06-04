#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <version> --repo <owner/repo> [--force] [--changelog <path>]"
  echo "  e.g. $0 v0.1.0 --repo get-vix/vix"
  echo ""
  echo "  --force              Delete existing release before creating a new one"
  echo "  --changelog <path>   Use contents of this file as the changelog"
  echo "                       (skips git-log derivation; confirmation prompt still shown)"
  exit 1
}

# Parse arguments
VERSION=""
REPO=""
FORCE=false
CHANGELOG_FILE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)
      REPO="$2"
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

if [[ -n "$CHANGELOG_FILE" && ! -f "$CHANGELOG_FILE" ]]; then
  echo "!! --changelog file not found: $CHANGELOG_FILE"
  exit 1
fi

# Ensure version starts with v
if [[ "$VERSION" != v* ]]; then
  VERSION="v$VERSION"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN_DIR="$ROOT_DIR/bin"
DIST_DIR="$ROOT_DIR/dist"

# --- Verify build outputs exist ---
# build.sh produces loose binaries in $ROOT_DIR/bin/. publish.sh is the
# tarball/SHA256/Homebrew/GPG/upload side of the pipeline.
REQUIRED_BINS=(
  "$BIN_DIR/vix-darwin-arm64" "$BIN_DIR/vixd-darwin-arm64"
  "$BIN_DIR/vix-linux-amd64"  "$BIN_DIR/vixd-linux-amd64"
  "$BIN_DIR/vix-linux-arm64"  "$BIN_DIR/vixd-linux-arm64"
)
for f in "${REQUIRED_BINS[@]}"; do
  if [[ ! -x "$f" ]]; then
    echo "!! Missing or non-executable: $f"
    echo "   Run ./script/build.sh --version $VERSION first."
    exit 1
  fi
done

# --- Stage tarballs in dist/ ---
# Each platform gets its own vix-<platform>/ directory containing vix + vixd,
# tarred into vix-<platform>.tar.gz. Keeps the exact naming install.sh,
# update-tap.sh, and the Homebrew formula depend on.
echo "==> Staging tarballs in $DIST_DIR..."
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

stage_platform() {
  local platform="$1"  # e.g. darwin-arm64
  local stage_dir="$DIST_DIR/vix-${platform}"
  mkdir -p "$stage_dir"
  cp "$BIN_DIR/vix-${platform}"  "$stage_dir/vix"
  cp "$BIN_DIR/vixd-${platform}" "$stage_dir/vixd"
  tar -czf "$DIST_DIR/vix-${platform}.tar.gz" -C "$DIST_DIR" "vix-${platform}"
  rm -rf "$stage_dir"
}
stage_platform darwin-arm64
stage_platform linux-amd64
stage_platform linux-arm64

TARBALLS=("$DIST_DIR"/vix-*.tar.gz)

# --- Per-tarball SHA256 for the Homebrew formula ---
echo "==> Computing tarball checksums for Homebrew formula..."
sha_of() { shasum -a 256 "$DIST_DIR/vix-${1}.tar.gz" | awk '{print $1}'; }
SHA_DARWIN_ARM64=$(sha_of darwin-arm64)
SHA_LINUX_ARM64=$(sha_of linux-arm64)
SHA_LINUX_AMD64=$(sha_of linux-amd64)
echo "    darwin-arm64: $SHA_DARWIN_ARM64"
echo "    linux-arm64:  $SHA_LINUX_ARM64"
echo "    linux-amd64:  $SHA_LINUX_AMD64"

# --- Homebrew formula ---
# Two flavors:
#   vix.rb       — ships to the tap repo, URLs point at the GitHub release.
#   vix-local.rb — local-testing mirror with file:// URLs, consumed by
#                  script/test-install.sh before publishing.
RELEASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

cat > "$DIST_DIR/vix.rb" <<FORMULA
class Vix < Formula
  desc "AI coding agent"
  homepage "https://github.com/${REPO}"
  version "${VERSION#v}"
  license "AGPL-3.0-or-later"

  on_macos do
    on_arm do
      url "${RELEASE_URL}/vix-darwin-arm64.tar.gz"
      sha256 "${SHA_DARWIN_ARM64}"
    end
  end

  on_linux do
    on_arm do
      url "${RELEASE_URL}/vix-linux-arm64.tar.gz"
      sha256 "${SHA_LINUX_ARM64}"
    end
    on_intel do
      url "${RELEASE_URL}/vix-linux-amd64.tar.gz"
      sha256 "${SHA_LINUX_AMD64}"
    end
  end

  def install
    bin.install "vix"
    bin.install "vixd"
  end

  service do
    run [opt_bin/"vixd"]
    keep_alive true
    log_path var/"log/vixd.log"
    error_log_path var/"log/vixd.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/vix --version 2>&1", 1)
  end
end
FORMULA

cat > "$DIST_DIR/vix-local.rb" <<FORMULA
class Vix < Formula
  desc "AI coding agent"
  homepage "https://github.com/${REPO}"
  version "${VERSION#v}"
  license "AGPL-3.0-or-later"

  on_macos do
    on_arm do
      url "file:///tmp/dist/vix-darwin-arm64.tar.gz"
      sha256 "${SHA_DARWIN_ARM64}"
    end
  end

  on_linux do
    on_arm do
      url "file:///tmp/dist/vix-linux-arm64.tar.gz"
      sha256 "${SHA_LINUX_ARM64}"
    end
    on_intel do
      url "file:///tmp/dist/vix-linux-amd64.tar.gz"
      sha256 "${SHA_LINUX_AMD64}"
    end
  end

  def install
    bin.install "vix"
    bin.install "vixd"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/vix --version 2>&1", 1)
  end
end
FORMULA
echo "==> Homebrew formulas written to $DIST_DIR/vix.rb + vix-local.rb"

# --- Build changelog ---
# Either load a user-supplied file (--changelog <path>) or derive one from the
# commits since the previous tag. Either way we show it and ask for a y/N
# confirmation before publishing — the release notes are user-visible and
# worth a sanity check.
if [[ -n "$CHANGELOG_FILE" ]]; then
  echo "==> Loading changelog from $CHANGELOG_FILE..."
  CHANGELOG=$(cat "$CHANGELOG_FILE")
  RANGE_LABEL="(from $CHANGELOG_FILE)"
else
  echo "==> Generating changelog for $VERSION..."
  PREV_TAG=$(git -C "$ROOT_DIR" describe --tags --abbrev=0 2>/dev/null || true)
  if [[ -z "$PREV_TAG" ]]; then
    echo "    No previous tag found; using all commits."
    CHANGELOG=$(git -C "$ROOT_DIR" log --pretty=format:"- %s")
    RANGE_LABEL="(all history)"
  else
    echo "    Previous tag: $PREV_TAG"
    CHANGELOG=$(git -C "$ROOT_DIR" log "${PREV_TAG}..HEAD" --pretty=format:"- %s")
    RANGE_LABEL="${PREV_TAG}..HEAD"
  fi
fi

if [[ -z "$CHANGELOG" ]]; then
  CHANGELOG="- (no new commits)"
fi

echo ""
echo "----- Changelog $RANGE_LABEL -----"
echo "$CHANGELOG"
echo "----------------------------------"
echo ""
read -r -p "Use this as the Discord changelog? [y/N] " CHANGELOG_OK
if [[ ! "$CHANGELOG_OK" =~ ^[Yy]$ ]]; then
  echo "Aborted by user."
  exit 1
fi

# Handle --force: delete existing release
if [[ "$FORCE" == true ]]; then
  echo "==> Deleting existing release $VERSION (if any)..."
  gh release delete "$VERSION" --repo "$REPO" --yes --cleanup-tag || true
fi

# --- Generate checksums.txt and GPG-sign it ---
# install.sh (on getvix.dev) reads
# checksums.txt to verify tarball integrity, and checksums.txt.asc to verify
# the checksums were actually signed by the release key. Sign detached +
# armored so the .asc lives next to the file and is ASCII-safe.
echo "==> Generating checksums.txt..."
(cd "$DIST_DIR" && shasum -a 256 vix-*.tar.gz > checksums.txt)

echo "==> GPG-signing checksums.txt..."
echo "→  TOUCH your YubiKey when it blinks (PIN prompt will appear first if not cached)"
gpg --armor --detach-sign --yes \
  --output "$DIST_DIR/checksums.txt.asc" \
  "$DIST_DIR/checksums.txt"

# Create release and upload tarballs + checksums + signature. Notes come
# from our own git-derived changelog (vix commits), not `gh --generate-notes`
# which would read from $REPO (vix) where there is no source history.
GH_NOTES="## What's Changed

${CHANGELOG}"
echo "==> Creating GitHub release $VERSION..."
gh release create "$VERSION" \
  --repo "$REPO" \
  --title "$VERSION" \
  --target main \
  --notes "$GH_NOTES" \
  "${TARBALLS[@]}" \
  "$DIST_DIR/checksums.txt" \
  "$DIST_DIR/checksums.txt.asc"

RELEASE_URL="https://github.com/${REPO}/releases/tag/${VERSION}"
echo ""
echo "==> Release published: $RELEASE_URL"

# Announce on Discord
DISCORD_WEBHOOK_URL="${DISCORD_WEBHOOK_URL:-}"
if [[ -n "$DISCORD_WEBHOOK_URL" ]]; then
  echo "==> Announcing $VERSION on Discord..."
  DISCORD_MSG="**vix ${VERSION}** is out! ${RELEASE_URL}

**Changelog**
${CHANGELOG}"
  # Discord content limit is 2000 chars
  if (( ${#DISCORD_MSG} > 1950 )); then
    DISCORD_MSG="${DISCORD_MSG:0:1950}
... (truncated, see ${RELEASE_URL})"
  fi
  DISCORD_PAYLOAD=$(CONTENT="$DISCORD_MSG" python3 -c 'import json, os; print(json.dumps({"content": os.environ["CONTENT"]}))')
  if curl -fsS -X POST -H "Content-Type: application/json" -d "$DISCORD_PAYLOAD" "$DISCORD_WEBHOOK_URL" >/dev/null; then
    echo "==> Discord announcement sent."
  else
    echo "!! Failed to post to Discord (release still published)."
  fi
fi
