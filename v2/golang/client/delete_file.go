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
// DeletedIDs e RaftIndex chegam com `omitempty` do servidor: em dry-run, ou
// quando nada casou, as chaves simplesmente não existem no JSON. Ausência aqui
// é informação legítima, não erro de decodificação.
type DeleteFileResult struct {
	SessionUUID     string  `json:"session_uuid"`
	IngestKeyPrefix string  `json:"ingest_key_prefix"`
	MatchedCount    int     `json:"matched_count"`
	DeletedCount    int     `json:"deleted_count"`
	DeletedIDs      []int64 `json:"deleted_ids,omitempty"`
	DryRun          bool    `json:"dry_run"`
	RaftIndex       int64   `json:"raft_index,omitempty"`
}

// DeleteFileOption configures a DeleteFile call.
type DeleteFileOption func(*deleteFileConfig)

// deleteFileConfig holds the optional parameters of DeleteFile.
type deleteFileConfig struct {
	dryRun bool
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
	trimmedSessionUUID := strings.TrimSpace(sessionUUID)
	if trimmedSessionUUID == "" {
		return nil, fmt.Errorf("DeleteFile: sessionUUID is required")
	}
	trimmedIngestKeyPrefix := strings.TrimSpace(ingestKeyPrefix)
	if trimmedIngestKeyPrefix == "" {
		return nil, fmt.Errorf("DeleteFile: ingestKeyPrefix is required")
	}

	cfg := &deleteFileConfig{}
	for _, applyOption := range opts {
		applyOption(cfg)
	}

	params := url.Values{}
	params.Set("session", trimmedSessionUUID)
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
