/**
 * `listSessions()` must return the WHOLE tenant, not the server's first page.
 *
 * WHY THIS FILE EXISTS, and why every case serves at least TWO pages: the
 * defect it pins shipped precisely because the only coverage was single-page.
 * `GET /api/v1/sessions/stats` defaults to `limit=50` and announces the
 * truncation in `has_more`/`next_offset`; TypeScript sent no params and read
 * only `data.sessions`, so a tenant with 99 sessions answered 50 and looked
 * complete. A one-page test agrees with that bug. Only a test whose server
 * INSISTS there is more can tell a paging client from a truncating one.
 *
 * Each case therefore asserts the UNION across pages, and additionally asserts
 * the exact query string the SDK put on the wire — because "returned 2 rows"
 * is also what a client that guessed `limit=1000` in one shot would report,
 * and the three SDKs are supposed to page identically.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { Memory } from "../memory.js";
import { AnhurError } from "../errors.js";
import { SESSION_STATS_MAX_PAGES, SESSION_STATS_PAGE_LIMIT } from "../sessionStats.js";

/** One request the fake server saw, reduced to what the contract cares about. */
interface RecordedRequest {
  limit: string | null;
  offset: string | null;
}

/**
 * Run `exercise` against a throwaway HTTP server whose body for each call is
 * produced by `replyFor`, and always close the server afterwards.
 */
async function withFakeSessionsServer<T>(
  replyFor: (request: RecordedRequest, callIndex: number) => string,
  exercise: (memory: Memory) => Promise<T>,
): Promise<{ value?: T; thrown?: unknown; requests: RecordedRequest[] }> {
  const requests: RecordedRequest[] = [];
  const server: Server = createServer((incoming, response) => {
    const requestUrl = new URL(incoming.url ?? "/", "http://127.0.0.1");
    const recorded: RecordedRequest = {
      limit: requestUrl.searchParams.get("limit"),
      offset: requestUrl.searchParams.get("offset"),
    };
    requests.push(recorded);
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(replyFor(recorded, requests.length - 1));
  });

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const listeningPort = (server.address() as AddressInfo).port;
  const memory = new Memory({ apiKey: "test-key", url: `http://127.0.0.1:${listeningPort}` });

  try {
    const value = await exercise(memory);
    return { value, requests };
  } catch (thrown: unknown) {
    return { thrown, requests };
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
}

/** An envelope page, shaped exactly like the live server's. */
function envelopePage(
  uuids: string[],
  hasMore: boolean,
  nextOffset: number,
  offset: number,
): string {
  return JSON.stringify({
    count: uuids.length,
    limit: SESSION_STATS_PAGE_LIMIT,
    offset,
    has_more: hasMore,
    next_offset: nextOffset,
    sessions: uuids.map((uuid) => ({ uuid, record_count: 1, last_active: "2026-09-14T00:00:00Z" })),
  });
}

describe("listSessions — auto-pagination", () => {
  it("returns the UNION of two pages, not the first page", async () => {
    const { value, requests } = await withFakeSessionsServer(
      (request) =>
        request.offset === "0"
          ? envelopePage(["page0-a", "page0-b"], true, SESSION_STATS_PAGE_LIMIT, 0)
          : envelopePage(["page1-a"], false, 0, SESSION_STATS_PAGE_LIMIT),
      (memory) => memory.listSessions(),
    );

    assert.deepEqual(
      value?.map((session) => session.uuid),
      ["page0-a", "page0-b", "page1-a"],
      "listSessions must concatenate every page the server offers",
    );
    assert.equal(requests.length, 2, "the second page must actually be fetched");
    assert.deepEqual(
      requests.map((request) => request.offset),
      ["0", String(SESSION_STATS_PAGE_LIMIT)],
      "the SDK must follow next_offset",
    );
  });

  it("asks for limit=500 on every page, matching the Go SDK", async () => {
    const { requests } = await withFakeSessionsServer(
      (request) =>
        request.offset === "0"
          ? envelopePage(["a"], true, SESSION_STATS_PAGE_LIMIT, 0)
          : envelopePage(["b"], false, 0, SESSION_STATS_PAGE_LIMIT),
      (memory) => memory.listSessions(),
    );

    assert.equal(SESSION_STATS_PAGE_LIMIT, 500, "Go's listSessionsPageLimit is 500");
    for (const request of requests) {
      assert.equal(request.limit, "500", "every page must request the Go page size");
    }
  });

  it("stops at has_more=false even when next_offset is non-zero", async () => {
    // The live server answers next_offset=0 on the last page, but Go tolerates
    // a non-zero one. has_more is the authority; next_offset is only a hint.
    const { value, requests } = await withFakeSessionsServer(
      () => envelopePage(["only"], false, 9999, 0),
      (memory) => memory.listSessions(),
    );

    assert.deepEqual(value?.map((session) => session.uuid), ["only"]);
    assert.equal(requests.length, 1, "has_more=false must end the loop immediately");
  });

  it("still advances when the server repeats next_offset (no infinite loop)", async () => {
    // The trap: has_more=true forever with a next_offset that never moves. The
    // monotonic check must fall back to offset + page size, and the page
    // ceiling must end the run with a LOUD error rather than a partial list.
    const { thrown, requests } = await withFakeSessionsServer(
      () => envelopePage(["stuck"], true, 0, 0),
      (memory) => memory.listSessions(),
    );

    assert.ok(thrown instanceof AnhurError, "a non-terminating server must raise, not truncate");
    assert.equal((thrown as AnhurError).kind, "server");
    assert.equal(
      requests.length,
      SESSION_STATS_MAX_PAGES,
      "the ceiling, not the server, must end the loop",
    );
    const offsetsSeen = requests.map((request) => Number(request.offset));
    assert.ok(
      offsetsSeen.every((offset, index) => index === 0 || offset > offsetsSeen[index - 1]),
      "offset must strictly increase even when next_offset is stuck",
    );
  });

  it("accepts the legacy bare array as one complete page", async () => {
    const { value, requests } = await withFakeSessionsServer(
      () => JSON.stringify([{ uuid: "legacy", record_count: 3, last_active: "" }]),
      (memory) => memory.listSessions(),
    );

    assert.deepEqual(value?.map((session) => session.uuid), ["legacy"]);
    assert.equal(requests.length, 1, "a bare array carries no has_more, so it is the whole answer");
  });

  it("returns an empty list for a tenant with zero sessions", async () => {
    const { value } = await withFakeSessionsServer(
      () => envelopePage([], false, 0, 0),
      (memory) => memory.listSessions(),
    );
    assert.deepEqual(value, []);
  });
});
