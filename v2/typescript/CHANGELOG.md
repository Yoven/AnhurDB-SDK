# TypeScript SDK Changelog

## Unreleased (2.0.1)

_Generated at 2026-07-15T01:21:22Z from `770a36dca15f3c1129b7e2a7618f27acb61eb386` → `HEAD`_

- fix(sdk): searchEntities sends q=; docs use organization not org (be79a07)
- docs: document Query Builder in Python, Go, and TypeScript SDKs (1ae72c8)
- feat(sdk): add session_id to ingest across ALL THREE SDKs + plugin (parity) (e603b94)
- refactor(sdk): make all 3 SDKs transparent HTTP transports (Go/Py/TS parity) (af90706)
- feat(sdk): AppendRelatedIDs across all 3 SDKs, mirroring AppendMainIDs (parity #13) (97c19c4)
- fix(sdk): search_by_type reads the correct 'records' envelope key (all 3 SDKs) (eb3324f)
- fix(sdk,ts): await tagReady before createInSession metadata (container_tag mis-routing) (db83bd6)
- fix(sdk): unify SearchResult to nested {record, similarity} across Go/Python/TS (8cde735)
- fix(sdk): align Go/Python/TS parity — recent route, session_uuid, typed search (ea4be89)
- feat(sdk): WalkSemantic goal-directed target across Go/Py/TS (parity) (9d393c2)
- feat(plugins): dogfood AnhurDB as Claude Code LTM + SDK hardening/parity (c28f109)
- fix(sdk-ts): readContent devolve conteúdo cru (paridade com Go/Python) (9e8bb9b)
- fix(ts-sdk): retry idempotent writes on transient cluster errors (Bug 3) (ef22656)
- fix(ts-sdk): stop dropping score/type/metadata on add() (Bug 2, parity) (26f81fc)
- fix(ts-sdk): emit real ESM and repair tsc toolchain (Bug 1) (2b4c8c8)
- feat(sdk): Go/Python/TS parity — new methods + metadata corruption fix (db580ec)
- feat: SDK fixes — Python AnhurClient, Go randomHex/timeout, TS CI, PyPI publish (907de0b)

## 2.0.0

- Initial v2 release: unified `Memory` API parity across Python, TypeScript, and Go.
- Open Beta default endpoint: `https://anhurdb.yoven.ai`.
- Full MCP-aligned surface: search, query AST, manifests, entities, uploads, temporal versioning.

