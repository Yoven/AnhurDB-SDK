# Cross-SDK live parity probes

Exercises Go / Python / TypeScript `Memory` clients against one AnhurDB with the
same operations (health, CRUD, search, entities, graph, grounding).

## Run

```bash
export ANHUR_API_KEY=...
export ANHUR_URL=https://anhurdb.yoven.ai   # optional
bash AnhurDB-SDK/v2/scripts/parity/run_all.sh
```

Or individually:

```bash
cd AnhurDB-SDK/v2/golang && go run ./scripts/parity_probe/
python3 AnhurDB-SDK/v2/scripts/parity/probe_python.py
cd AnhurDB-SDK/v2/typescript && npx tsx ../scripts/parity/probe_typescript.ts
```

## What must match

- `ListEntities` / `GetRecordEntities` expose non-empty `entity_type`
- Upsert case variants (`Foo` / `FOO` / ` foo `) collapse to **one** entity id
  (server `NormalizeEntityName`: trim + lowercase)
- Write→read→search→entity link→graph paths succeed on all three SDKs
