#!/usr/bin/env python3
"""Live parity probe for the Python AnhurDB SDK (mirrors Go/TS probes)."""

from __future__ import annotations

import asyncio
import os
import sys
import time
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../python"))

from anhurdb import CreateRequest, EntityModel, Memory, MemoryType  # noqa: E402


def emit(operation: str, ok: bool, detail: str = "", err: Any = None) -> None:
    status = "PASS" if ok else "FAIL"
    err_part = f" err={err}" if err is not None else ""
    print(f'RESULT sdk=python op="{operation}" status={status}{err_part} detail={detail}')


def add_record_id(add_res: Any) -> int:
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
                CreateRequest(
                    uuid=create_session,
                    content=f"parity-py create fact token={token}",
                    type=MemoryType.FACT,
                    score=8,
                )
            )
            emit("Create", True, f"id={create_res.get('id')} session={create_session}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("Create", False, err=exc)

        async_ops = [
            ("Search", mem.search("AnhurDB SDK probe")),
            ("Profile", mem.profile()),
            ("CountByType", mem.count_by_type()),
            ("ListSessions", mem.list_sessions()),
            ("Recent", mem.recent(limit=5)),
            ("SmartSearch", mem.smart_search("AnhurDB", limit=5)),
            ("Recall", mem.recall("AnhurDB", limit=5)),
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
            first_id = int(first["id"])
            emit(
                "UpsertEntity(caseA)",
                True,
                f"id={first_id} type={first.get('entity_type')} name={first.get('name')!r}",
            )
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("UpsertEntity(caseA)", False, err=exc)
            return 1

        try:
            second = await mem.upsert_entity(entity_base.upper(), "product", "parity probe")
            if int(second["id"]) != first_id:
                fail_count += 1
                emit(
                    "UpsertEntity.dedup",
                    False,
                    detail=(
                        f"{first_id} vs {second['id']} — "
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
            typed = sum(1 for entity in entities if EntityModel.model_validate(entity).entity_type)
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
            entities = listed.get("entities", []) if isinstance(listed, dict) else listed
            typed = sum(1 for entity in entities if EntityModel.model_validate(entity).entity_type)
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
            entities = found.get("entities", found) if isinstance(found, dict) else found
            emit("SearchEntities", True, f"n={len(entities) if isinstance(entities, list) else '?'}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("SearchEntities", False, err=exc)

        try:
            other = await mem.upsert_entity(f"parity-org-py-{token}", "organization")
            await mem.upsert_entity_edge(first_id, int(other["id"]), "related_to", confidence=1.0)
            emit("UpsertEntityEdge", True, "ok")
            graph = await mem.get_entity_graph(first_id, depth=2)
            emit("EntityGraph", True, f"keys={list(graph)[:5]}")
            timeline = await mem.entity_timeline(first_id)
            emit("EntityTimeline", True, f"keys={list(timeline)[:5]}")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("EntityGraphFamily", False, err=exc)

        try:
            await mem.get_grounding(record_id, max_depth=2)
            emit("GetGrounding", True, "ok")
        except Exception as exc:  # noqa: BLE001
            fail_count += 1
            emit("GetGrounding", False, err=exc)

    if fail_count:
        print(f"SUMMARY sdk=python FAIL count={fail_count}")
        return 1
    print("SUMMARY sdk=python PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
