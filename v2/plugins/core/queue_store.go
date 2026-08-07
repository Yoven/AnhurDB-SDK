package core

// queue_store.go — a fila de memória não entregue, com ESTADO EXPLÍCITO.
//
// Junior Tip [por que sair do nome de arquivo — as duas falhas que isto mata]:
// a fila antiga guardava tudo no NOME do arquivo (timestamp-pid-seq.txt) e nada
// mais. Duas consequências, as duas pagas em produção:
//
//  1. Um chunk que falhava ficava indistinguível de um recém-chegado. O log
//     repetia "flush still failing ... (retried on every persist)" 52 vezes sem
//     dizer HÁ QUANTO TEMPO, QUANTAS VEZES, nem POR QUÊ. Quando a chave rotacionou
//     em 2026-07-14, os chunks anteriores viraram lixo permanente — cada persist
//     re-tentava com a chave nova algo que só a antiga aceitaria — e ninguém tinha
//     como perceber, porque o estado não existia em lugar nenhum.
//  2. Dois chunks no mesmo instante colidiam de nome e um sobrescrevia o outro em
//     silêncio (corrigido em 2026-07-04 com nanossegundo+pid+seq — mas a correção
//     foi tornar a CHAVE mais única, não tornar a escrita atômica).
//
// Com estado explícito, "o que está preso e por quê" vira uma pergunta com
// resposta, e enfileirar vira uma transação.
//
// Junior Tip [SQLite em Go PURO, e por que isso não é preciosismo]: o driver
// obrigatório é modernc.org/sqlite (transpilado), NUNCA mattn/go-sqlite3 (cgo).
// O plugin promete no cabeçalho "ZERO runtime dependencies ... SINGLE static
// binary" e é cross-compilado para 4 alvos por `make release-binaries`. cgo
// quebraria a compilação cruzada e exigiria toolchain C em cada plataforma —
// trocaríamos uma classe de bug por um problema de distribuição.
//
// Junior Tip [o arquivo achatado continua sendo o PISO — a lição de 2026-07-30]:
// hoje o arquivo lossless morreu porque estava atrás da mesma guarda que ele
// deveria proteger. A regra que saiu disso: a rede de segurança nunca pode
// depender daquilo que ela protege. Então se o SQLite não abrir — disco cheio,
// permissão, arquivo corrompido — enqueueMemoryChunk cai para o arquivo .txt de
// sempre. Um banco indisponível NUNCA pode custar uma memória.

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	// Junior Tip [import anônimo obrigatório, e por que o driver É este]:
	// database/sql não conhece nenhum banco — ele resolve o nome "sqlite" num
	// registro global que só é populado pelo efeito colateral deste import. Sem
	// ele o código COMPILA e falha em runtime com "unknown driver", que é
	// exatamente a classe de erro que este projeto não tolera. E o driver é
	// modernc (Go puro, transpilado), NUNCA mattn/go-sqlite3 (cgo): o plugin é
	// cross-compilado para 4 plataformas por `make release-binaries` e promete
	// binário estático sem dependência de runtime.
	_ "modernc.org/sqlite"
)

// Estados de um item na fila. Deliberadamente poucos: cada estado extra é uma
// transição a mais para alguém errar.
const (
	// queueStatePending: aguardando envio. É o único estado que o dreno pega.
	queueStatePending = "pending"

	// queueStateSending: envio em curso. Existe para o caso de o processo morrer
	// no meio — ver reclaimStaleSendingItems.
	queueStateSending = "sending"

	// queueStateQuarantined: NUNCA mais será tentado automaticamente. Reservado
	// para o que não pode ser reparado adivinhando: chunk sem sessão
	// identificável. Adivinhar a sessão violaria o invariante inviolável
	// "1 sessão do agente = 1 sessão AnhurDB" e fundiria conversas.
	queueStateQuarantined = "quarantined"
)

// queueSendingTimeout é quanto tempo um item pode ficar em "sending" antes de
// ser considerado abandonado por um processo morto.
//
// Junior Tip [por que existe]: o hook é um processo curto que pode ser morto a
// qualquer momento (fim de sessão, SIGTERM, máquina suspensa). Sem esta
// reivindicação, um item marcado "sending" no instante da morte ficaria preso
// para sempre — exatamente o modo de falha que esta tabela existe para acabar.
// Cinco minutos é generoso perto do timeout HTTP (15s por padrão) e curto perto
// do intervalo entre persists.
const queueSendingTimeout = 5 * time.Minute

// queueMaxBackoff limita o adiamento entre tentativas.
//
// Junior Tip [backoff serve ao BANCO, não ao chunk]: sem ele, um endpoint fora
// do ar leva o plugin a martelar a rede a cada persist. Com o teto, um backend
// morto custa uma tentativa a cada 30 min por item, e o item continua lá quando
// ele voltar. O que NUNCA acontece é desistir: não há limite de tentativas,
// porque "tentei demais" jamais pode virar "joguei fora".
const queueMaxBackoff = 30 * time.Minute

// queueStore é a fila persistente.
type queueStore struct {
	database *sql.DB
	path     string
}

// queueItem é uma memória aguardando entrega.
type queueItem struct {
	ID         int64
	SessionID  string
	Content    string
	RetryCount int
	CreatedAt  time.Time
	LastError  string
}

// queueSchema é criado na abertura. IF NOT EXISTS torna a abertura idempotente.
//
// Junior Tip [por que session_id é NOT NULL]: um item sem sessão não pode ser
// entregue nem adivinhado — o invariante do dono proíbe. Recusar na COLUNA
// significa que o caminho "gravei sem sessão e descubro depois" não existe:
// quem não tem sessão vai para quarentena na entrada, com o motivo registrado.
const queueSchema = `
CREATE TABLE IF NOT EXISTS pending_memory (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT    NOT NULL,
    content         TEXT    NOT NULL,
    state           TEXT    NOT NULL DEFAULT 'pending',
    retry_count     INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    next_attempt_at TEXT    NOT NULL,
    state_changed_at TEXT   NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pending_drain
    ON pending_memory(state, next_attempt_at, created_at);
`

// openQueueStore abre (ou cria) a fila. O chamador DEVE tratar erro caindo para
// o arquivo achatado — ver a Junior Tip do topo.
func openQueueStore(stateDir string) (*queueStore, error) {
	databasePath := filepath.Join(stateDir, "queue.db")
	database, openErr := sql.Open("sqlite", databasePath)
	if openErr != nil {
		return nil, fmt.Errorf("queue store: open %s: %w", databasePath, openErr)
	}
	// Junior Tip [uma conexão só, de propósito]: o hook é monoprocesso e
	// curtíssimo. Um pool aqui só criaria contenção de lock do SQLite sem ganho.
	database.SetMaxOpenConns(1)

	// WAL sobrevive melhor a morte abrupta do processo, que é o normal para um hook.
	if _, pragmaErr := database.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;"); pragmaErr != nil {
		database.Close()
		return nil, fmt.Errorf("queue store: pragmas: %w", pragmaErr)
	}
	if _, schemaErr := database.Exec(queueSchema); schemaErr != nil {
		database.Close()
		return nil, fmt.Errorf("queue store: schema: %w", schemaErr)
	}
	return &queueStore{database: database, path: databasePath}, nil
}

// Close libera a conexão.
func (store *queueStore) Close() error {
	if store == nil || store.database == nil {
		return nil
	}
	return store.database.Close()
}

// EnqueueChunk grava uma memória não entregue. Sessão vazia é recusada ALTO — o
// chamador decide entre quarentena e erro, mas nunca inventa sessão.
func (store *queueStore) EnqueueChunk(sessionID string, content string) error {
	return store.enqueueAt(sessionID, content, queueStatePending, "", time.Now().UTC())
}

// EnqueueQuarantined guarda um chunk cuja sessão não pôde ser provada. Ele é
// preservado inteiro, contado, mostrado no bloco de recall — e NUNCA enviado.
//
// Junior Tip [por que a quarentena acontece na ENTRADA agora]: antes, a decisão
// era tomada no dreno, uma vez por tentativa, para sempre. Decidir na entrada
// significa que o motivo é registrado no instante em que a informação existe
// (temos o conteúdo em mãos) e que o dreno nunca mais precisa reavaliar. Sessão
// vazia vira um estado, não um caminho de código repetido.
func (store *queueStore) EnqueueQuarantined(content string, reason string) error {
	// A coluna é NOT NULL; o marcador explícito deixa claro na inspeção manual
	// que a ausência de sessão é o fato registrado, não um dado perdido.
	return store.enqueueAt("(unknown)", content, queueStateQuarantined, reason, time.Now().UTC())
}

// enqueueAt é o INSERT compartilhado. createdAt é parâmetro porque a migração
// dos arquivos antigos precisa preservar o instante original.
//
// Junior Tip [por que created_at não pode ser "agora" na migração]: os itens são
// pedaços de conversa e o dreno os entrega por created_at. Carimbar todos com o
// instante da migração embaralharia turnos que estavam corretamente ordenados no
// nome do arquivo — a migração destruiria justamente o que ela existe para salvar.
func (store *queueStore) enqueueAt(sessionID string, content string, state string, reason string, createdAt time.Time) error {
	if sessionID == "" {
		return errors.New("queue store: refusing to enqueue without a session id (guessing would merge conversations)")
	}
	createdStamp := createdAt.UTC().Format(time.RFC3339Nano)
	nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, execErr := store.database.Exec(
		`INSERT INTO pending_memory (session_id, content, state, last_error, created_at, next_attempt_at, state_changed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, content, state, truncateRunes(reason, 300), createdStamp, createdStamp, nowStamp,
	)
	if execErr != nil {
		return fmt.Errorf("queue store: enqueue: %w", execErr)
	}
	return nil
}

// Release devolve um item reivindicado para pending SEM contar tentativa e SEM
// backoff.
//
// Junior Tip [soltar não é falhar]: quando o dreno para de tentar uma sessão
// porque a anterior falhou (bloqueio de cabeça de fila, que preserva a ordem da
// conversa), os itens seguintes daquela sessão nunca chegaram a ser enviados.
// Marcá-los como falha inflaria retry_count e adiaria a recuperação por um erro
// que não foi deles. Sem este método eles ficariam presos em "sending" até o
// resgate de 5 minutos — atrasando o dreno seguinte sem motivo.
func (store *queueStore) Release(itemID int64) error {
	nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, execErr := store.database.Exec(
		`UPDATE pending_memory SET state = ?, state_changed_at = ? WHERE id = ?`,
		queueStatePending, nowStamp, itemID,
	)
	if execErr != nil {
		return fmt.Errorf("queue store: release: %w", execErr)
	}
	return nil
}

// ClaimNextBatch devolve os itens prontos para envio, do mais ANTIGO para o mais
// novo, e os marca como "sending".
//
// Junior Tip [ordem cronológica não é estética — é a memória]: os itens são
// pedaços de conversa. Entregar o turno 7 antes do 5 escreve a conversa fora de
// ordem no banco, e nenhuma consolidação posterior conserta isso. Por isso
// created_at ASC, e por isso o dreno PARA na primeira falha (ver o chamador) em
// vez de pular para o próximo.
func (store *queueStore) ClaimNextBatch(maximumItems int) ([]queueItem, error) {
	if err := store.reclaimStaleSendingItems(); err != nil {
		return nil, err
	}
	nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
	rows, queryErr := store.database.Query(
		`SELECT id, session_id, content, retry_count, created_at, last_error
		   FROM pending_memory
		  WHERE state = ? AND next_attempt_at <= ?
		  ORDER BY created_at ASC, id ASC
		  LIMIT ?`,
		queueStatePending, nowStamp, maximumItems,
	)
	if queryErr != nil {
		return nil, fmt.Errorf("queue store: claim: %w", queryErr)
	}
	defer rows.Close()

	claimedItems := make([]queueItem, 0, maximumItems)
	for rows.Next() {
		var item queueItem
		var createdAtText string
		if scanErr := rows.Scan(&item.ID, &item.SessionID, &item.Content,
			&item.RetryCount, &createdAtText, &item.LastError); scanErr != nil {
			return nil, fmt.Errorf("queue store: claim scan: %w", scanErr)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtText)
		claimedItems = append(claimedItems, item)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("queue store: claim rows: %w", rowsErr)
	}
	// Junior Tip [fechar ANTES do UPDATE, e por que isso não é zelo à toa]: o pool
	// tem UMA conexão (SetMaxOpenConns(1)). Enquanto um *sql.Rows está aberto, ele
	// é dono dessa conexão — qualquer Exec abaixo ficaria esperando para sempre.
	// Hoje funciona por acidente: rows.Next() esgotado libera a conexão sozinho. Um
	// `return` antecipado dentro do laço de leitura (uma linha a mais que alguém
	// acrescente amanhã) transformaria isso num travamento do hook, sem erro e sem
	// log. Fechar explicitamente tira o acidente do caminho.
	if closeErr := rows.Close(); closeErr != nil {
		return nil, fmt.Errorf("queue store: claim close: %w", closeErr)
	}

	for _, claimed := range claimedItems {
		if _, markErr := store.database.Exec(
			`UPDATE pending_memory SET state = ?, state_changed_at = ? WHERE id = ?`,
			queueStateSending, nowStamp, claimed.ID,
		); markErr != nil {
			return nil, fmt.Errorf("queue store: mark sending: %w", markErr)
		}
	}
	return claimedItems, nil
}

// MarkDelivered remove o item: entregue é entregue.
//
// Junior Tip [por que apagar em vez de marcar "sent"]: manter o histórico de
// entregues transformaria a fila num arquivo paralelo ao archive/, com política
// de retenção própria e o risco de reenviar algo já gravado. O AnhurDB é a
// memória; esta tabela é só a sala de espera.
func (store *queueStore) MarkDelivered(itemID int64) error {
	if _, execErr := store.database.Exec(`DELETE FROM pending_memory WHERE id = ?`, itemID); execErr != nil {
		return fmt.Errorf("queue store: mark delivered: %w", execErr)
	}
	return nil
}

// MarkFailed devolve o item para pending com backoff e registra a causa.
// NUNCA descarta: não existe contador de desistência.
func (store *queueStore) MarkFailed(itemID int64, retryCount int, cause error) error {
	causeText := ""
	if cause != nil {
		causeText = truncateRunes(cause.Error(), 300)
	}
	nowInstant := time.Now().UTC()
	_, execErr := store.database.Exec(
		`UPDATE pending_memory
		    SET state = ?, retry_count = ?, last_error = ?, next_attempt_at = ?, state_changed_at = ?
		  WHERE id = ?`,
		queueStatePending, retryCount+1, causeText,
		nowInstant.Add(backoffForAttempt(retryCount+1)).Format(time.RFC3339Nano),
		nowInstant.Format(time.RFC3339Nano), itemID,
	)
	if execErr != nil {
		return fmt.Errorf("queue store: mark failed: %w", execErr)
	}
	return nil
}

// Quarantine tira o item do ciclo automático PARA SEMPRE, com o motivo.
func (store *queueStore) Quarantine(itemID int64, reason string) error {
	nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, execErr := store.database.Exec(
		`UPDATE pending_memory SET state = ?, last_error = ?, state_changed_at = ? WHERE id = ?`,
		queueStateQuarantined, truncateRunes(reason, 300), nowStamp, itemID,
	)
	if execErr != nil {
		return fmt.Errorf("queue store: quarantine: %w", execErr)
	}
	return nil
}

// reclaimStaleSendingItems devolve para pending o que ficou preso em "sending"
// além do timeout — herança de um processo morto no meio do envio.
//
// Junior Tip [pode reenviar, e o que REALMENTE acontece então — corrigido
// 2026-07-31]: se o processo morreu DEPOIS de o servidor gravar mas ANTES de
// marcar entregue, a reivindicação reenvia. Este comentário dizia que "a
// consolidação deduplica por checksum" e isso era FALSO: o checksum da
// consolidação (AnhurAgents/cmd/consolidation/source_hash.go) serve para PULAR
// uma chamada de LLM quando o conjunto-fonte não mudou — ele nunca funde dois
// registros.
//
// A proteção real é outra e mora no servidor: CreateRecord tem um curto-circuito
// de idempotência por (uuid, type, checksum) dentro de ANHUR_DEDUP_WINDOW (30s
// por padrão), e o /ingest passa por ele (service/ingest.go:157). Isso cobre a
// rajada — a retentativa em voo de deliverWithRetry, com 300ms/1200ms de espera,
// cai confortavelmente dentro da janela.
//
// O que a janela NÃO cobre é justamente ESTE caminho: um reenvio da fila acontece
// minutos ou horas depois, muito fora dos 30s, e o servidor o trata como evento
// distinto legítimo — por decisão explícita dele (record_create.go: "same content
// outside the window is a LEGITIMATE distinct event and MUST be preserved").
// Medição de 2026-07-31 no banco real: 3 chunks byte-idênticos duplicados em 120,
// com 17m04s e 24m42s de intervalo — exatamente esta assinatura.
//
// A escolha continua a mesma e continua certa: duplicar é recuperável, perder não
// é. Mas ela agora está registrada pelo que é — uma dívida conhecida, não um
// problema que alguém lá embaixo resolve. Fechá-la exige idempotência ponta-a-
// ponta (uma chave de idempotência por chunk que o servidor honre fora da janela),
// e isso é mudança no AnhurDB, não aqui.
func (store *queueStore) reclaimStaleSendingItems() error {
	staleBefore := time.Now().UTC().Add(-queueSendingTimeout).Format(time.RFC3339Nano)
	nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, execErr := store.database.Exec(
		`UPDATE pending_memory
		    SET state = ?, state_changed_at = ?
		  WHERE state = ? AND state_changed_at < ?`,
		queueStatePending, nowStamp, queueStateSending, staleBefore,
	)
	if execErr != nil {
		return fmt.Errorf("queue store: reclaim stale sending: %w", execErr)
	}
	return nil
}

// queueStoreSummary é o retrato que o bloco de memória mostra ao usuário.
type queueStoreSummary struct {
	PendingCount        int
	QuarantinedCount    int
	OldestPendingAt     string
	OldestQuarantinedAt string
	LastError           string
}

// Summarize responde "o que está preso, há quanto tempo e por quê" — a pergunta
// que a fila baseada em nome de arquivo não sabia responder.
func (store *queueStore) Summarize() (queueStoreSummary, error) {
	var summary queueStoreSummary
	countRow := store.database.QueryRow(
		`SELECT
		   SUM(CASE WHEN state IN (?, ?) THEN 1 ELSE 0 END),
		   SUM(CASE WHEN state = ? THEN 1 ELSE 0 END)
		 FROM pending_memory`,
		queueStatePending, queueStateSending, queueStateQuarantined,
	)
	var pendingCount, quarantinedCount sql.NullInt64
	if scanErr := countRow.Scan(&pendingCount, &quarantinedCount); scanErr != nil {
		return summary, fmt.Errorf("queue store: summarize counts: %w", scanErr)
	}
	summary.PendingCount = int(pendingCount.Int64)
	summary.QuarantinedCount = int(quarantinedCount.Int64)

	oldestRow := store.database.QueryRow(
		`SELECT created_at, last_error FROM pending_memory
		  WHERE state IN (?, ?) ORDER BY created_at ASC LIMIT 1`,
		queueStatePending, queueStateSending,
	)
	var oldestAt, lastError sql.NullString
	if scanErr := oldestRow.Scan(&oldestAt, &lastError); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return summary, fmt.Errorf("queue store: summarize oldest: %w", scanErr)
	}
	summary.OldestPendingAt = normalizeQueueStamp(oldestAt.String)
	summary.LastError = lastError.String

	oldestQuarantinedRow := store.database.QueryRow(
		`SELECT created_at FROM pending_memory WHERE state = ? ORDER BY created_at ASC LIMIT 1`,
		queueStateQuarantined,
	)
	var oldestQuarantinedAt sql.NullString
	if scanErr := oldestQuarantinedRow.Scan(&oldestQuarantinedAt); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return summary, fmt.Errorf("queue store: summarize oldest quarantined: %w", scanErr)
	}
	summary.OldestQuarantinedAt = normalizeQueueStamp(oldestQuarantinedAt.String)
	return summary, nil
}

// PurgeIfDrained apaga o estado local quando não há mais nada esperando entrega.
// Devolve true quando a limpeza aconteceu.
//
// Junior Tip [a regra do dono: "limpar o local pra não ter risco", 2026-07-31]:
// depois que tudo foi entregue, o arquivo local não guarda mais nada que o AnhurDB
// não tenha — ele só guarda RISCO. Páginas com conteúdo de conversa continuam
// legíveis no disco muito depois do DELETE (o SQLite marca a página como livre, não
// a sobrescreve), e um banco que só cresce vira um segundo depósito de memória fora
// do lugar onde a memória deve estar. VACUUM reescreve o arquivo sem as páginas
// mortas: o túnel volta a ser um túnel.
//
// Junior Tip [por que a quarentena SEGURA a limpeza]: chunk em quarentena é o único
// dado que existe SÓ aqui — ele nunca chegou ao AnhurDB e, por definição, ninguém
// sabe a que conversa pertence. Limpar com quarentena presente seria a perda
// silenciosa que este arquivo inteiro existe para impedir. Enquanto houver um, o
// local permanece — e o aviso reaparece em todo recall até um humano resolver.
func (store *queueStore) PurgeIfDrained() (bool, error) {
	var remainingCount int
	if scanErr := store.database.QueryRow(
		`SELECT COUNT(*) FROM pending_memory`,
	).Scan(&remainingCount); scanErr != nil {
		return false, fmt.Errorf("queue store: purge check: %w", scanErr)
	}
	if remainingCount > 0 {
		return false, nil
	}
	// VACUUM não roda dentro de transação e devolve o espaço ao sistema de arquivos.
	if _, vacuumErr := store.database.Exec("VACUUM"); vacuumErr != nil {
		return false, fmt.Errorf("queue store: vacuum: %w", vacuumErr)
	}
	return true, nil
}

// ListPending devolve, só para leitura, os chunks aguardando entrega.
//
// Junior Tip [por que não reusar ClaimNextBatch]: aquele MARCA os itens como
// "sending". Uma inspeção que muda o estado do que inspeciona transformaria um
// diagnóstico em efeito colateral — e um item deixado em "sending" por alguém que
// só queria olhar ficaria fora do dreno até o resgate de 5 minutos.
func (store *queueStore) ListPending() ([]queueItem, error) {
	return store.listByState(queueStatePending, queueStateSending)
}

// ListQuarantined devolve, inteiros, os chunks que aguardam decisão humana.
//
// Junior Tip [para que serve na prática]: a quarentena nunca se resolve sozinha —
// por definição, ninguém sabe a que conversa o chunk pertence. Quem for resolver
// precisa LER o conteúdo para reconhecer a origem. Sem esta leitura, a quarentena
// seria um buraco: preserva o dado e o torna inalcançável, que para o usuário é
// indistinguível de perdê-lo.
func (store *queueStore) ListQuarantined() ([]queueItem, error) {
	return store.listByState(queueStateQuarantined)
}

// listByState é a leitura compartilhada por ListPending e ListQuarantined.
// Nunca altera estado — ver a Junior Tip de ListPending.
func (store *queueStore) listByState(states ...string) ([]queueItem, error) {
	placeholders := make([]string, len(states))
	arguments := make([]interface{}, len(states))
	for stateIndex, stateName := range states {
		placeholders[stateIndex] = "?"
		arguments[stateIndex] = stateName
	}
	rows, queryErr := store.database.Query(
		`SELECT id, session_id, content, retry_count, created_at, last_error
		   FROM pending_memory WHERE state IN (`+strings.Join(placeholders, ",")+`)
		  ORDER BY created_at ASC, id ASC`,
		arguments...,
	)
	if queryErr != nil {
		return nil, fmt.Errorf("queue store: list %v: %w", states, queryErr)
	}
	defer rows.Close()

	listedItems := []queueItem{}
	for rows.Next() {
		var item queueItem
		var createdAtText string
		if scanErr := rows.Scan(&item.ID, &item.SessionID, &item.Content,
			&item.RetryCount, &createdAtText, &item.LastError); scanErr != nil {
			return nil, fmt.Errorf("queue store: list %v scan: %w", states, scanErr)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtText)
		listedItems = append(listedItems, item)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("queue store: list %v rows: %w", states, rowsErr)
	}
	return listedItems, nil
}

// normalizeQueueStamp reduz o carimbo interno (nanossegundos) ao RFC3339 que o
// bloco <anhur-memory> mostra ao usuário. Um carimbo ilegível é devolvido como
// está: a data é um detalhe do aviso, e nunca pode custar o aviso em si.
func normalizeQueueStamp(storedStamp string) string {
	if storedStamp == "" {
		return ""
	}
	parsed, parseErr := time.Parse(time.RFC3339Nano, storedStamp)
	if parseErr != nil {
		return storedStamp
	}
	return parsed.UTC().Format(time.RFC3339)
}

// backoffForAttempt dobra a espera a cada tentativa, com teto.
func backoffForAttempt(attemptNumber int) time.Duration {
	if attemptNumber < 1 {
		attemptNumber = 1
	}
	backoff := time.Duration(1<<uint(minimumInt(attemptNumber-1, 12))) * time.Minute
	if backoff > queueMaxBackoff || backoff <= 0 {
		return queueMaxBackoff
	}
	return backoff
}

// minimumInt evita overflow do shift em backoffForAttempt.
func minimumInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
