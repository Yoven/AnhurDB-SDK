/**
 * waitForUpload — polling tolerante a 404 transiente (paridade 3 SDKs).
 *
 * Junior Tip [por que 404 vira "pendente" no começo — medido 2026-08-07]: as
 * leituras do AnhurDB são load-balanced; logo após o POST de upload um
 * follower que ainda não aplicou a entrada devolve 404 legítimo por alguns
 * segundos (read-your-writes). Dentro de notFoundGraceMs o 404 é espera;
 * depois dela ele re-lança — id inválido não pode virar espera infinita.
 *
 * Os testes stubam `uploadStatus` (a unidade é a máquina de estados do wait,
 * não o transporte) — mesmo desenho do teste Python.
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
      { record_id: 42, status: "completed", completed: true } as UploadStatusResult,
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
    const mem = memoryWithStub([
      { record_id: 7, status: "failed", error: "extract crashed" } as UploadStatusResult,
    ]);
    const result = await mem.waitForUpload(7, { timeoutMs: 2000, intervalMs: 10 });
    assert.equal(result.status, "failed");
  });

  it("timeout throws typed error carrying the last status", async () => {
    const mem = memoryWithStub([
      { record_id: 8, status: "processing" } as UploadStatusResult,
    ]);
    await assert.rejects(
      mem.waitForUpload(8, { timeoutMs: 100, intervalMs: 10 }),
      (thrown: unknown) =>
        thrown instanceof AnhurUploadWaitTimeout &&
        String((thrown as Error).message).includes("processing"),
    );
  });
});
