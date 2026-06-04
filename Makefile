.PHONY: build build-web build-all pull push test release run-server run-ui patch-deps vendor-cgo-sources update-deps web-source

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
	./bin/vix --pprof-port 6061 2>/tmp/vix-debug.log -disable-automatic-directory-access -disable-automatic-write-permission

# Local dev build — current platform only, fast
build: build-d build-x

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

# Publish a release. Usage: make release VERSION=v1.2.3
# Build these versions: darwin-arm64 + linux-amd64 + linux-arm64, Docker for Linux
release:
	@[ "$(VERSION)" ] || ( echo "Usage: make release VERSION=v1.x.x"; exit 1 )
	$(MAKE) build-web
	@if [ -n "$$(git status --porcelain internal/daemon/web/dist)" ]; then \
		git add internal/daemon/web/dist && \
		git commit -m "chore: rebuild web dist for $(VERSION)"; \
	fi
	./script/release.sh $(VERSION) --repo get-vix/vix
