/**
 * waitForUpload — polling tolerante a 404 transiente (paridade 3 SDKs).
 *
 * Junior Tip [por que 404 vira "pendente" no começo — medido 2026-08-07]: as
 * leituras do AnhurDB são load-balanced; logo após o POST de upload um
 * follower que ainda não aplicou a entrada devolve 404 legítimo por alguns
 * segundos (read-your-writes). Dentro de notFoundGraceMs o 404 é espera;
 * depois dela ele re-lança — id inválido não pode virar espera infinita.
 *
 * Os testes abaixo stubam `uploadStatus` para exercitar a MÁQUINA DE ESTADOS
 * (relógios, término, timeout) — mesmo desenho do teste Python.
 *
 * ATENÇÃO — o que estes stubs NÃO provam: eles constroem o erro 404 à mão, com
 * o status já preenchido. Enquanto o `request()` real construía o 404 SEM o
 * status, esta suíte continuou verde e a janela de graça estava morta em
 * produção. A prova de que o transporte entrega o status vive em
 * `http_error_status.test.ts` (fetch stubado, caminho real), e o último teste
 * deste arquivo amarra as duas pontas atravessando o transporte de verdade.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import {
  AnhurQueryError,
  AnhurUploadWaitTimeout,
  type UploadStatusResult,
} from "../types.js";

type StubStep = UploadStatusResult | Error;

function memoryWithStub(steps: StubStep[]): Memory {
  const mem = new Memory({ apiKey: "test-key-000" });
  let index = 0;
  (mem as any).uploadStatus = async (): Promise<UploadStatusResult> => {
    const step = steps[Math.min(index, steps.length - 1)];
    index += 1;
    if (step instanceof Error) throw step;
    return step;
  };
  return mem;
}

const notFound = () =>
  new AnhurQueryError("Resource not found (HTTP 404): /api/v1/upload/9/status", 404);

describe("waitForUpload", () => {
  it("tolerates early 404 then completes", async () => {
    const mem = memoryWithStub([
      notFound(),
      notFound(),
      {
        record_id: 42,
        uuid: "u-42",
        status: "completed",
        type: "file",
        completed: true,
      } as UploadStatusResult,
    ]);
    const result = await mem.waitForUpload(42, {
      timeoutMs: 5000,
      intervalMs: 10,
      notFoundGraceMs: 2000,
    });
    assert.equal(result.completed, true);
  });

  it("404 beyond grace rethrows the real error", async () => {
    const mem = memoryWithStub([notFound()]);
    await assert.rejects(
      mem.waitForUpload(999999, {
        timeoutMs: 5000,
        intervalMs: 10,
        notFoundGraceMs: 50,
      }),
      (thrown: unknown) =>
        thrown instanceof AnhurQueryError && thrown.statusCode === 404,
    );
  });

  it("failed status is terminal data, not an error", async () => {
    // Shape copied from AnhurDB/server/handler/upload.go: the handler emits
    // record_id/uuid/status/type/summary/metadata/completed and NOTHING else.
    // A failure is announced through `status`, never through an `error` field —
    // the fixture used to invent one, which is how the unreachable
    // `Boolean(payload.error)` terminal branch looked covered.
    const mem = memoryWithStub([
      {
        record_id: 7,
        uuid: "u-7",
        status: "failed",
        type: "file",
        completed: false,
      } as UploadStatusResult,
    ]);
    const result = await mem.waitForUpload(7, { timeoutMs: 2000, intervalMs: 10 });
    assert.equal(result.status, "failed");
  });

  it("a non-terminal status stays non-terminal even with a stray error key", async () => {
    // Bites the branch that was removed: `Boolean(payload.error)` used to be a
    // TERMINAL condition, so any payload carrying an `error`-shaped key would
    // end the wait and be handed back as the final answer while the upload was
    // still processing. The server never sends that key (upload.go emits seven
    // fixed keys), so the branch was unreachable — but if it ever comes back,
    // this is the wait ending on a field that means nothing.
    const mem = memoryWithStub([
      {
        record_id: 9,
        uuid: "u-9",
        status: "processing",
        type: "file",
        completed: false,
        error: "a warning the wait must ignore",
      } as unknown as UploadStatusResult,
    ]);
    await assert.rejects(
      mem.waitForUpload(9, { timeoutMs: 100, intervalMs: 10 }),
      (thrown: unknown) => thrown instanceof AnhurUploadWaitTimeout,
    );
  });

  it("timeout throws typed error carrying the last status", async () => {
    const mem = memoryWithStub([
      {
        record_id: 8,
        uuid: "u-8",
        status: "processing",
        type: "file",
        completed: false,
      } as UploadStatusResult,
    ]);
    await assert.rejects(
      mem.waitForUpload(8, { timeoutMs: 100, intervalMs: 10 }),
      (thrown: unknown) =>
        thrown instanceof AnhurUploadWaitTimeout &&
        String((thrown as Error).message).includes("processing"),
    );
  });
});

/**
 * The link the hand-built fixtures above cannot test: the grace window only
 * works if the TRANSPORT hands `waitForUpload` a 404 that still knows it is a
 * 404. This drives real `fetch` → real `request()` → real `uploadStatus()`.
 */
describe("waitForUpload over the real transport", () => {
  it("rides out a follower 404 and returns the completed payload", async () => {
    const originalFetch = globalThis.fetch;
    let callCount = 0;
    globalThis.fetch = (async () => {
      callCount += 1;
      // Two load-balanced reads land on a follower that has not applied the
      // upload yet — a legitimate 404 (read-your-writes), then it appears.
      if (callCount <= 2) {
        return new Response(JSON.stringify({ error: "record not found" }), {
          status: 404,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(
        JSON.stringify({
          record_id: 42,
          uuid: "u-42",
          status: "completed",
          type: "file",
          summary: "ok",
          metadata: "{}",
          completed: true,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }) as typeof fetch;

    try {
      const mem = new Memory({
        apiKey: "test-key-000",
        userId: "u",
        url: "http://localhost:8000",
      });
      const result = await mem.waitForUpload(42, {
        timeoutMs: 5000,
        intervalMs: 10,
        notFoundGraceMs: 2000,
      });
      assert.equal(result.completed, true);
      assert.equal(result.status, "completed");
      assert.equal(callCount, 3);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("rethrows the transport's own 404 once the grace window closes", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ error: "record not found" }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      })) as typeof fetch;

    try {
      const mem = new Memory({
        apiKey: "test-key-000",
        userId: "u",
        url: "http://localhost:8000",
      });
      await assert.rejects(
        mem.waitForUpload(999999, {
          timeoutMs: 5000,
          intervalMs: 10,
          notFoundGraceMs: 0,
        }),
        (thrown: unknown) =>
          thrown instanceof AnhurQueryError && thrown.statusCode === 404,
      );
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});
