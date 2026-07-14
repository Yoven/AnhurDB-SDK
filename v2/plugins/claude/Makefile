# AnhurDB Claude-memory plugin — build the hook engine.
#
# The hooks call ./bin/anhur-claude-memory (a single static binary that uses the AnhurDB Go SDK).
# Build once after install: `make build`.

BINARY := bin/anhur-claude-memory
PKG    := ./cmd/anhur-claude-memory

.PHONY: build clean tidy deploy

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) $(PKG)

# deploy builds, then overwrites the installed plugin-cache binary the Claude Code hooks
# actually run. This kills the recurring "rebuilt the source but the cache binary went
# stale" gotcha — after a change, `make deploy` is the single command to ship it. The cache
# path is discovered version-agnostically (the glob tolerates a plugin version bump), and a
# missing cache binary fails loud rather than silently no-op'ing.
deploy: build
	@cache="$$(ls $(HOME)/.claude/plugins/cache/anhur/anhurdb-memory/*/bin/anhur-claude-memory 2>/dev/null | head -1)"; \
	if [ -z "$$cache" ]; then echo "deploy: no installed plugin-cache binary found — is the plugin installed?" >&2; exit 1; fi; \
	cp $(BINARY) "$$cache" && echo "deployed $(BINARY) -> $$cache"

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
