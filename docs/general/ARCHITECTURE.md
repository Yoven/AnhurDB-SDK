# AnhurDB SDK — Architecture

## Purpose

This repository ships the official client libraries for AnhurDB v2:

- `v2/python` — async Python SDK (`anhurdb`)
- `v2/typescript` — TypeScript SDK (`anhurdb`)
- `v2/golang` — Go module `github.com/Yoven/AnhurDB-SDK/v2/golang/v2`
- `v2/plugins` — optional IDE memory plugins

All three SDKs expose a single **`Memory`** client with the same REST surface.
See [`v2/PARITY_SPEC.md`](../../v2/PARITY_SPEC.md) for the canonical method list.

## Design

### Single client, REST transport

Each SDK is a thin HTTP client over the public AnhurDB REST API. Processing
runs on the server; the SDK does not duplicate business logic.

Authentication uses the `X-API-Key` header. Multi-tenant deployments may also
send `X-Tenant-ID`.

### Two doors into one data plane

```
Python / TypeScript / Go SDK ──REST──► smart-router ──► AnhurDB data plane
                                                          ▲
MCP client (Claude, Cursor, …) ──MCP──► AnhurDB MCP ──gRPC┘
                                        server (22 tools)
```

The **SDKs enter over REST** through the smart-router. The **MCP server talks gRPC**
to the data plane — since 2026-07-28 that is true for all 22 tools; the last four REST
holdouts (`explain_record`, `query(ast=)`, the `read_entities` walk, `upload_file`)
migrated in ADR-0013 Fase 5. This is a transport change on the MCP side only; the
public REST API kept its shape.

### One rule, two doors

The rules that both ports must agree on — the AST column whitelist and operator set,
the explain depth bounds, grounding traversal, the upload plane resolution, the
`sessions` grammar — live in `AnhurDB/server/service/*` and are called by the REST
handler and the gRPC service alike. Neither port re-implements them, so the two cannot
answer differently for the same request. Behavioural fixes in that layer therefore
reach SDK callers too: the AST validation tightening of 2026-07-28 is documented in
[`docs/api/REST_API.md`](../api/REST_API.md#post-apiv1query--structured-ast-query).

### Explicit scoping, no implicit widening

Search takes two orthogonal arguments: `scope` selects the memory plane (which store
is reachable at all) and `sessions` selects the subset inside it. `sessions` is
mandatory — an absent or empty filter is rejected rather than defaulted to "all"
(ADR-0014). The rejected cases are exactly the callers that did not say what they
wanted; guessing on their behalf is how a scoping bug becomes a data-exposure bug.

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
├── .claude-plugin/
│   └── marketplace.json   Claude Code marketplace manifest (name: "anhur")
├── .github/workflows/     CI and release automation
├── docs/
│   ├── api/REST_API.md
│   ├── general/ARCHITECTURE.md
│   └── claude/CLAUDE_ANHURDB_INTEGRATION.md
├── v2/
│   ├── PARITY_SPEC.md
│   ├── python/
│   ├── typescript/
│   ├── golang/
│   ├── scripts/
│   └── plugins/           claude/, hermes/, core/, docs/
└── README.md
```

`marketplace.json` lives at the **repository root** and its `source` paths
(`./v2/plugins/claude`, `./v2/plugins/hermes`) are read from the live worktree by the
`directory` marketplace source. Moving or renaming either the manifest or those
directories breaks plugin installs silently (`failed to load: cache-miss`) — treat
them as production interfaces, not layout.

## Open Beta

- API keys: `https://anhur.yoven.ai/app`
- Data plane: `https://anhurdb.yoven.ai`
