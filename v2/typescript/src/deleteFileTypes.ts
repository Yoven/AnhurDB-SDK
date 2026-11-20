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
 * `deleted_ids` e `raft_index` são OPCIONAIS porque o servidor os emite com
 * `omitempty`: em dry-run, ou quando nada casou, as chaves não existem no JSON.
 * Ausência aqui é informação legítima, não erro de parse.
 */
export interface DeleteFileResult {
  session_uuid: string;
  ingest_key_prefix: string;
  matched_count: number;
  deleted_count: number;
  deleted_ids?: number[];
  dry_run: boolean;
  raft_index?: number;
  /**
   * Present only for a tenant-library delete: one outcome per library store
   * (generation `"primary"` = `{c}_{t}_lib`, `"legacy"` = pre-migration rows).
   */
  legs?: DeleteFileLeg[];
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
