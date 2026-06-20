# AnhurDB Claude-memory plugin — build the hook engine.
#
# The hooks call ./bin/anhur-claude-memory (a single static binary that uses the AnhurDB Go SDK).
# Build once after install: `make build`.

BINARY := bin/anhur-claude-memory
PKG    := ./cmd/anhur-claude-memory

.PHONY: build clean tidy

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) $(PKG)

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
