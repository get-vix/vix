#!/usr/bin/env bash
# Build and run a throwaway container with vix installed on PATH.
#
# Usage:
#   script/vix-docker.sh [VERSION] [--from-source] [--mount] [--force-gpg]
#                        [--provider PATH --provider-id ID]
#
#   VERSION       Version of vix to install in release mode: "latest" (default),
#                 "1.2.3", or "v1.2.3". Ignored with --from-source.
#   --from-source Build vix locally with script/build.sh and COPY the resulting
#                 static Linux binaries into the image, instead of downloading a
#                 released build with the official installer.
#   --mount       Mount the current host directory into /workspace (read-write)
#                 so you can test vix against real code.
#   --force-gpg   Abort the install unless the GPG signature verifies.
#                 (release mode only.)
#   --provider P  Mount a Linux daz-secrets provider executable read-only.
#   --provider-id I  Exact provider identity reported by that executable.
#
# Examples:
#   script/vix-docker.sh                 # latest release, no credential provider
#   script/vix-docker.sh v1.2.3          # a specific released version
#   script/vix-docker.sh latest --mount  # latest, with the cwd mounted in
#   script/vix-docker.sh --from-source   # build from the working tree, then run
#   script/vix-docker.sh --provider ./provider-linux-arm64 --provider-id example.provider
#
# Inside the container, just run `vix`. It auto-spawns the vixd daemon.
# Credentials stay behind the mounted daz-secrets provider process.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

VERSION="latest"
MOUNT=false
FORCE_GPG=false
PROVIDER_PATH=""
PROVIDER_ID=""
FROM_SOURCE=false

while [ $# -gt 0 ]; do
  case "$1" in
    --from-source) FROM_SOURCE=true; shift ;;
    --mount)     MOUNT=true; shift ;;
    --force-gpg) FORCE_GPG=true; shift ;;
    --provider)  PROVIDER_PATH="${2:-}"; shift 2 ;;
    --provider-id) PROVIDER_ID="${2:-}"; shift 2 ;;
    -h|--help)
      sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    -*)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
    *)
      VERSION="$1"; shift ;;
  esac
done

if [ -n "$PROVIDER_PATH" ] && [ ! -x "$PROVIDER_PATH" ]; then
  echo "Error: --provider must name an executable Linux provider: $PROVIDER_PATH" >&2
  exit 1
fi
if { [ -n "$PROVIDER_PATH" ] && [ -z "$PROVIDER_ID" ]; } || { [ -z "$PROVIDER_PATH" ] && [ -n "$PROVIDER_ID" ]; }; then
  echo "Error: --provider and --provider-id must be supplied together" >&2
  exit 1
fi

# Map host arch to a Docker platform the build supports (linux amd64/arm64).
case "$(uname -m)" in
  arm64|aarch64) PLATFORM="linux/arm64" ;;
  x86_64|amd64)  PLATFORM="linux/amd64" ;;
  *)             echo "Unsupported host architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ "$FROM_SOURCE" = true ]; then
  IMAGE="vix-test:source"

  # Build the static Linux binaries locally first. script/build.sh produces
  # bin/{vix,vixd}-linux-{amd64,arm64}; the Dockerfile COPYs the one matching
  # the target arch.
  echo "==> Building vix from source (script/build.sh)"
  "$SCRIPT_DIR/build.sh"

  echo "==> Building ${IMAGE} (${PLATFORM}, from local source)"
  docker build \
    --platform "$PLATFORM" \
    --build-arg "VIX_INSTALL_MODE=source" \
    -t "$IMAGE" \
    -f "$ROOT_DIR/Dockerfile" \
    "$ROOT_DIR"
else
  IMAGE="vix-test:${VERSION}"

  echo "==> Building ${IMAGE} (${PLATFORM}, vix ${VERSION})"
  docker build \
    --platform "$PLATFORM" \
    --build-arg "VIX_VERSION=${VERSION}" \
    --build-arg "VIX_FORCE_GPG=${FORCE_GPG}" \
    -t "$IMAGE" \
    -f "$ROOT_DIR/Dockerfile" \
    "$ROOT_DIR"
fi

RUN_ARGS=(--rm -it --platform "$PLATFORM")
PROVIDER_CONFIG_DIR=""
if [ -n "$PROVIDER_PATH" ]; then
  PROVIDER_CONFIG_DIR="$(mktemp -d)"
  trap 'rm -rf "$PROVIDER_CONFIG_DIR"' EXIT
  printf 'version = 1\nprovider_path = "/run/daz-secrets/provider"\nprovider_id = "%s"\ntimeout_ms = 5000\n' "$PROVIDER_ID" > "$PROVIDER_CONFIG_DIR/provider.toml"
  chmod 600 "$PROVIDER_CONFIG_DIR/provider.toml"
  RUN_ARGS+=(-v "$PROVIDER_PATH:/run/daz-secrets/provider:ro")
  RUN_ARGS+=(-v "$PROVIDER_CONFIG_DIR/provider.toml:/root/.config/daz-secrets/provider.toml:ro")
  echo "==> Mounting daz-secrets provider $PROVIDER_ID"
else
  echo ""
  echo "!!  No daz-secrets provider was supplied."
  echo "    Vix will install and start, but credential-backed features fail closed."
  echo "    Pass --provider and --provider-id to exercise real credentials."
  echo ""
fi

if [ "$MOUNT" = true ]; then
  echo "==> Mounting $PWD -> /workspace"
  RUN_ARGS+=(-v "$PWD:/workspace")
fi

echo "==> Starting container. Type 'vix' to test, 'exit' to leave."
exec docker run "${RUN_ARGS[@]}" "$IMAGE"
