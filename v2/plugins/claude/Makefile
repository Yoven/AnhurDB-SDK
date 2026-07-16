# AnhurDB Claude-memory plugin — build the hook engine.
#
# Shipping model: the marketplace-installed plugin runs with ZERO Go toolchain.
# bin/anhur-claude-memory is a committed shell wrapper that execs the prebuilt
# per-platform binary bin/anhur-claude-memory-<os>-<arch>. `make release-binaries`
# cross-compiles the whole matrix (CI runs it; see .github/workflows/release-plugin.yml)
# and the outputs are committed so an install needs no compiler.
#
# -trimpath (no absolute build path) + -buildvcs=false (no git commit/dirty stamp,
# which otherwise changes the bytes on every commit) make the builds reproducible,
# so CI can gate on "committed binaries == a fresh build" (git diff --exit-code bin/).

PKG     := ./cmd/anhur-claude-memory
WRAPPER := bin/anhur-claude-memory
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
NATIVE  := bin/anhur-claude-memory-$(GOOS)-$(GOARCH)
GOFLAGS := -trimpath -ldflags=-s -ldflags=-w
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
#
# Junior Tip [why not just point the hooks at bin/, 2026-07-16]: because bin/ lives inside a
# git worktree. Hooks that reference a path under version control break the moment anyone
# rebases, renames, or checks out another branch — the memory silently stops recalling and
# nothing announces it. That is exactly how this setup broke once: a marketplace registered
# against the live worktree went dangling when a manifest was renamed, and the engine went
# unloaded for hours before anyone noticed. Copying the binary OUT of the repo decouples the
# user's long-term memory from day-to-day development of the repo that produces it.
install: build
	install -d $(PREFIX)/bin
	install -m 755 $(NATIVE) $(PREFIX)/bin/anhur-claude-memory
	@echo "installed -> $(PREFIX)/bin/anhur-claude-memory"
	@echo "next: point three hooks (SessionStart/Stop/SessionEnd) at it — see README → Install → Option A"

# release-binaries cross-compiles every shipped platform into bin/. The outputs are
# committed; a marketplace install just runs the wrapper, no toolchain required.
release-binaries:
	@for platform in $(PLATFORMS); do \
	  goos=$${platform%/*}; goarch=$${platform#*/}; \
	  echo "build $$goos/$$goarch"; \
	  CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath -buildvcs=false -ldflags="-s -w" \
	    -o bin/anhur-claude-memory-$$goos-$$goarch $(PKG) || exit 1; \
	done

# deploy builds the native binary, then overwrites the installed plugin-cache bin/
# (wrapper + this platform's binary) that the Claude Code hooks actually run. Kills
# the recurring "rebuilt source but the cache binary went stale" gotcha. The cache
# path is version-agnostic (glob tolerates a version bump); a missing cache dir
# fails loud rather than silently no-op'ing.
deploy: build
	@cachebin="$$(ls -d $(HOME)/.claude/plugins/cache/anhur/anhurdb-memory/*/bin 2>/dev/null | head -1)"; \
	if [ -z "$$cachebin" ]; then echo "deploy: no installed plugin-cache bin dir found — is the plugin installed?" >&2; exit 1; fi; \
	cp $(WRAPPER) "$$cachebin/anhur-claude-memory" && \
	cp $(NATIVE) "$$cachebin/anhur-claude-memory-$(GOOS)-$(GOARCH)" && \
	echo "deployed wrapper + $(GOOS)/$(GOARCH) binary -> $$cachebin"

tidy:
	go mod tidy

# clean removes only the local native build; the committed release binaries stay.
clean:
	rm -f $(NATIVE)
