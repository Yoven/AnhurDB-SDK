# AnhurDB SDK — Architecture

## Purpose

This repository ships the official client libraries for AnhurDB v2:

- `v2/python` — async Python SDK (`anhurdb`)
- `v2/typescript` — TypeScript SDK (`anhurdb`)
- `v2/golang` — Go module `github.com/anhurdb/sdk-go/v2`
- `v2/plugins` — optional IDE memory plugins

All three SDKs expose a single **`Memory`** client with the same REST surface.
See [`v2/PARITY_SPEC.md`](../../v2/PARITY_SPEC.md) for the canonical method list.

## Design

### Single client, REST transport

Each SDK is a thin HTTP client over the public AnhurDB REST API. Processing
runs on the server; the SDK does not duplicate business logic.

Authentication uses the `X-API-Key` header. Multi-tenant deployments may also
send `X-Tenant-ID`.

### Parity invariant

A change that adds or modifies a public `Memory` method must land in all three
languages in the same release.

### Container tag

When `user_id` / `userId` / `WithUserID` is omitted, the SDK derives a stable
container tag from the API key hash. The algorithm is identical across Python,
TypeScript, and Go.

## Repository layout

```
AnhurDB-SDK/
├── .github/workflows/     CI and release automation
├── docs/                  Public API and integration guides
├── v2/
│   ├── PARITY_SPEC.md
│   ├── python/
│   ├── typescript/
│   ├── golang/
│   └── plugins/
└── README.md
```

## Open Beta

- API keys: `https://anhur.yoven.ai/app`
- Data plane: `https://anhurdb.yoven.ai`
