/**
 * deleteFile — apagar TODO o rastro de um arquivo ingerido (paridade 3 SDKs).
 *
 * O contrato deste endpoint é a URL: `DELETE /api/v1/records/by-file` com
 * `session`, `ingest_key_prefix` e `dry_run` na query. Por isso os testes
 * interceptam `fetch` e asseguram o que foi para o fio — não a intenção do
 * código. A contagem devolvida também é contrato: "apaguei 0" precisa ser
 * visível, nunca um sucesso mudo.
 *
 * Mesmo desenho dos testes Go (httptest) e Python (aiohttp).
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurQueryError, type DeleteFileResult } from "../types.js";

/** O que o SDK realmente pôs no fio. */
interface CapturedRequest {
  method: string;
  url: URL;
}

/**
 * Executa `run` com `fetch` interceptado, devolvendo o pedido capturado e o
 * resultado do método. `responseBody` é o envelope que o servidor devolveria.
 */
async function captureDeleteFile(
  responseBody: unknown,
  status: number,
  run: (mem: Memory) => Promise<DeleteFileResult>,
): Promise<{ captured: CapturedRequest | null; result?: DeleteFileResult; error?: unknown }> {
  const originalFetch = globalThis.fetch;
  let captured: CapturedRequest | null = null;
  globalThis.fetch = (async (requestUrl: unknown, init: RequestInit) => {
    captured = { method: String(init.method), url: new URL(String(requestUrl)) };
    return new Response(JSON.stringify(responseBody), {
      status,
      headers: { "content-type": "application/json" },
    });
  }) as unknown as typeof fetch;

  try {
    const mem = new Memory({ apiKey: "key", userId: "u", url: "http://localhost:8000" });
    const result = await run(mem);
    return { captured, result };
  } catch (error: unknown) {
    return { captured, error };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

describe("Memory.deleteFile (wire contract)", () => {
  it("sends DELETE /api/v1/records/by-file with session, prefix and dry_run=false", async () => {
    const { captured, result } = await captureDeleteFile(
      {
        session_uuid: "chat-42",
        ingest_key_prefix: "ef9976f1ef5d5176",
        matched_count: 511,
        deleted_count: 511,
        deleted_ids: [1, 2, 3],
        dry_run: false,
        raft_index: 123,
      },
      200,
      (mem) => mem.deleteFile("chat-42", "ef9976f1ef5d5176"),
    );

    assert.ok(captured);
    assert.equal(captured!.method, "DELETE");
    assert.equal(captured!.url.pathname, "/api/v1/records/by-file");
    assert.equal(captured!.url.searchParams.get("session"), "chat-42");
    assert.equal(
      captured!.url.searchParams.get("ingest_key_prefix"),
      "ef9976f1ef5d5176",
    );
    assert.equal(captured!.url.searchParams.get("dry_run"), "false");

    // A contagem é a resposta ao usuário — perdê-la devolveria "sucesso" sem
    // dizer o que aconteceu.
    assert.equal(result?.matched_count, 511);
    assert.equal(result?.deleted_count, 511);
    assert.deepEqual(result?.deleted_ids, [1, 2, 3]);
    assert.equal(result?.raft_index, 123);
    assert.equal(result?.dry_run, false);
  });

  it("sends dry_run=true and decodes the envelope without deleted_ids", async () => {
    const { captured, result } = await captureDeleteFile(
      {
        session_uuid: "chat-42",
        ingest_key_prefix: "ef9976f1ef5d5176",
        matched_count: 511,
        deleted_count: 0,
        dry_run: true,
      },
      200,
      (mem) => mem.deleteFile("chat-42", "ef9976f1ef5d5176", { dryRun: true }),
    );

    assert.equal(captured!.url.searchParams.get("dry_run"), "true");
    assert.equal(result?.dry_run, true);
    assert.equal(result?.matched_count, 511);
    assert.equal(result?.deleted_count, 0);
    assert.equal(result?.deleted_ids, undefined);
    assert.equal(result?.raft_index, undefined);
  });

  it("trims both arguments so whitespace never becomes part of the identity", async () => {
    const { captured } = await captureDeleteFile(
      { session_uuid: "chat-42", ingest_key_prefix: "ef9976f1ef5d5176", matched_count: 0, deleted_count: 0, dry_run: false },
      200,
      (mem) => mem.deleteFile("  chat-42 ", " ef9976f1ef5d5176\n"),
    );

    assert.equal(captured!.url.searchParams.get("session"), "chat-42");
    assert.equal(
      captured!.url.searchParams.get("ingest_key_prefix"),
      "ef9976f1ef5d5176",
    );
  });

  it("surfaces the server's HTTP 400 for a short prefix (rule lives server-side)", async () => {
    const { error } = await captureDeleteFile(
      { error: 'ingest key prefix "abc" is too short (minimum 8 characters)' },
      400,
      (mem) => mem.deleteFile("chat-42", "abc"),
    );

    assert.ok(error instanceof AnhurQueryError);
  });
});

describe("Memory.deleteFile (local validation)", () => {
  // O `fetch` é substituído por uma armadilha: se a validação local falhar em
  // barrar o argumento vazio, o teste quebra em vez de bater no servidor.
  const rejectAnyNetworkCall = async (
    run: (mem: Memory) => Promise<DeleteFileResult>,
  ): Promise<unknown> => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () => {
      throw new Error("local validation must not reach the server");
    }) as unknown as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      return await run(mem).then(() => undefined).catch((error: unknown) => error);
    } finally {
      globalThis.fetch = originalFetch;
    }
  };

  it("rejects an empty sessionUuid", async () => {
    const error = await rejectAnyNetworkCall((mem) =>
      mem.deleteFile("", "ef9976f1ef5d5176"));
    assert.ok(error instanceof Error);
    assert.equal((error as Error).message, "deleteFile: sessionUuid is required");
  });

  it("rejects a blank sessionUuid", async () => {
    const error = await rejectAnyNetworkCall((mem) =>
      mem.deleteFile("   ", "ef9976f1ef5d5176"));
    assert.equal((error as Error).message, "deleteFile: sessionUuid is required");
  });

  it("rejects an empty ingestKeyPrefix", async () => {
    const error = await rejectAnyNetworkCall((mem) =>
      mem.deleteFile("chat-42", ""));
    assert.equal(
      (error as Error).message,
      "deleteFile: ingestKeyPrefix is required",
    );
  });

  it("rejects a blank ingestKeyPrefix", async () => {
    const error = await rejectAnyNetworkCall((mem) =>
      mem.deleteFile("chat-42", "\t"));
    assert.equal(
      (error as Error).message,
      "deleteFile: ingestKeyPrefix is required",
    );
  });
});
