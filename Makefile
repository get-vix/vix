.PHONY: build build-web build-all pull push test test-e2e test-e2e-sharded test-all test-perf perf-corpus perf-baseline perf-smoke release run-server run-ui patch-deps vendor-cgo-sources update-deps web-source proto-schema mac-models mac-probe mac-build bundle-mac run-mac

# The web UI source lives in a private submodule (internal/daemon/web/source).
# It is only needed to *rebuild* the UI; the built output (internal/daemon/web/dist/)
# is committed and embedded into vixd, so `make build` works for everyone without it.
WEB_SOURCE := $(wildcard internal/daemon/web/source/package.json)

# Rebuild the frontend (regenerates internal/daemon/web/dist/).
# No-op when the private web source isn't present — the committed dist/ is used as-is.
build-web:
ifneq ($(WEB_SOURCE),)
	npm --prefix internal/daemon/web/source ci
	VITE_USE_REAL_DATA=true npm --prefix internal/daemon/web/source run build
else
	@echo "web/source not present — using committed internal/daemon/web/dist/ (no rebuild)"
endif

# Fetch the private web UI source (maintainers with repo access only).
web-source:
	git submodule update --init --checkout internal/daemon/web/source

# Pull latest changes from the web UI source and rebuild (requires web source access).
pull:
ifneq ($(WEB_SOURCE),)
	git submodule update --remote --merge internal/daemon/web/source
	git pull
else
	@echo "web/source not present — run 'make web-source' first (requires repo access)."
	@exit 1
endif

# Push submodule first, then main repo (requires web source access).
push:
ifneq ($(WEB_SOURCE),)
	git -C internal/daemon/web/source push
	git push
else
	@echo "web/source not present — nothing to push for the web UI."
	@exit 1
endif

build-d: build-web
# go build -race -o bin/vixd ./cmd/vixd
	go build -o bin/vixd ./cmd/vixd

# Build and run the daemon with debug instrumentation (pprof on :6060)
run-d: build-d
	GOTRACEBACK=crash \
	GODEBUG=schedtrace=5000 \
	GORACE=halt_on_error=1 \
	ANTHROPIC_BASE_URL=http://localhost:59000/ \
	./bin/vixd --pprof-port 6060 2>/tmp/vixd-debug.log

build-x:
# go build -race -o bin/vix ./cmd/vix
	go build -o bin/vix ./cmd/vix

# Build and run the TUI client with debug instrumentation (pprof on :6061)
run-x: build-x
	GOTRACEBACK=crash \
	GORACE=halt_on_error=1 \
	./bin/vix --pprof-port 6061 2>/tmp/vix-debug.log 
# 	To try with full restrictions
#	./bin/vix --pprof-port 6061 2>/tmp/vix-debug.log -disable-automatic-directory-access -disable-automatic-write-permission

# Local dev build — current platform only, fast
build: build-d build-x

# Regenerate the machine-readable daemon<->client protocol schema from the Go
# structs in internal/protocol. Commit the result. CI runs this and fails if the
# committed internal/protocol/schema/vix-protocol.schema.json is out of date
# (see internal/protocol/protoschema TestSchemaNotStale).
proto-schema:
	go run ./cmd/protoschema

# Regenerate the Swift Codable models (apps/vix-mac) from the same protocol walk
# as the JSON schema. Runs the Go generator, so it works on any platform. Commit
# the result — internal/protocol/protoschema TestSwiftNotStale fails when the
# committed Generated.swift is out of date.
mac-models:
	go run ./cmd/protoschema -lang swift

# Build and run the headless Swift probe against the running vixd (macOS only;
# needs the Swift toolchain). Regenerates models first. Honors VIXD_SOCK.
mac-probe: mac-models
ifeq ($(shell uname),Darwin)
	cd apps/vix-mac && swift run vix-mac-probe
else
	@echo "mac-probe: macOS + Swift toolchain required"; exit 1
endif

# --- macOS SwiftUI app (Darwin only; needs the Swift toolchain) ---
MAC_APP := apps/vix-mac/.build/VixMac.app

# Compile the SwiftUI app executable (regenerates models first).
mac-build: mac-models
ifeq ($(shell uname),Darwin)
	cd apps/vix-mac && swift build --product vix-mac
else
	@echo "mac-build: macOS + Swift toolchain required"; exit 1
endif

# Assemble a runnable .app bundle around the built executable. No Xcode, no
# signing — a plain unsandboxed dev bundle that can reach /tmp/vixd.sock.
bundle-mac: mac-build
ifeq ($(shell uname),Darwin)
	@rm -rf "$(MAC_APP)"
	@mkdir -p "$(MAC_APP)/Contents/MacOS"
	@cp apps/vix-mac/App/Info.plist "$(MAC_APP)/Contents/Info.plist"
	@cp "$$(cd apps/vix-mac && swift build --product vix-mac --show-bin-path)/vix-mac" "$(MAC_APP)/Contents/MacOS/VixMac"
	@echo "==> $(MAC_APP)"
else
	@echo "bundle-mac: macOS + Swift toolchain required"; exit 1
endif

# Build, bundle, and launch the app against the running vixd. Honors VIXD_SOCK.
run-mac: bundle-mac
ifeq ($(shell uname),Darwin)
	open "$(MAC_APP)"
else
	@echo "run-mac: macOS + Swift toolchain required"; exit 1
endif


# Apply local patches to vendored dependencies.
# Run this after `go mod vendor` whenever dependencies are updated.
patch-deps:
	@for p in patches/*.patch; do \
		echo "Applying $$p..."; \
		patch -p1 --forward --batch --reject-file=- < "$$p" || true; \
	done

# Copy C source files for CGo dependencies that go mod vendor omits.
# tree-sitter grammars keep their C parsers in subdirs outside bindings/go/,
# so we pull them directly from the module cache after vendoring.
vendor-cgo-sources:
	$(eval MODCACHE := $(shell go env GOMODCACHE))
	@echo "Copying C sources for go-tree-sitter..."
	$(eval TS_CORE := $(shell go list -m -f '{{.Path}}@{{.Version}}' github.com/tree-sitter/go-tree-sitter))
	cp -r $(MODCACHE)/$(TS_CORE)/include vendor/github.com/tree-sitter/go-tree-sitter/
	cp -r $(MODCACHE)/$(TS_CORE)/src     vendor/github.com/tree-sitter/go-tree-sitter/
	cp    $(MODCACHE)/$(TS_CORE)/*.c     vendor/github.com/tree-sitter/go-tree-sitter/
	cp    $(MODCACHE)/$(TS_CORE)/*.h     vendor/github.com/tree-sitter/go-tree-sitter/
	@echo "Copying C sources for tree-sitter grammars..."
	@for mod in \
	    github.com/tree-sitter/tree-sitter-bash \
	    github.com/tree-sitter/tree-sitter-c \
	    github.com/tree-sitter/tree-sitter-c-sharp \
	    github.com/tree-sitter/tree-sitter-cpp \
	    github.com/tree-sitter/tree-sitter-css \
	    github.com/tree-sitter/tree-sitter-go \
	    github.com/tree-sitter/tree-sitter-html \
	    github.com/tree-sitter/tree-sitter-java \
	    github.com/tree-sitter/tree-sitter-javascript \
	    github.com/tree-sitter/tree-sitter-json \
	    github.com/tree-sitter/tree-sitter-python \
	    github.com/tree-sitter/tree-sitter-ruby \
	    github.com/tree-sitter/tree-sitter-rust \
	    github.com/tree-sitter-grammars/tree-sitter-kotlin \
	    github.com/gridlhq-dev/tree-sitter-swift; do \
	  ver=$$(go list -m -f '{{.Version}}' $$mod 2>/dev/null) && \
	  src=$(MODCACHE)/$$mod@$$ver && \
	  dst=vendor/$$mod && \
	  [ -d $$src/src ] && cp -r $$src/src $$dst/ && echo "  $$mod" || true; \
	done
	@echo "Copying C sources for tree-sitter-php (non-standard layout)..."
	$(eval TS_PHP := $(shell go list -m -f '{{.Path}}@{{.Version}}' github.com/tree-sitter/tree-sitter-php))
	cp -r $(MODCACHE)/$(TS_PHP)/php      vendor/github.com/tree-sitter/tree-sitter-php/
	cp -r $(MODCACHE)/$(TS_PHP)/php_only vendor/github.com/tree-sitter/tree-sitter-php/
	cp -r $(MODCACHE)/$(TS_PHP)/common   vendor/github.com/tree-sitter/tree-sitter-php/
	@echo "Copying C sources for tree-sitter-typescript (non-standard layout)..."
	$(eval TS_TS := $(shell go list -m -f '{{.Path}}@{{.Version}}' github.com/tree-sitter/tree-sitter-typescript))
	cp -r $(MODCACHE)/$(TS_TS)/typescript vendor/github.com/tree-sitter/tree-sitter-typescript/
	cp -r $(MODCACHE)/$(TS_TS)/tsx        vendor/github.com/tree-sitter/tree-sitter-typescript/
	cp -r $(MODCACHE)/$(TS_TS)/common     vendor/github.com/tree-sitter/tree-sitter-typescript/
	@# Make all copied C sources writable so a future `go mod vendor` can remove them cleanly.
	@chmod -R u+w vendor/

# Update a Go dependency, re-vendor, and re-apply patches.
# Usage: make update-deps PKG=charm.land/bubbles/v2@latest
update-deps:
	@[ "$(PKG)" ] || ( echo "Usage: make update-deps PKG=module@version"; exit 1 )
	go get $(PKG)
	go mod tidy
	@# Make vendor fully writable before go mod vendor rewrites it.
	@# (CGo sources copied from the module cache are read-only by default.)
	@[ -d vendor ] && chmod -R u+w vendor/ || true
	go mod vendor
	$(MAKE) vendor-cgo-sources
	$(MAKE) patch-deps
	@echo "Done. Review vendor/ changes, resolve any patch conflicts, then commit."

# Run the full test suite
test:
	go test ./...

# Number of benchmark repetitions for benchstat (higher = stabler, slower).
COUNT ?= 10

# Generate/validate the on-disk benchmark corpora (idempotent; gitignored).
# Sizing knobs: PERF_BIG_MB (MiB per big file, default 20), PERF_HUGE=1 (1M files).
perf-corpus:
	go run ./cmd/perftool gen-corpus

# Run the performance benchmarks, write perf/results/<commit>.txt, and print a
# benchstat comparison vs the baseline and the previous committed result.
# Does NOT commit — `make release` records the result on release.
test-perf:
	COUNT=$(COUNT) go run ./cmd/perftool run

# Run the benchmarks once and write perf/results/baseline.txt. Commit it as the
# frozen reference (first-run baseline).
perf-baseline:
	COUNT=$(COUNT) go run ./cmd/perftool baseline

# Fast breakage guard: run every benchmark exactly once (-benchtime=1x).
perf-smoke:
	go run ./cmd/perftool smoke

# Full pre-release verification: unit tests, containerized e2e, then perf.
# e2e requires Docker and is slow (see the test-e2e target).
test-all: test test-e2e test-perf

# Map the host arch to Go's GOARCH naming for the e2e container.
E2E_ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

# Run the containerised end-to-end suite. PRE-RELEASE GATE — slow; runs the real
# vix TUI + vixd against a mock LLM inside Docker, exercises the Landlock
# sandbox, and writes an HTML report (e2e/out/report/index.html) + zip.
# Requires Docker. See e2e/README.md.
# build.sh runs with --force: its default staleness check keys only on the git
# commit, so uncommitted changes (the normal dev loop) would otherwise ship a
# stale daemon into the image. The e2e gate must always test the current source.
test-e2e:
	./script/build.sh --force
	./e2e/build-e2e.sh $(E2E_ARCH)
	docker build -t vix-e2e -f e2e/Dockerfile .
	mkdir -p e2e/out
	@docker run --rm --network none \
	  --security-opt seccomp=e2e/seccomp-landlock.json \
	  -v "$(CURDIR)/e2e/out:/out" vix-e2e; \
	rc=$$?; \
	echo "==> Report: e2e/out/report/index.html  (zip: e2e/out/e2e-report.zip)"; \
	(open e2e/out/report/index.html || xdg-open e2e/out/report/index.html) >/dev/null 2>&1 || true; \
	exit $$rc

# Sharded e2e run: fan the suite across SHARDS isolated containers in parallel,
# each writing to e2e/out/shard-<k>/report, then merge + render + zip once on the
# host. Usage: make test-e2e-sharded SHARDS=4
SHARDS ?= 2
test-e2e-sharded:
	./e2e/build-e2e.sh $(E2E_ARCH)
	docker build -t vix-e2e -f e2e/Dockerfile .
	@rm -rf e2e/out/shard-* e2e/out/report e2e/out/e2e-report.zip
	@pids=""; \
	for k in $$(seq 0 $$(($(SHARDS)-1))); do \
	  mkdir -p e2e/out/shard-$$k; \
	  echo "==> launching shard $$k/$(SHARDS)"; \
	  docker run --rm --network none \
	    --security-opt seccomp=e2e/seccomp-landlock.json \
	    -e SHARD_INDEX=$$k -e SHARD_TOTAL=$(SHARDS) \
	    -e VIX_E2E_REPORT=/out/shard-$$k/report \
	    -v "$(CURDIR)/e2e/out:/out" vix-e2e & \
	  pids="$$pids $$!"; \
	done; \
	rc=0; \
	for pid in $$pids; do wait $$pid || rc=1; done; \
	exit $$rc
	cd e2e && go run ./cmd/report merge $(addprefix --in out/shard-,$(addsuffix /report,$(shell seq 0 $$(($(SHARDS)-1))))) --out out/report
	cd e2e && go run ./cmd/report zip --in out/report --out out/e2e-report.zip
	@echo "==> Merged report: e2e/out/report/index.html  (zip: e2e/out/e2e-report.zip)"

# Publish a release. Usage: make release VERSION=v1.2.3
# Build these versions: darwin-arm64 + linux-amd64 + linux-arm64, Docker for Linux
release:
	@[ "$(VERSION)" ] || ( echo "Usage: make release VERSION=v1.x.x"; exit 1 )
	$(MAKE) build-web
	@if [ -n "$$(git status --porcelain internal/daemon/web/dist)" ]; then \
		git add internal/daemon/web/dist && \
		git commit -m "chore: rebuild web dist for $(VERSION)"; \
	fi
	@# Commit this release's recorded benchmark results (perf/results/<commit>.txt).
	go run ./cmd/perftool record
	./script/release.sh $(VERSION) --repo get-vix/vix
