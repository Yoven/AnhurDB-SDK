# Python SDK Changelog

## Unreleased (2.0.2)

_Generated at 2026-07-15T01:21:22Z from `v2/python/v2.0.1` → `HEAD`_

- fix(sdk): searchEntities sends q=; docs use organization not org (be79a07)
- docs: document Query Builder in Python, Go, and TypeScript SDKs (1ae72c8)
- feat(sdk): add session_id to ingest across ALL THREE SDKs + plugin (parity) (e603b94)
- refactor(sdk): make all 3 SDKs transparent HTTP transports (Go/Py/TS parity) (af90706)
- feat(sdk): AppendRelatedIDs across all 3 SDKs, mirroring AppendMainIDs (parity #13) (97c19c4)
- fix(sdk): search_by_type reads the correct 'records' envelope key (all 3 SDKs) (eb3324f)
- fix(sdk,py): read-model enum tolerance + null-records coalesce (crash fixes) (bf5fb87)
- fix(sdk): recent() returns the FULL typed record across Go/Python/TS (60a676c)
- fix(sdk): unify SearchResult to nested {record, similarity} across Go/Python/TS (8cde735)
- fix(sdk): align Go/Python/TS parity — recent route, session_uuid, typed search (ea4be89)
- feat(sdk): WalkSemantic goal-directed target across Go/Py/TS (parity) (9d393c2)
- fix(plugin,test): log flush errors + de-hardcode API key from env (4ac41a7)
- feat(plugins): dogfood AnhurDB as Claude Code LTM + SDK hardening/parity (c28f109)
- test(python): cover score/type/metadata persistence, retry, plain-text content (3711d24)
- fix(python): retry transient cluster 500s and stop wrapping plain-text content (c54685c)
- fix(python): add() must persist score/type/metadata, not drop them on ingest (4bf0aa5)
- feat(sdk): Go/Python/TS parity — new methods + metadata corruption fix (db580ec)
- feat: SDK fixes — Python AnhurClient, Go randomHex/timeout, TS CI, PyPI publish (907de0b)
- feat: so many fixes (0e74900)
- feat: mcp integration (ba6a991)

## 2.0.1

- Initial v2 release: unified `Memory` API parity across Python, TypeScript, and Go.
- Open Beta default endpoint: `https://anhurdb.yoven.ai`.
- Full MCP-aligned surface: search, query AST, manifests, entities, uploads, temporal versioning.

