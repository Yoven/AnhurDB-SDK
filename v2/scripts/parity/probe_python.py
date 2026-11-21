#!/usr/bin/env python3
"""Live parity probe for the Python AnhurDB SDK (mirrors Go/TS probes)."""

from __future__ import annotations

import asyncio
import os
import sys
import time
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../python"))

from anhurdb import (  # noqa: E402
    AddResult,
    Memory,
    MemoryType,
    sessions_all,
)
from probe_python_l2 import run_l2_section  # noqa: E402


def emit(operation: str, ok: bool, detail: str = "", err: Any = None) -> None:
    emit_status(operation, "PASS" if ok else "FAIL", detail, err)


def emit_status(operation: str, status: str, detail: str = "", err: Any = None) -> None:
    """One RESULT line; status is PASS | FAIL | SKIP (run_all.sh parses it)."""
    err_part = f" err={err}" if err is not None else ""
    print(f'RESULT sdk=python op="{operation}" status={status}{err_part} detail={detail}')


def add_record_id(add_res: Any) -> int:
    # Junior Tip [typed AddResult]: add()/create() return the pydantic
    # AddResult (id only on the /records door, records[] on both); the old dict
    # branch is kept for a server/SDK that still answers a plain mapping.
    if isinstance(add_res, AddResult):
        if add_res.id:
            return int(add_res.id)
        if add_res.records and add_res.records[0].id:
            return int(add_res.records[0].id)
    if isinstance(add_res, dict):
        if add_res.get("id"):
            return int(add_res["id"])
        records = add_res.get("records") or []
        if records and isinstance(records[0], dict) and records[0].get("id"):
            return int(records[0]["id"])
    raise RuntimeError(f"no id in add result: {add_res!r}")


async def main() -> int:
    api_key = os.environ.get("ANHUR_API_KEY", "")
    if not api_key:
        print("FAIL: ANHUR_API_KEY required", file=sys.stderr)
        return 1

    base_url = os.environ.get("ANHUR_URL", "https://anhurdb.yoven.ai")
    tenant_id = os.environ.get("ANHUR_TENANT_ID") or f"sdk-py-parity-{int(time.time())}"
    print(f"SDK=python URL={base_url} TENANT={tenant_id}")

    fail_count = 0
    token = time.time_ns()

    async with Memory(api_key=api_key, url=base_url, tenant_id=tenant_id) as mem:
        try:
            health = await mem.health()
            emit("Health", True, f"status={health.get('status')}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("Health", False, err=exc)

        try:
            # Junior Tip [EnsureSession]: create_session must precede add on HEL1.
            registered_session_id = await mem.create_session()
            emit("CreateSession", True, f"id={registered_session_id}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("CreateSession", False, err=exc)
            return 1

        try:
            add_res = await mem.add(f"parity-py: AnhurDB SDK probe token={token}")
            record_id = add_record_id(add_res)
            emit("Add", True, f"id={record_id}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("Add", False, err=exc)
            return 1

        try:
            content = await mem.read_content(record_id)
            ok = "parity-py" in (content or "")
            emit("ReadContent", ok, f"len={len(content or '')}")
            fail_count += int(not ok)
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("ReadContent", False, err=exc)

        got: dict = {}
        try:
            got = await mem.get(record_id)
            emit("Get", True, f"id={got.get('id')} type={got.get('type')}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("Get", False, err=exc)

        try:
            create_session = str(got.get("uuid") or "") or mem.session_id
            create_res = await mem.create(
                create_session,
                f"parity-py create fact token={token}",
                type=MemoryType.FACT,
                score=8,
            )
            emit("Create", True, f"id={add_record_id(create_res)} session={create_session}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("Create", False, err=exc)

        # Junior Tip [ADR-0014]: sessions is MANDATORY in the search family —
        # sessions_all() means "every session in scope"; omitting it is an error.
        async_ops = [
            ("Search", mem.search("AnhurDB SDK probe", sessions_all())),
            ("Profile", mem.profile()),
            ("CountByType", mem.count_by_type()),
            ("ListSessions", mem.list_sessions()),
            ("Recent", mem.recent(limit=5)),
            ("SmartSearch", mem.smart_search("AnhurDB", sessions_all(), limit=5)),
            ("Recall", mem.recall("AnhurDB", sessions_all(), limit=5)),
        ]
        for op_name, coro in async_ops:
            try:
                value = await coro
                detail = f"type={type(value).__name__}"
                if isinstance(value, (list, dict)):
                    detail = f"n={len(value)}"
                emit(op_name, True, detail)
            except Exception as exc:  # noqa: BLE001
                fail_count += 1
                emit(op_name, False, err=exc)

        try:
            types = mem.list_types()
            emit("ListTypes", True, f"n={len(types)}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("ListTypes", False, err=exc)

        entity_base = f"paritychromepy{token}"
        try:
            first = await mem.upsert_entity(f"  {entity_base} ", "product", "parity probe")
            first_id = int(first.id or 0)
            emit(
                "UpsertEntity(caseA)",
                True,
                f"id={first_id} type={first.entity_type} name={first.name!r}",
            )
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("UpsertEntity(caseA)", False, err=exc)
            return 1

        try:
            second = await mem.upsert_entity(entity_base.upper(), "product", "parity probe")
            if int(second.id or 0) != first_id:
                fail_count += 1
                emit(
                    "UpsertEntity.dedup",
                    False,
                    detail=(
                        f"{first_id} vs {second.id} — "
                        "redeploy AnhurDB if server lacks NormalizeEntityName"
                    ),
                )
            else:
                emit("UpsertEntity.dedup", True, f"same_id={first_id}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("UpsertEntity.dedup", False, err=exc)

        try:
            await mem.link_record_entity(record_id, first_id, role="mentions")
            emit("LinkRecordEntity", True, "ok")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("LinkRecordEntity", False, err=exc)

        try:
            entities = await mem.get_record_entities(record_id)
            typed = sum(1 for entity in entities if entity.entity_type)
            if entities and typed == 0:
                fail_count += 1
                emit("GetRecordEntities.entity_type", False, "all empty")
            else:
                emit("GetRecordEntities", True, f"n={len(entities)} with_type={typed}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("GetRecordEntities", False, err=exc)

        try:
            listed = await mem.list_entities(limit=50, offset=0)
            entities = listed.entities
            typed = sum(1 for entity in entities if entity.entity_type)
            if entities and typed == 0:
                fail_count += 1
                emit("ListEntities.entity_type", False, "all empty")
            else:
                emit("ListEntities", True, f"n={len(entities)} with_type={typed}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("ListEntities", False, err=exc)

        try:
            found = await mem.search_entities(query=entity_base, entity_type="product", limit=10)
            emit("SearchEntities", True, f"n={len(found)}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("SearchEntities", False, err=exc)

        try:
            other = await mem.upsert_entity(f"parity-org-py-{token}", "organization")
            await mem.upsert_entity_edge(first_id, int(other.id or 0), "related_to", confidence=1.0)
            emit("UpsertEntityEdge", True, "ok")
            graph = await mem.get_entity_graph(first_id, depth=2)
            emit("EntityGraph", True, f"nodes={graph.node_count}")
            timeline = await mem.entity_timeline(first_id)
            emit("EntityTimeline", True, f"edges={timeline.edge_count}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("EntityGraphFamily", False, err=exc)

        try:
            await mem.get_grounding(record_id, max_depth=2)
            emit("GetGrounding", True, "ok")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("GetGrounding", False, err=exc)

        # L2/ADR-0033: tenant library by-id scope (probe_python_l2.py).
        fail_count += await run_l2_section(mem, token, emit_status)

    if fail_count:
        print(f"SUMMARY sdk=python FAIL count={fail_count}")
        return 1
    print("SUMMARY sdk=python PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
