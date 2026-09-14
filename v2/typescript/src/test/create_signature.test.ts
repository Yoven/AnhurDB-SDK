/**
 * `create()` — one convention: required session, required content, typed
 * optionals (SPEC §7). Plus the entity-graph depth invariant (§2), which is a
 * no-op in TypeScript and is asserted here so it STAYS one.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { recordWire, expectNoRequest } from "./wireRecorder.js";

describe("create(sessionUuid, text, options) — the session is positional", () => {
  it("files the record under the POSITIONAL session, not the ambient one", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { requests, value } = await recordWire({ id: 77 },
      () => memory.create("explicit-session", "a fact", { type: "fact" }));
    assert.equal(requests.length, 1);
    assert.equal(requests[0].url.pathname, "/api/v1/records");
    const body = requests[0].body as Record<string, unknown>;
    assert.equal(body.uuid, "explicit-session");
    assert.equal(body.type, "fact");
    assert.equal(value.sessionId, "explicit-session");
  });

  it("refuses a blank session BEFORE any HTTP, with no ambient fallback", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    // The load-bearing case: this client HAS a session of its own. Under the
    // old signature `create(text, opts)` with no session option, the record
    // went there silently — a misfile, not an error. Now the caller must say.
    await recordWire({ uuid: "ambient-session" },
      () => memory.createSession({ sessionId: "ambient-session" }));
    for (const blankSession of ["", "   "]) {
      const { requestCount, thrown } = await expectNoRequest(
        () => memory.create(blankSession, "a fact", { type: "fact" }));
      assert.ok(thrown instanceof Error, "a blank session must throw");
      assert.match(String((thrown as Error).message), /sessionUuid is required/);
      assert.equal(requestCount, 0,
        "the guard must fire locally — a server round-trip would mean the " +
          "SDK still had to guess a session to send");
    }
  });

  it("puts every optional field on the wire, `status` included", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { requests } = await recordWire({ id: 78 },
      () => memory.create("sess-1", "a decision", {
        type: "decision",
        score: 9,
        // OLD TYPE: `status` was the one field of the shared optional set that
        // TypeScript could not express, so createRecord hardcoded "saved" and
        // a caller wanting anything else had to PATCH afterwards.
        status: "consolidated",
        relatedIds: [1, 2],
        validFrom: "2026-01-01T00:00:00Z",
        validUntil: "2026-12-31T00:00:00Z",
        metadata: { origin: "test" },
      }));
    const body = requests[0].body as Record<string, unknown>;
    assert.equal(body.uuid, "sess-1");
    assert.equal(body.type, "decision");
    assert.equal(body.score, 9);
    assert.equal(body.status, "consolidated");
    assert.deepEqual(body.related_ids, [1, 2]);
    // CORRECAO 2026-09-14: estas duas asserções pinavam o DEFEITO. A janela
    // temporal ia como campo de TOPO, e nesta rota o servidor so a le de dentro
    // do metadata (service/record_create.go:334) — respondia 201 e a janela
    // nunca existia. Ver create_temporal_metadata.test.ts para o contrato.
    assert.equal(body.valid_from, undefined);
    assert.equal(body.valid_until, undefined);
    const temporalMetadata = JSON.parse(String(body.metadata ?? "{}"));
    assert.equal(temporalMetadata.valid_from, "2026-01-01T00:00:00Z");
    assert.equal(temporalMetadata.valid_until, "2026-12-31T00:00:00Z");
  });

  it("still defaults status to `saved` when the caller stays quiet", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { requests } = await recordWire({ id: 79 },
      () => memory.create("sess-1", "an episode"));
    const body = requests[0].body as Record<string, unknown>;
    assert.equal(body.status, "saved");
    assert.equal(body.type, "episodic");
    assert.equal(body.score, 5);
  });
});

describe("entityGraph — the SERVER owns the default depth (it is 1)", () => {
  it("sends NO depth param when the caller did not ask", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { requests } = await recordWire(
      { entity_id: 1, depth: 1, node_count: 1, nodes: [] },
      () => memory.entityGraph(1));
    // Live 2026-09-14: omitted -> depth:1; ?depth=2 -> depth:2. The value
    // changes the graph, so an SDK that restates today's default keeps
    // returning the old graph on the day the server changes it. Python
    // defaulted to 2 and silently answered a graph the server would not have.
    assert.equal(requests[0].url.searchParams.has("depth"), false);
    assert.equal(requests[0].url.pathname, "/api/v1/entities/1/graph");
  });

  it("sends exactly what the caller asked for when they do ask", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { requests } = await recordWire(
      { entity_id: 1, depth: 3, node_count: 4, nodes: [] },
      () => memory.entityGraph(1, 3));
    assert.equal(requests[0].url.searchParams.get("depth"), "3");
  });
});
