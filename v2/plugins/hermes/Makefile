# AnhurDB Hermes-memory plugin — build the hook engine.
#
# Same engine as the claude plugin (shared github.com/anhurdb/anhur-memory-core), pointed at the
# hermes memory identity. This Makefile deliberately mirrors ../claude/Makefile target for target:
# the two plugins share one engine, so a build/ship rule that exists for one and not the other is
# exactly how they drift.
#
# Shipping model: the marketplace-installed plugin runs with ZERO Go toolchain.
# bin/anhur-hermes-memory is a committed shell wrapper that execs the prebuilt
# per-platform binary bin/anhur-hermes-memory-<os>-<arch>. `make release-binaries`
# cross-compiles the whole matrix (CI runs it; see .github/workflows/release-plugin.yml)
# and the outputs are committed so an install needs no compiler.
#
# -trimpath (no absolute build path) + -buildvcs=false (no git commit/dirty stamp,
# which otherwise changes the bytes on every commit) make the builds reproducible,
# so CI can gate on "committed binaries == a fresh build" (git diff --exit-code bin/).

PKG     := ./cmd/anhur-hermes-memory
WRAPPER := bin/anhur-hermes-memory
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
NATIVE  := bin/anhur-hermes-memory-$(GOOS)-$(GOARCH)
# Platforms whose prebuilt binaries ship (committed) in bin/. Keep in sync with the
# wrapper's os/arch mapping and the CI matrix.
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
# PREFIX is where `make install` puts the engine for the marketplace-free setup.
PREFIX  ?= $(HOME)/.local

.PHONY: build install release-binaries deploy tidy clean

# build compiles the binary for THIS machine (local dogfood / dev).
build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o $(NATIVE) $(PKG)

# install copies the engine to a STABLE path outside this git worktree, for the
# marketplace-free setup (three hooks in settings.json invoking this binary directly).
# See ../claude/Makefile for why a hook must never reference a path under version control.
install: build
	install -d $(PREFIX)/bin
	install -m 755 $(NATIVE) $(PREFIX)/bin/anhur-hermes-memory
	@echo "installed -> $(PREFIX)/bin/anhur-hermes-memory"
	@echo "next: point three hooks (SessionStart/Stop/SessionEnd) at it — see README"

# release-binaries cross-compiles every shipped platform into bin/. The outputs are
# committed; a marketplace install just runs the wrapper, no toolchain required.
release-binaries:
	@for platform in $(PLATFORMS); do \
	  goos=$${platform%/*}; goarch=$${platform#*/}; \
	  echo "build $$goos/$$goarch"; \
	  CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath -buildvcs=false -ldflags="-s -w" \
	    -o bin/anhur-hermes-memory-$$goos-$$goarch $(PKG) || exit 1; \
	done

# deploy builds the native binary, then overwrites the installed plugin-cache bin/
# (wrapper + this platform's binary) that the Claude Code hooks actually run. The cache
# path is version-agnostic (glob tolerates a version bump); a missing cache dir fails
# loud rather than silently no-op'ing.
deploy: build
	@cachebin="$$(ls -d $(HOME)/.claude/plugins/cache/anhur/anhurdb-memory-hermes/*/bin 2>/dev/null | head -1)"; \
	if [ -z "$$cachebin" ]; then echo "deploy: no installed plugin-cache bin dir found — is the plugin installed?" >&2; exit 1; fi; \
	cp $(WRAPPER) "$$cachebin/anhur-hermes-memory" && \
	cp $(NATIVE) "$$cachebin/anhur-hermes-memory-$(GOOS)-$(GOARCH)" && \
	echo "deployed wrapper + $(GOOS)/$(GOARCH) binary -> $$cachebin"

tidy:
	go mod tidy

# clean removes only the local native build; the committed release binaries stay.
clean:
	rm -f $(NATIVE)
