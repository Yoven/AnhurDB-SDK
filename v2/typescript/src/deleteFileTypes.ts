/**
 * AnhurDB TypeScript SDK — the shapes of `DELETE /api/v1/records/by-file`.
 *
 * One domain: the whole-file delete's options, answer and per-library-store
 * legs. Split out of `types.ts` (past the house 300-line cut) on 2026-11-20
 * when the L2 tenant library (ADR-0029) added `legs` and `scope`.
 */

/**
 * Response of `DELETE /api/v1/records/by-file` — apagar TODO o rastro de um
 * arquivo ingerido (root + capítulos + satélites) de uma sessão.
 *
 * `matched_count` é o que o prefixo ENCONTROU; `deleted_count` é o que o
 * cluster realmente apagou. Em dry-run só `matched_count` é preenchido: nada
 * foi escrito, então `deleted_count` fica 0 de propósito.
 *
 * Junior Tip [por que a contagem faz parte do contrato]: "apaguei 0 registros"
 * tem de ser visível para o chamador. Um método que devolvesse `void`
 * transformaria um prefixo errado em sucesso silencioso — e perda silenciosa é
 * a falha número um deste projeto.
 *
 * `deleted_ids` é OPCIONAL porque o servidor o emite com `omitempty`: em
 * dry-run, ou quando nada casou, a chave não existe no JSON. Ausência aqui é
 * informação legítima, não erro de parse.
 */
export interface DeleteFileResult {
  session_uuid: string;
  ingest_key_prefix: string;
  matched_count: number;
  deleted_count: number;
  deleted_ids?: number[];
  dry_run: boolean;
  /**
   * `true` = gone for good (shared plane); `false` = restorable tombstone (chat
   * plane). Server: `json:"purge"`, always sent (`server/service/record_delete_file.go`).
   * Optional only so a pre-`purge` server still type-checks; treat absent as unknown.
   */
  purge?: boolean;
  /**
   * @deprecated Never sent: the server keeps the consensus log index internal
   * (`json:"-"` at `server/service/record_delete_file.go:42`), so this is always
   * `undefined`. Kept only so existing code that reads it still compiles;
   * removed in the next major. Do not branch on it.
   */
  raft_index?: number;
  /**
   * Present ONLY when `matched_count === 0`: the sessions of this SAME store
   * that do hold records with the requested prefix (server
   * `database.FileSessionCandidate`, `server/database/file_record_sessions.go:38-48`,
   * `omitempty`). Absent = the prefix is not in this store at all (or something
   * matched); present = the session was wrong.
   *
   * Junior Tip [zero matches has two causes — this tells them apart]: the delete
   * stays strict — re-issue `deleteFile` with a candidate's `session_uuid`, and
   * check `root_checksum`/`root_filename` first: a 64-bit prefix is weaker proof
   * than the full sha256.
   */
  alternative_sessions?: FileSessionCandidate[];
  /**
   * Present only for a tenant-library delete: one outcome per library store
   * (generation `"primary"` = `{c}_{t}_lib`, `"legacy"` = pre-migration rows).
   */
  legs?: DeleteFileLeg[];
}

/**
 * One session of the queried store holding records of the file named by the
 * ingest_key prefix (see `DeleteFileResult.alternative_sessions`).
 * `root_checksum` is the FULL sha256 the file_ingestor stores on the root record
 * and `root_filename` its original name; both are absent when the root no longer
 * exists or predates the field (`omitempty` on the server).
 */
export interface FileSessionCandidate {
  session_uuid: string;
  matched_count: number;
  root_checksum?: string;
  root_filename?: string;
}

/**
 * One library store's part of a delete-by-file answer. Server truth:
 * `service.DeleteFileLegOutcome` (`server/service/record_delete_file_library.go:34-44`);
 * `skipped`, `dispatch_skipped` and `error` are `omitempty` there, so optional here.
 *
 * Junior Tip [why the legs matter]: during the L2 migration a library file can
 * live in either store, and the two stores are two Raft groups with no shared
 * transaction. `skipped` = the store does not exist on that node;
 * `dispatch_skipped` = it held no match while the other leg did, so nothing was
 * proposed there. A leg with `error` set failed — see
 * `AnhurLibraryDeletePartialError`.
 */
export interface DeleteFileLeg {
  generation: string;
  session_uuid: string;
  skipped?: boolean;
  dispatch_skipped?: boolean;
  matched_count: number;
  deleted_count: number;
  error?: string;
}

/** Optional arguments of `Memory.deleteFile`. */
export interface DeleteFileOptions {
  /**
   * Count only, write nothing.
   *
   * Junior Tip [dry-run é a rede de segurança, não um detalhe de debug]: a
   * interface mostra "isto vai apagar 511 registros" ANTES de o usuário
   * confirmar. Apagar um documento inteiro é operação de mão pesada; a contagem
   * prévia é o que separa "removi a edição velha da lei" de "removi a
   * biblioteca".
   */
  dryRun?: boolean;
  /**
   * `"tenant_shared"` (the tenant library: the server deletes in BOTH library
   * stores and fills `legs`; `sessionUuid` may then be empty) or
   * `"client_shared"`. Omit for your own store — passing a library session uuid
   * also targets the library. Anything else is refused locally: the server's
   * delete-by-file resolves only those two planes
   * (`server/handler/record_delete_file.go:88-104`).
   */
  scope?: "tenant_shared" | "client_shared";
}
