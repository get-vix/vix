#!/usr/bin/env bash
# Build and run a throwaway container with vix installed on PATH.
#
# Usage:
#   script/vix-docker.sh [VERSION] [--from-source] [--mount] [--force-gpg] [--env-file PATH] [--no-env]
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
#   --env-file P  Seed /workspace/.env from P (default: <repo>/.env if present).
#                 vix reads .env from its working dir, so the API key just works.
#                 It's copied in, so you can `rm /workspace/.env` to drop it.
#   --no-env      Don't seed any .env — you'll enter the key yourself.
#
# Examples:
#   script/vix-docker.sh                 # latest release, seeds <repo>/.env if present
#   script/vix-docker.sh v1.2.3          # a specific released version
#   script/vix-docker.sh latest --mount  # latest, with the cwd mounted in
#   script/vix-docker.sh --from-source   # build from the working tree, then run
#   script/vix-docker.sh --no-env        # no key baked in; enter it yourself
#
# Inside the container, just run `vix`. It auto-spawns the vixd daemon.
# vix needs ANTHROPIC_API_KEY — this script forwards it from your environment.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

VERSION="latest"
MOUNT=false
FORCE_GPG=false
NO_ENV=false
ENV_FILE=""
FROM_SOURCE=false

while [ $# -gt 0 ]; do
  case "$1" in
    --from-source) FROM_SOURCE=true; shift ;;
    --mount)     MOUNT=true; shift ;;
    --force-gpg) FORCE_GPG=true; shift ;;
    --no-env)    NO_ENV=true; shift ;;
    --env-file)  ENV_FILE="${2:-}"; shift 2 ;;
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

# Resolve the .env to seed into /workspace. Default to the repo's .env when the
# caller didn't pass --env-file and didn't opt out with --no-env.
if [ "$NO_ENV" = true ]; then
  ENV_FILE=""
elif [ -z "$ENV_FILE" ] && [ -f "$ROOT_DIR/.env" ]; then
  ENV_FILE="$ROOT_DIR/.env"
fi
if [ -n "$ENV_FILE" ] && [ ! -f "$ENV_FILE" ]; then
  echo "Error: --env-file path not found: $ENV_FILE" >&2
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

if [ -n "$ENV_FILE" ]; then
  echo "==> Seeding /workspace/.env from $ENV_FILE (rm it inside the container to drop the key)"
elif [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo ""
  echo "!!  No .env to seed and ANTHROPIC_API_KEY is not set in your environment."
  echo "    vix will start but can't reach the LLM until you provide a key. Either:"
  echo "      export ANTHROPIC_API_KEY=sk-ant-...   # then re-run this script"
  echo "      script/vix-docker.sh --env-file /path/to/.env"
  echo "    or set it from inside the container shell."
  echo ""
fi

RUN_ARGS=(--rm -it --platform "$PLATFORM" -e ANTHROPIC_API_KEY)

if [ -n "$ENV_FILE" ]; then
  RUN_ARGS+=(-v "$ENV_FILE:/seed/.env:ro")
fi

if [ "$MOUNT" = true ]; then
  echo "==> Mounting $PWD -> /workspace"
  RUN_ARGS+=(-v "$PWD:/workspace")
fi

echo "==> Starting container. Type 'vix' to test, 'exit' to leave."
exec docker run "${RUN_ARGS[@]}" "$IMAGE"
