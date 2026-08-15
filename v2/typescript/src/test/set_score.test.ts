/**
 * O score não pode ser alterado por update() — falha medida em 2026-08-15.
 *
 * PATCH /api/v1/records/{id} não tem campo score: o servidor responde 200 e
 * descarta a chave. Um cliente que chamasse update(id, { score: 8 }) recebia
 * sucesso e nada era gravado — a mesma forma do defeito do campo `archived`.
 *
 * A correção não é dividir a chamada em duas por baixo dos panos: isso criaria
 * sucesso PARCIAL (summary grava, score falha), que é a mesma perda silenciosa
 * por outra porta. update() lança e nomeia setScore().
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import * as http from "node:http";
import { Memory } from "../memory.js";

async function withServer(
  handler: (request: http.IncomingMessage, response: http.ServerResponse, body: string) => void,
  run: (baseUrl: string) => Promise<void>,
): Promise<void> {
  const server = http.createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => { body += chunk; });
    request.on("end", () => handler(request, response, body));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address() as { port: number };
  try {
    await run(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
}

describe("score is not writable through update()", () => {
  it("update() throws instead of silently dropping score", async () => {
    let serverWasCalled = false;
    await withServer(
      (_request, response) => { serverWasCalled = true; response.end("{}"); },
      async (baseUrl) => {
        const memory = new Memory({ apiKey: "test-key", url: baseUrl });
        await assert.rejects(
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          () => memory.update(42, { score: 8 } as any),
          /setScore/,
          "a mensagem tem que nomear o método que funciona",
        );
        assert.equal(serverWasCalled, false,
          "a guarda deixou a requisição sair — tem que barrar antes do transporte");
      },
    );
  });

  it("update() throws even when score rides along other fields", async () => {
    await withServer(
      (_request, response) => response.end("{}"),
      async (baseUrl) => {
        const memory = new Memory({ apiKey: "test-key", url: baseUrl });
        await assert.rejects(
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          () => memory.update(42, { summary: "novo", score: 8 } as any),
          /setScore/,
          "sucesso parcial (summary grava, score não) é perda silenciosa",
        );
      },
    );
  });

  it("update() still accepts ordinary fields", async () => {
    let capturedPath = "";
    let capturedMethod = "";
    await withServer(
      (request, response) => {
        capturedPath = request.url ?? "";
        capturedMethod = request.method ?? "";
        response.end("{}");
      },
      async (baseUrl) => {
        const memory = new Memory({ apiKey: "test-key", url: baseUrl });
        await memory.update(42, { summary: "novo" });
        assert.equal(capturedMethod, "PATCH");
        assert.equal(capturedPath, "/api/v1/records/42");
      },
    );
  });

  it("setScore() posts to the durable replicated route with ids and score", async () => {
    let capturedPath = "";
    let capturedMethod = "";
    let capturedBody = "";
    await withServer(
      (request, response, body) => {
        capturedPath = request.url ?? "";
        capturedMethod = request.method ?? "";
        capturedBody = body;
        response.setHeader("Content-Type", "application/json");
        response.end(`{"status":"ok"}`);
      },
      async (baseUrl) => {
        const memory = new Memory({ apiKey: "test-key", url: baseUrl });
        await memory.setScore(42, 8);
        assert.equal(capturedMethod, "POST");
        assert.equal(capturedPath, "/api/v1/records/set-score");
        const parsed = JSON.parse(capturedBody);
        assert.deepEqual(parsed.ids, [42]);
        assert.equal(parsed.score, 8);
      },
    );
  });

  it("setScore() validates the schema range, boundaries included", async () => {
    await withServer(
      (_request, response) => {
        response.setHeader("Content-Type", "application/json");
        response.end(`{"status":"ok"}`);
      },
      async (baseUrl) => {
        const memory = new Memory({ apiKey: "test-key", url: baseUrl });
        for (const invalid of [0, -1, 11, 99, 2.5]) {
          await assert.rejects(() => memory.setScore(42, invalid), /between 1 and 10/);
        }
        // 1 e 10 são válidos: a guarda não pode encolher a faixa do schema.
        await memory.setScore(42, 1);
        await memory.setScore(42, 10);
      },
    );
  });
});
