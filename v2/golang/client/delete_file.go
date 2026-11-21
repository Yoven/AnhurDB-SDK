package client

// delete_file.go — apagar TODO o rastro de um arquivo ingerido.
//
// Domínio de um arquivo só: o tipo de retorno, as opções e o método vivem
// juntos aqui porque mudam juntos. client.go e types.go já passam do corte de
// 300 linhas da casa; nada novo entra neles.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DeleteFileResult is the wire response of DELETE /api/v1/records/by-file.
//
// MatchedCount is what the prefix FOUND; DeletedCount is what the cluster
// actually apagou. Em dry-run só MatchedCount é preenchido — DeletedCount fica
// 0 de propósito, porque nada foi escrito.
//
// Junior Tip [por que a contagem faz parte do contrato]: "apaguei 0 registros"
// tem de ser visível para o chamador. Um método que devolvesse apenas `error`
// transformaria um prefixo errado em sucesso silencioso — e perda silenciosa é
// a falha número um deste projeto.
//
// DeletedIDs chega com `omitempty` do servidor: em dry-run, ou quando nada
// casou, a chave simplesmente não existe no JSON. Ausência aqui é informação
// legítima, não erro de decodificação.
type DeleteFileResult struct {
	SessionUUID     string  `json:"session_uuid"`
	IngestKeyPrefix string  `json:"ingest_key_prefix"`
	MatchedCount    int     `json:"matched_count"`
	DeletedCount    int     `json:"deleted_count"`
	DeletedIDs      []int64 `json:"deleted_ids,omitempty"`
	DryRun          bool    `json:"dry_run"`
	// Purge reports HOW the records went: true = gone for good (shared plane),
	// false = restorable tombstone (chat plane). Server truth: `json:"purge"`,
	// always sent (server/service/record_delete_file.go).
	//
	// Junior Tip [why the caller needs it]: "deleted 511" means two very
	// different things — undoable or not. A UI that cannot tell them apart either
	// promises an undo that does not exist or hides one that does.
	Purge bool `json:"purge"`
	// There is deliberately no RaftIndex field (removed in 4.0.0): the server
	// keeps the consensus log index internal (json:"-" in
	// service.DeleteFileResult, server/service/record_delete_file.go:42), so a
	// field here could only ever read 0.
	// AlternativeSessions is filled ONLY when MatchedCount == 0: the sessions of
	// this SAME store that do hold records with the requested ingest_key prefix.
	// Server truth: database.FileSessionCandidate
	// (server/database/file_record_sessions.go:38-48), `omitempty`.
	//
	// Junior Tip [zero matches has two causes — this field tells them apart]: an
	// empty list means the prefix is not in this store at all (wrong checksum); a
	// non-empty list means the SESSION was wrong and names the right one. The
	// delete stays strict — re-issue DeleteFile with a candidate's SessionUUID;
	// never pick one automatically without checking RootChecksum/RootFilename,
	// because a 64-bit prefix is weaker proof than the full sha256.
	AlternativeSessions []FileSessionCandidate `json:"alternative_sessions,omitempty"`
	// Legs is present only for a tenant-library delete: one outcome per library
	// store (generation "primary" = {c}_{t}_lib, "legacy" = pre-migration rows in
	// {c}_{t}). Absent for every other plane.
	Legs []DeleteFileLeg `json:"legs,omitempty"`
}

// FileSessionCandidate is one session of the queried store that holds records of
// the file named by the ingest_key prefix (see DeleteFileResult.AlternativeSessions).
// RootChecksum is the FULL sha256 the file_ingestor stores on the root record and
// RootFilename its original name; both are empty when the root no longer exists
// or predates the field.
type FileSessionCandidate struct {
	SessionUUID  string `json:"session_uuid"`
	MatchedCount int    `json:"matched_count"`
	RootChecksum string `json:"root_checksum,omitempty"`
	RootFilename string `json:"root_filename,omitempty"`
}

// DeleteFileLeg is one library store's part of a delete-by-file answer.
// Server truth: service.DeleteFileLegOutcome (server/service/record_delete_file_library.go:34-44).
//
// Junior Tip [why the legs matter]: during the L2 migration a library file can live
// in either store, and the two stores are two Raft groups with no shared
// transaction. Skipped = the store does not exist on that node; DispatchSkipped =
// the store held no match while the other leg did, so nothing was proposed there.
// A leg with Error set failed — see LibraryDeletePartialError.
type DeleteFileLeg struct {
	Generation      string `json:"generation"`
	SessionUUID     string `json:"session_uuid"`
	Skipped         bool   `json:"skipped,omitempty"`
	DispatchSkipped bool   `json:"dispatch_skipped,omitempty"`
	MatchedCount    int    `json:"matched_count"`
	DeletedCount    int    `json:"deleted_count"`
	Error           string `json:"error,omitempty"`
}

// DeleteFileOption configures a DeleteFile call.
type DeleteFileOption func(*deleteFileConfig)

// deleteFileConfig holds the optional parameters of DeleteFile.
type deleteFileConfig struct {
	dryRun bool
	scope  RecordScope
}

// WithDeleteFileDryRun turns the call into a COUNT-ONLY probe: the server
// resolves the same match set and returns MatchedCount without writing
// anything.
//
// Junior Tip [dry-run é a rede de segurança, não um detalhe de debug]: a
// interface mostra "isto vai apagar 511 registros" ANTES de o usuário
// confirmar. Apagar um documento inteiro é uma operação de mão pesada; a
// contagem prévia é o que separa "removi a edição velha da lei" de "removi a
// biblioteca".
func WithDeleteFileDryRun(dryRun bool) DeleteFileOption {
	return func(cfg *deleteFileConfig) {
		cfg.dryRun = dryRun
	}
}

// WithDeleteFileScope names the plane the file lives in: RecordScopeTenantShared
// (the tenant library — the server deletes in BOTH library stores and reports
// Legs; sessionUUID may then be empty) or RecordScopeClientShared. Without it the
// caller's own store is used, except that passing a library session uuid also
// targets the library.
//
// Junior Tip [one store per scope]: sessions, tenant_shared_legacy and shared_all are
// refused locally — the server's delete-by-file resolves only tenant_shared (two
// legs, handled together) and client_shared (one store); anything else would delete
// from a store the caller did not mean (server/handler/record_delete_file.go:88-104).
func WithDeleteFileScope(scope RecordScope) DeleteFileOption {
	return func(cfg *deleteFileConfig) {
		cfg.scope = scope
	}
}

// DeleteFile removes EVERY record produced by one ingested file inside a
// session — the root record, every chapter and every satellite — via
// DELETE /api/v1/records/by-file.
//
// Junior Tip [por que este método existe — o delete que mentia]: Delete(id)
// apaga UM record. Um arquivo ingerido vira centenas deles (root + capítulos +
// satélites), então apagar a ficha do arquivo deixava o CONHECIMENTO vivo: a
// busca continuava respondendo com o livro "apagado". Sem DeleteFile não existe
// "trocar a edição velha pela nova" — as duas coexistiriam para sempre,
// disputando a mesma busca.
//
// A identidade do arquivo é o prefixo <checksum16> que o file_ingestor grava em
// metadata.ingest_key (root: "<c16>:root"; capítulo: "<c16>:<total>:<índice>")
// e em metadata.chapter_ingest_key (satélite). Um prefixo alcança as três
// formas.
//
// A remoção é soft-delete REPLICADO (tombstone via Raft): o registro fica
// archived/status=deleted, some da busca e do tier analítico, e continua
// restaurável — a auditoria não é perdida.
//
// Passe WithDeleteFileDryRun(true) para contar sem apagar.
//
//	preview, _ := mem.DeleteFile(ctx, sessionUUID, "ef9976f1ef5d5176",
//	    client.WithDeleteFileDryRun(true))
//	// preview.MatchedCount == 511 → confirme com o usuário, então repita sem a opção.
func (m *Memory) DeleteFile(
	ctx context.Context,
	sessionUUID string,
	ingestKeyPrefix string,
	opts ...DeleteFileOption,
) (*DeleteFileResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	// Validação local mínima: o vazio nunca merece uma ida ao servidor, e o erro
	// local nomeia o argumento do SDK. As demais regras do prefixo (tamanho
	// mínimo e caracteres válidos) são do servidor — uma fonte da verdade só,
	// para que os três SDKs não fiquem a divergir dela.
	cfg := &deleteFileConfig{}
	for _, applyOption := range opts {
		applyOption(cfg)
	}
	deleteScope := RecordScope(strings.TrimSpace(string(cfg.scope)))
	switch deleteScope {
	case "", RecordScopeTenantShared, RecordScopeClientShared:
	default:
		return nil, newValidationError(
			"INVALID_PARAM: DeleteFile: scope %q is not a delete-by-file plane (want tenant_shared|client_shared, or none)",
			string(deleteScope))
	}

	trimmedSessionUUID := strings.TrimSpace(sessionUUID)
	if trimmedSessionUUID == "" && deleteScope != RecordScopeTenantShared {
		return nil, newValidationError("INVALID_PARAM: DeleteFile: sessionUUID is required")
	}
	trimmedIngestKeyPrefix := strings.TrimSpace(ingestKeyPrefix)
	if trimmedIngestKeyPrefix == "" {
		return nil, newValidationError("INVALID_PARAM: DeleteFile: ingestKeyPrefix is required")
	}

	params := url.Values{}
	if deleteScope != "" {
		params.Set("scope", string(deleteScope))
	}
	if trimmedSessionUUID != "" {
		params.Set("session", trimmedSessionUUID)
	}
	params.Set("ingest_key_prefix", trimmedIngestKeyPrefix)
	params.Set("dry_run", strconv.FormatBool(cfg.dryRun))

	respBytes, deleteErr := m.conn.DeleteWithParams(ctx, "/api/v1/records/by-file", params)
	if deleteErr != nil {
		return nil, deleteErr
	}

	deleteResult := &DeleteFileResult{}
	if unmarshalErr := json.Unmarshal(respBytes, deleteResult); unmarshalErr != nil {
		return nil, fmt.Errorf("DeleteFile: decode response: %w", unmarshalErr)
	}
	return deleteResult, nil
}
