/**
 * Live parity probe for the TypeScript AnhurDB SDK (mirrors Go/Python probes).
 *
 *   ANHUR_API_KEY=... npx tsx AnhurDB-SDK/v2/scripts/parity/probe_typescript.ts
 */
import { Memory } from "../../typescript/src/index.js";

function emit(operation: string, ok: boolean, detail = "", err?: unknown): void {
  const status = ok ? "PASS" : "FAIL";
  const errPart = err !== undefined ? ` err=${String(err)}` : "";
  console.log(`RESULT sdk=typescript op="${operation}" status=${status}${errPart} detail=${detail}`);
}

function addRecordId(addRes: { id?: number; records?: Array<{ id: number }> }): number {
  if (addRes.id) return addRes.id;
  if (addRes.records?.[0]?.id) return addRes.records[0].id;
  throw new Error(`no id in add result: ${JSON.stringify(addRes)}`);
}

async function main(): Promise<number> {
  const apiKey = process.env.ANHUR_API_KEY ?? "";
  if (!apiKey) {
    console.error("FAIL: ANHUR_API_KEY required");
    return 1;
  }

  const baseUrl = process.env.ANHUR_URL ?? "https://anhurdb.yoven.ai";
  const tenantId =
    process.env.ANHUR_TENANT_ID ?? `sdk-ts-parity-${Math.floor(Date.now() / 1000)}`;
  console.log(`SDK=typescript URL=${baseUrl} TENANT=${tenantId}`);

  let failCount = 0;
  const token = Date.now();
  const mem = new Memory({ apiKey, url: baseUrl, tenantId });

  try {
    const health = await mem.health();
    emit("Health", true, `status=${(health as { status?: string }).status ?? "ok"}`);
  } catch (err) {
    failCount++;
    emit("Health", false, "", err);
  }

  let recordId = 0;
  try {
    const addRes = await mem.add(`parity-ts: AnhurDB SDK probe token=${token}`);
    recordId = addRecordId(addRes);
    emit("Add", true, `id=${recordId}`);
  } catch (err) {
    failCount++;
    emit("Add", false, "", err);
    return 1;
  }

  try {
    const content = await mem.readContent(recordId);
    const ok = (content ?? "").includes("parity-ts");
    emit("ReadContent", ok, `len=${(content ?? "").length}`);
    if (!ok) failCount++;
  } catch (err) {
    failCount++;
    emit("ReadContent", false, "", err);
  }

  let recordUuid = "";
  try {
    const got = await mem.get(recordId);
    recordUuid = String((got as { uuid?: string }).uuid ?? "");
    emit("Get", true, `id=${(got as { id?: number }).id} type=${(got as { type?: string }).type}`);
  } catch (err) {
    failCount++;
    emit("Get", false, "", err);
  }

  try {
    const createRes = await mem.create(`parity-ts create fact token=${token}`, {
      type: "fact",
      score: 8,
      sessionUuid: recordUuid || undefined,
    });
    emit("Create", true, `id=${createRes.id} session=${recordUuid}`);
  } catch (err) {
    failCount++;
    emit("Create", false, "", err);
  }

  const ops: Array<[string, () => Promise<unknown>]> = [
    ["Search", () => mem.search("AnhurDB SDK probe")],
    ["Profile", () => mem.profile()],
    ["CountByType", () => mem.countByType()],
    ["ListSessions", () => mem.listSessions()],
    ["Recent", () => mem.recent(5)],
    ["SmartSearch", () => mem.smartSearch("AnhurDB", 5)],
    ["Recall", () => mem.recall("AnhurDB", 5)],
    ["ListTypes", async () => mem.listTypes()],
  ];
  for (const [name, run] of ops) {
    try {
      const value = await run();
      const detail = Array.isArray(value)
        ? `n=${value.length}`
        : typeof value === "object" && value !== null
          ? `keys=${Object.keys(value as object).length}`
          : "ok";
      emit(name, true, detail);
    } catch (err) {
      failCount++;
      emit(name, false, "", err);
    }
  }

  const entityBase = `paritychromets${token}`;
  let firstId = 0;
  try {
    const first = await mem.upsertEntity(`  ${entityBase} `, {
      entityType: "product",
      summary: "parity probe",
    });
    firstId = first.id;
    emit("UpsertEntity(caseA)", true, `id=${firstId}`);
  } catch (err) {
    failCount++;
    emit("UpsertEntity(caseA)", false, "", err);
    return 1;
  }

  try {
    const second = await mem.upsertEntity(entityBase.toUpperCase(), {
      entityType: "product",
      summary: "parity probe",
    });
    if (second.id !== firstId) {
      failCount++;
      emit(
        "UpsertEntity.dedup",
        false,
        `${firstId} vs ${second.id} — redeploy AnhurDB if server lacks NormalizeEntityName`,
      );
    } else {
      emit("UpsertEntity.dedup", true, `same_id=${firstId}`);
    }
  } catch (err) {
    failCount++;
    emit("UpsertEntity.dedup", false, "", err);
  }

  try {
    await mem.linkRecordEntity(recordId, firstId, "mentions");
    emit("LinkRecordEntity", true, "ok");
  } catch (err) {
    failCount++;
    emit("LinkRecordEntity", false, "", err);
  }

  try {
    const linked = await mem.getRecordEntities(recordId);
    const typed = linked.filter((entity) => entity.entity_type).length;
    if (linked.length > 0 && typed === 0) {
      failCount++;
      emit("GetRecordEntities.entity_type", false, "all empty — SDK type field mismatch");
    } else {
      emit("GetRecordEntities", true, `n=${linked.length} with_type=${typed}`);
    }
  } catch (err) {
    failCount++;
    emit("GetRecordEntities", false, "", err);
  }

  try {
    const listed = await mem.listEntities(50, 0);
    const typed = listed.entities.filter((entity) => entity.entity_type).length;
    if (listed.entities.length > 0 && typed === 0) {
      failCount++;
      emit("ListEntities.entity_type", false, "all empty");
    } else {
      emit("ListEntities", true, `n=${listed.entities.length} with_type=${typed}`);
    }
  } catch (err) {
    failCount++;
    emit("ListEntities", false, "", err);
  }

  try {
    const found = await mem.searchEntities(entityBase, "product", 10);
    emit("SearchEntities", true, `n=${found.length}`);
  } catch (err) {
    failCount++;
    emit("SearchEntities", false, "", err);
  }

  try {
    const other = await mem.upsertEntity(`parity-org-ts-${token}`, { entityType: "organization" });
    await mem.upsertEntityEdge(firstId, other.id, "related_to", { confidence: 1.0 });
    emit("UpsertEntityEdge", true, "ok");
    const graph = await mem.entityGraph(firstId, 2);
    emit("EntityGraph", true, `nodes=${graph.nodes?.length ?? 0}`);
    await mem.entityTimeline(firstId);
    emit("EntityTimeline", true, "ok");
  } catch (err) {
    failCount++;
    emit("EntityGraphFamily", false, "", err);
  }

  try {
    await mem.getGrounding(recordId, 2);
    emit("GetGrounding", true, "ok");
  } catch (err) {
    failCount++;
    emit("GetGrounding", false, "", err);
  }

  if (failCount > 0) {
    console.log(`SUMMARY sdk=typescript FAIL count=${failCount}`);
    return 1;
  }
  console.log("SUMMARY sdk=typescript PASS");
  return 0;
}

main().then((code) => process.exit(code));
