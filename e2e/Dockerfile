# syntax=docker/dockerfile:1
#
# vix end-to-end test image.
#
# Philosophy: BUILD ON HOST, COPY BINARIES IN. The product binaries (vix,
# vixd) come from script/build.sh and the test binary + report CLI from
# e2e/build-e2e.sh — all statically linked Linux binaries. The only thing built
# in-image is `freeze` (for rendering screenshots to PNG), in a throwaway
# builder stage. The runtime image has no Go toolchain.
#
# Run via `make test-e2e`, which passes the landlock-allowing seccomp profile
# and bind-mounts /out for the report zip.

ARG TARGETARCH=amd64

# --- builder: just to obtain a static `freeze` binary -----------------------
FROM golang:1.26-bookworm AS freeze-builder
ENV CGO_ENABLED=0
RUN go install github.com/charmbracelet/freeze@latest

# --- runtime ----------------------------------------------------------------
FROM debian:bookworm-slim
ARG TARGETARCH

# bash + coreutils so the agent's bash tool (ls, cat, …) works inside the
# container; grep + jq so lifecycle-hook commands can match/parse tool input the
# way real hooks (and the docs' examples) do; sqlite3 for the GitHub watch-job
# tracker.sh used by the jobs.github_claim scenario; ca-certificates is harmless
# and handy. fontconfig/fonts let freeze render text glyphs in the PNGs.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash coreutils grep jq sqlite3 ca-certificates fontconfig fonts-dejavu bubblewrap tmux \
 && rm -rf /var/lib/apt/lists/*

COPY --from=freeze-builder /go/bin/freeze /usr/local/bin/freeze

# Prebuilt static binaries from the host (build context = repo root).
COPY bin/vix-linux-${TARGETARCH}  /usr/local/bin/vix
COPY bin/vixd-linux-${TARGETARCH} /usr/local/bin/vixd
COPY e2e/bin/e2e.test             /usr/local/bin/e2e.test
COPY e2e/bin/report               /usr/local/bin/report
COPY e2e/bin/daz-secrets           /usr/local/bin/daz-secrets
COPY e2e/bin/vix-e2e-secrets--ok    /usr/local/bin/vix-e2e-secrets--ok
RUN chmod +x /usr/local/bin/vix /usr/local/bin/vixd /usr/local/bin/e2e.test /usr/local/bin/report /usr/local/bin/daz-secrets /usr/local/bin/vix-e2e-secrets--ok \
 && mkdir -p /root/.config/daz-secrets \
 && printf 'version = 1\nprovider_path = "/usr/local/bin/vix-e2e-secrets--ok"\nprovider_id = "vix-e2e-secrets"\ntimeout_ms = 5000\n' > /root/.config/daz-secrets/provider.toml \
 && chmod 600 /root/.config/daz-secrets/provider.toml \
 && printf test | daz-secrets set vix anthropic-api-key \
 && printf test | daz-secrets set vix openai-api-key \
 && printf test | daz-secrets set vix minimax-api-key \
 && printf acme-test | daz-secrets set vix acme-env-api-key

# Deterministic locale/terminal for stable screenshots.
ENV LC_ALL=C.UTF-8 \
    LANG=C.UTF-8 \
    TERM=xterm-256color \
    VIX_E2E=1 \
    VIX_BIN=/usr/local/bin/vix \
    VIXD_BIN=/usr/local/bin/vixd \
    FREEZE_BIN=/usr/local/bin/freeze \
    REPORT_BIN=/usr/local/bin/report \
    VIX_E2E_REPORT=/out/report

WORKDIR /work

# Runs the suite (appends artifacts), then TestMain renders + zips to /out.
CMD ["/usr/local/bin/e2e.test", "-test.v"]
