# AnhurDB Hermes-memory plugin — build the hook engine.
#
# The hooks call ./bin/anhur-hermes-memory (a single static binary that uses the AnhurDB Go SDK).
# It is the SAME engine as the claude plugin (shared github.com/anhurdb/anhur-memory-core), pointed
# at the hermes memory identity. Build once after install: `make build`.

BINARY := bin/anhur-hermes-memory
PKG    := ./cmd/anhur-hermes-memory

.PHONY: build clean tidy

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) $(PKG)

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
