/**
 * AnhurDB TypeScript SDK — whole-file operations: upload, its status poll, and
 * delete-by-file.
 *
 * One domain in one sentence: send a file to the plane the caller chose,
 * remember that plane, poll IN that plane, and remove a file's whole trace
 * reporting what each library store did. Parity: Go `client/upload.go` +
 * `client/delete_file.go`, Python `anhurdb/client/uploads.py` +
 * `record_delete_file.py`.
 *
 * Moved out of `memory.ts` (far past the house 300-line cut) on 2026-11-20 when
 * the L2 tenant library (ADR-0029) gave the status poll a scope and the delete
 * its per-leg answer. `Memory` extends this class; the public surface is
 * identical.
 */

import { MemoryRecordsApi } from "./recordsApi.js";
import { AnhurQueryError } from "./errors.js";
import { resolveRecordScope, scopeParams, uploadStatusScope } from "./recordScope.js";
import type { ByIdOptions } from "./recordScope.js";
import { pollUploadUntilTerminal } from "./uploadWait.js";
import type { UploadWaitOptions } from "./uploadWait.js";
import type { DeleteFileOptions, DeleteFileResult } from "./deleteFileTypes.js";
import type { UploadResult, UploadStatusResult } from "./uploadTypes.js";

/** Upload and delete-by-file surface of `Memory`. Holds no state. */
export abstract class MemoryFilesApi extends MemoryRecordsApi {
  /**
   * Remove EVERY record produced by one ingested file inside a session — the
   * root record, every chapter and every satellite — via
   * `DELETE /api/v1/records/by-file`.
   *
   * Junior Tip [por que este método existe — o delete que mentia]: `delete(id)`
   * apaga UM record. Um arquivo ingerido vira centenas deles (root + capítulos
   * + satélites), então apagar a ficha do arquivo deixava o CONHECIMENTO vivo:
   * a busca continuava respondendo com o livro "apagado". Sem `deleteFile` não
   * existe "trocar a edição velha pela nova" — as duas coexistiriam para
   * sempre, disputando a mesma busca.
   *
   * A identidade do arquivo é o prefixo `<checksum16>` que o file_ingestor
   * grava em `metadata.ingest_key` (root: `"<c16>:root"`; capítulo:
   * `"<c16>:<total>:<índice>"`) e em `metadata.chapter_ingest_key` (satélite).
   * Um prefixo alcança as três formas.
   *
   * A remoção é soft-delete REPLICADO (tombstone via Raft): o registro fica
   * archived/status=deleted, some da busca e do tier analítico, e continua
   * restaurável — a auditoria não é perdida.
   *
   * Passe `{ dryRun: true }` para contar sem apagar — a rede de segurança que a
   * interface mostra antes de o usuário confirmar.
   *
   * `{ scope: "tenant_shared" }` targets the tenant library: the server deletes
   * in BOTH library stores (`{c}_{t}_lib` and the pre-migration rows) and fills
   * `legs`; `sessionUuid` may then be empty. A failure in one library store
   * throws `AnhurLibraryDeletePartialError` with every leg — retrying the same
   * call is safe (idempotent). `{ scope: "client_shared" }` targets the client
   * library. A collision with a registered tenant throws
   * `AnhurTenantLibraryCollisionError` (not retryable).
   *
   * O tamanho mínimo (8) e o conjunto de caracteres do prefixo são validados no
   * SERVIDOR (fonte única da verdade) e chegam como `AnhurQueryError` HTTP 400.
   *
   * @param sessionUuid     - Session that owns the file's records.
   * @param ingestKeyPrefix - The file's `<checksum16>` identity.
   * @param options         - `{ dryRun }` — count only, write nothing.
   * @throws AnhurQueryError (400, kind `invalid_request`) when either argument
   *         is empty/blank or the scope is not a delete-by-file plane (fails
   *         locally, without a round trip).
   *
   * @example
   * ```ts
   * const preview = await mem.deleteFile(sessionId, "ef9976f1ef5d5176", { dryRun: true });
   * if (preview.matched_count > 0 && confirmed) {
   *   await mem.deleteFile(sessionId, "ef9976f1ef5d5176");
   * }
   * ```
   */
  async deleteFile(
    sessionUuid: string,
    ingestKeyPrefix: string,
    options: DeleteFileOptions = {}): Promise<DeleteFileResult> {
    // Validação local mínima: o vazio nunca merece uma ida ao servidor, e o
    // erro local nomeia o argumento do SDK.
    const deleteScope = (options.scope ?? "").trim();
    if (deleteScope && deleteScope !== "tenant_shared" && deleteScope !== "client_shared") {
      // Junior Tip [one store per scope]: the server's delete-by-file resolves
      // only tenant_shared (two legs, handled together) and client_shared (one
      // store); anything else would delete from a store the caller did not mean.
      throw new AnhurQueryError(
        `INVALID_PARAM: deleteFile: scope '${deleteScope}' is not a delete-by-file plane ` +
          "(want tenant_shared|client_shared, or none)",
        400,
      );
    }
    const trimmedSessionUuid = (sessionUuid ?? "").trim();
    if (!trimmedSessionUuid && deleteScope !== "tenant_shared") {
      throw new AnhurQueryError("INVALID_PARAM: deleteFile: sessionUuid is required", 400);
    }
    const trimmedIngestKeyPrefix = (ingestKeyPrefix ?? "").trim();
    if (!trimmedIngestKeyPrefix) {
      throw new AnhurQueryError("INVALID_PARAM: deleteFile: ingestKeyPrefix is required", 400);
    }

    const deleteParams: Record<string, string> = {
      ingest_key_prefix: trimmedIngestKeyPrefix,
      dry_run: options.dryRun === true ? "true" : "false",
    };
    if (trimmedSessionUuid) deleteParams.session = trimmedSessionUuid;
    if (deleteScope) deleteParams.scope = deleteScope;
    return this.searchTransport.delete<DeleteFileResult>("/api/v1/records/by-file", deleteParams);
  }

  /**
   * Upload a document for async ingestion (multipart/form-data).
   *
   * Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown,
   * HTML, DOCX.
   *
   * The server processes the file asynchronously — use
   * `uploadStatus()` to poll for completion.
   *
   * @param filename  - Original filename (used for format detection).
   * @param content   - Raw file bytes (`Uint8Array` / `Buffer`) or UTF-8 text.
   * @param options   - Chat: `{ sessionId, linkedEpisodicId }` (both required).
   *                    Shared: `{ mode: "tenant_shared" | "client_shared" }`.
   *                    `mode` is trimmed and lower-cased; any other value throws
   *                    `AnhurQueryError` (400, `invalid_request`) before sending.
   *
   * Chat uploads hang the file as a sub-tree of the episodic and set `has_file=true`.
   *
   * The result's `scope` is the plane the file landed in (`tenant_shared` by
   * default, `client_shared`, or `sessions` for chat) — pass it to
   * `uploadStatus` / `waitForUpload` as `{ scope: upload.scope }`.
   */
  async uploadFile(
    filename: string,
    content: Uint8Array | ArrayBuffer | string,
    options?: {
      sessionId?: string;
      linkedEpisodicId?: number;
      mode?: "chat" | "tenant_shared" | "client_shared";
    }): Promise<UploadResult> {
    const form = new FormData();
    let blobParts: BlobPart[];
    if (typeof content === "string") {
      blobParts = [content];
    } else if (content instanceof ArrayBuffer) {
      blobParts = [content];
    } else {
      // Uint8Array — copy into a plain ArrayBuffer slice for BlobPart typing.
      blobParts = [content.buffer.slice(content.byteOffset, content.byteOffset + content.byteLength) as ArrayBuffer];
    }
    form.append("file", new Blob(blobParts), filename);
    // Junior Tip [why the mode is normalised at runtime, 2026-11-21]: the
    // union type only protects TypeScript callers; from plain JS "Chat" or
    // " client_shared " used to fall through both branches, send NO mode, and
    // the server filed the upload in tenant_shared while `scope` below
    // reported another plane. Go and Python trim + lower-case and refuse an
    // unknown value; this does the same, with the same typed error.
    let mode = String(options?.mode ?? "").trim().toLowerCase();
    if (options?.sessionId && !mode) {
      mode = "chat";
    }
    if (mode && mode !== "chat" && mode !== "tenant_shared" && mode !== "client_shared") {
      throw new AnhurQueryError(
        `INVALID_PARAM: upload: mode '${String(options?.mode)}' is not an upload mode ` +
          "(want chat|tenant_shared|client_shared)",
        400,
      );
    }
    if (mode === "chat") {
      if (!options?.sessionId) {
        throw new Error(
          "session_id is required — create a session first (POST /api/v1/sessions)",
        );
      }
      if (options.linkedEpisodicId === undefined || options.linkedEpisodicId <= 0) {
        throw new Error(
          "linked_episodic_id is required for chat uploads — attach the file to the episodic turn",
        );
      }
      form.append("mode", "chat");
      form.append("session_id", options.sessionId);
      form.append("linked_episodic_id", String(options.linkedEpisodicId));
    } else if (mode === "tenant_shared" || mode === "client_shared") {
      form.append("mode", mode);
    }
    const uploadResult = await this.searchTransport.postMultipart<UploadResult>("/api/v1/upload", form);
    return { ...uploadResult, scope: uploadStatusScope(mode, options?.sessionId) };
  }

  /**
   * Check the processing status of a file upload.
   *
   * Junior Tip [why the poll names the plane — server fix/l2-lib-scope]: a
   * record id is a rowid PER STORE, and uploads now land in the library store
   * `{c}_{t}_lib` or in `{c}_shared`. Without a scope the server searches every
   * plane this caller can upload to and throws `AnhurUploadStatusAmbiguousError`
   * (HTTP 409) when the id is a file in more than one
   * (`handler/upload_status_scope.go:143-198`).
   *
   * @param uploadId - The `record_id` returned by `uploadFile()`.
   * @param options  - `{ scope: upload.scope }` — the plane the file landed in.
   */
  async uploadStatus(
    uploadId: number,
    options?: ByIdOptions): Promise<UploadStatusResult> {
    const scope = resolveRecordScope("uploadStatus", options?.scope);
    return this.searchTransport.get<UploadStatusResult>(
      `/api/v1/upload/${uploadId}/status`,
      scopeParams(scope));
  }

  /**
   * Poll `uploadStatus` until the upload reaches a terminal state.
   *
   * Parity: Go `WaitForUpload` / Python `wait_for_upload` — same state
   * machine, same defaults (PARITY_SPEC.md).
   *
   * Junior Tip [por que 404 vira "pendente" no começo — medido 2026-08-07]:
   * as leituras do AnhurDB são load-balanced; logo após o POST de upload um
   * follower que ainda não aplicou a entrada devolve 404 legítimo por alguns
   * segundos (read-your-writes). Dentro de `notFoundGraceMs` o 404 é espera;
   * DEPOIS dela ele re-lança — um id inválido não pode virar espera infinita
   * (falhar alto, nunca engolir).
   *
   * Returns the final payload — INCLUDING `status="failed"` (a failed ingest
   * is terminal data the caller must inspect, not a transport error). Throws
   * `AnhurQueryError` when the 404 persists beyond the grace window, and
   * `AnhurUploadWaitTimeout` when no terminal status arrives in time. Pass
   * `{ scope: upload.scope }` so every poll reads the plane the file landed in.
   */
  async waitForUpload(
    uploadId: number,
    options?: UploadWaitOptions,
  ): Promise<UploadStatusResult> {
    // The state machine lives in uploadWait.ts (house cut). It is handed a
    // CLOSURE over `this.uploadStatus` rather than the client, so the polling
    // logic depends on one behaviour only — that a missing upload arrives as an
    // AnhurError with statusCode 404 — and on nothing else about transport.
    return pollUploadUntilTerminal(
      () => this.uploadStatus(uploadId, { scope: options?.scope }),
      uploadId,
      options,
    );
  }
}
