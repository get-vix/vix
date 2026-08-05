# Dockerfile for testing a build of vix in a clean container.
#
# Two install modes, selected by VIX_INSTALL_MODE:
#
#   release (default) — vix is installed via the official installer
#     (https://getvix.dev/install.sh), which drops both the `vix` CLI and the
#     `vixd` daemon into /usr/local/bin. Nothing from the local repo is used.
#
#   source — the static Linux binaries built locally by script/build.sh
#     (bin/vix-linux-<arch>, bin/vixd-linux-<arch>) are COPYed into the image.
#     Build them first: `script/build.sh`.
#
# Running `vix` auto-spawns `vixd`, so a shell with both on PATH is all you need.
#
# Build (release):
#   docker build --build-arg VIX_VERSION=latest -t vix-test .
# Build (source — requires bin/ populated by script/build.sh first):
#   docker build --build-arg VIX_INSTALL_MODE=source -t vix-test .
# Run:
#   docker run --rm -it -e ANTHROPIC_API_KEY vix-test
#
# Prefer the script/vix-docker.sh wrapper, which handles platform, build args,
# and (for source mode) running script/build.sh for you.

# Select which install stage becomes the final image: "release" or "source".
# Declared in the global scope so it can be used in the `FROM ${VIX_INSTALL_MODE}`
# selector below.
ARG VIX_INSTALL_MODE=release

# ── Base: common OS + tooling shared by both install modes ────────────────────
FROM debian:stable-slim AS base

# Dependencies:
#   ca-certificates curl wget -> download release + checksums over HTTPS
#   tar gzip unzip            -> extract archives
#   coreutils                 -> sha256sum for checksum verification
#   gnupg                     -> optional GPG signature verification
#   git                       -> vix shells out to git for many tasks
#   bash less vim nano        -> shell + editors / pager
#   procps psmisc htop        -> ps, top, kill, htop (process inspection)
#   iproute2                  -> ss / ip (networking)
#   jq ripgrep tree file      -> common everyday CLI tools
#   sudo                      -> so you can install more packages interactively
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        coreutils \
        curl \
        file \
        git \
        gnupg \
        gzip \
        htop \
        iproute2 \
        jq \
        less \
        nano \
        procps \
        psmisc \
        ripgrep \
        sudo \
        tar \
        tree \
        unzip \
        vim \
        wget \
    && rm -rf /var/lib/apt/lists/*

# ── release: install vix from the official installer ──────────────────────────
FROM base AS release

# Version of vix to install: "latest", "1.2.3", or "v1.2.3".
# Declared as an ARG so changing it busts the install layer's cache.
ARG VIX_VERSION=latest

# When true, the installer fails (rather than silently downgrading to
# checksum-only verification) if the GPG signature can't be verified.
ARG VIX_FORCE_GPG=false

# As root, /usr/local/bin is writable, so the installer needs no sudo and runs
# without interactive prompts.
RUN set -eux; \
    args="${VIX_VERSION} --no-fancy"; \
    if [ "${VIX_FORCE_GPG}" = "true" ]; then args="${args} --force-gpg-verification"; fi; \
    curl -fsSL https://getvix.dev/install.sh | bash -s -- ${args}; \
    vix --version

# ── source: copy locally-built static binaries from script/build.sh ───────────
# TARGETARCH is set automatically by BuildKit from --platform (amd64 / arm64),
# matching the naming script/build.sh uses for its loose binaries.
FROM base AS source
ARG TARGETARCH
COPY bin/vix-linux-${TARGETARCH}  /usr/local/bin/vix
COPY bin/vixd-linux-${TARGETARCH} /usr/local/bin/vixd
RUN set -eux; \
    chmod +x /usr/local/bin/vix /usr/local/bin/vixd; \
    vix --version

# ── final: pick the selected install stage, then add shared runtime config ────
FROM ${VIX_INSTALL_MODE} AS final

# Handy interactive-shell aliases for the throwaway test container.
RUN echo "alias ll='ls -al'" >> /root/.bashrc

WORKDIR /workspace

# Entrypoint: seed /workspace/.env from a read-only mount at /seed/.env (if the
# host provided one), so `vix` picks up the API key with zero extra steps. It's
# a copy, not the mount itself, so you can `rm /workspace/.env` inside the
# container to drop the key and enter it yourself. We never bake the key into
# the image — the .env only arrives via a runtime bind mount.
RUN cat > /usr/local/bin/vix-docker-entrypoint.sh <<'EOF' \
    && chmod +x /usr/local/bin/vix-docker-entrypoint.sh
#!/usr/bin/env bash
set -e
if [ -f /seed/.env ] && [ ! -f /workspace/.env ]; then
  cp /seed/.env /workspace/.env
fi
exec "$@"
EOF

ENTRYPOINT ["vix-docker-entrypoint.sh"]
CMD ["bash"]
