package core

// queue_bridge.go — liga a fila com estado (queue_store.go) ao motor, SEM abrir
// mão do piso de arquivo achatado.
//
// Junior Tip [a rede de segurança não pode depender do que ela protege]: esta é
// a lição que custou 12,8 dias de memória em julho de 2026. O arquivo lossless
// existia, mas estava atrás da MESMA guarda de chave que ele deveria compensar —
// então falhou exatamente quando era necessário. Aqui a regra é aplicada
// literalmente: se o SQLite não abrir (disco cheio, permissão, arquivo
// corrompido), queueChunk volta a escrever o .txt de sempre e flushQueue volta a
// drenar .txt. Um banco indisponível NUNCA pode custar uma memória.
//
// Junior Tip [ordem: por que o bloqueio é POR SESSÃO]: um item que falha bloqueia
// os itens seguintes DA MESMA sessão, e só dela. Dentro de uma sessão a ordem é a
// conversa — entregar o turno 7 antes do 5 escreve a conversa errada e nenhuma
// consolidação conserta. Entre sessões diferentes não há ordem a preservar, e
// bloquear tudo seria pior: uma rejeição permanente numa sessão (HTTP 409
// "session has reached the maximum of 500 records" — o caso real de 2026-07-03)
// travaria a fila inteira para sempre.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// queueDrainLimit é o teto de itens por dreno. Existe para o dreno não virar uma
// operação ilimitada num hook que precisa terminar rápido; quando é atingido, o
// excedente é REGISTRADO (nunca truncado em silêncio) e sai no dreno seguinte —
// que acontece a cada persist e a cada recall.
const queueDrainLimit = 500

// sharedQueueStore é aberto uma vez por processo.
//
// Junior Tip [por que um só, e por que não há Close]: o hook é um processo curto
// que roda um comando e morre. Abrir o banco por chunk repetiria PRAGMA + DDL a
// cada item sem ganho, e o SQLite aceita um escritor por vez de qualquer forma.
// Não fechar no fim é seguro porque `synchronous=FULL` faz cada COMMIT chegar ao
// disco antes de retornar: a durabilidade não depende de um desligamento limpo —
// e depender dele seria supor que o processo sempre morre bem, que é justamente
// o que não acontece.
// Junior Tip [a chave é o stateDir, não um sync.Once nu]: um Once global amarraria
// o processo inteiro ao PRIMEIRO stateDir visto. No binário isso passaria
// despercebido (um comando, um state dir), e no primeiro caso em que não passasse
// — dois plugins no mesmo processo, uma bateria de testes — as memórias de um
// destino iriam para o banco do outro em silêncio. O mapa torna o acoplamento
// impossível em vez de improvável.
var (
	queueStoreCacheMutex sync.Mutex
	queueStoreCache      = map[string]*queueStore{}
	queueStoreErrorCache = map[string]error{}
)

// queueStoreFor abre a fila (uma vez por stateDir) e migra o que sobrou do
// formato antigo de arquivos.
func queueStoreFor(cfg config) (*queueStore, error) {
	queueStoreCacheMutex.Lock()
	defer queueStoreCacheMutex.Unlock()
	if cached, found := queueStoreCache[cfg.stateDir]; found {
		return cached, nil
	}
	if cachedErr, found := queueStoreErrorCache[cfg.stateDir]; found {
		// Uma abertura que falhou não é retentada dentro do mesmo processo: o motivo
		// (disco, permissão, corrupção) não muda em milissegundos, e insistir só
		// atrasaria o caminho do piso, que é o que precisa rodar rápido.
		return nil, cachedErr
	}
	openedStore, openErr := openQueueStore(cfg.stateDir)
	if openErr != nil {
		queueStoreErrorCache[cfg.stateDir] = openErr
		return nil, openErr
	}
	migrateLegacyQueueFiles(cfg, openedStore)
	queueStoreCache[cfg.stateDir] = openedStore
	return openedStore, nil
}

// backlogNeedsAttention responde se há algo represado que mereça um aviso.
// Existe para que a decisão "vale a pena montar um bloco?" tenha UM dono, em vez
// de a mesma condição ser reescrita em cada ponto que a consulta.
func backlogNeedsAttention(backlog queueBacklog) bool {
	return backlog.chunkCount > 0 || backlog.quarantinedChunkCount > 0
}

// queueChunk grava uma memória não entregue. Nunca retorna erro ao chamador
// porque não existe ação alternativa: o dever aqui é não perder, e ele é cumprido
// descendo os degraus — banco, e se ele falhar, arquivo.
func queueChunk(cfg config, content string) {
	store, storeErr := queueStoreFor(cfg)
	if storeErr != nil {
		logLine(cfg, "queue store unavailable, using flat file (memory still safe): "+storeErr.Error())
		queueChunkToFile(cfg, content)
		return
	}

	// A sessão é extraída AQUI, no instante em que o conteúdo existe. Antes isso
	// só acontecia no dreno, e o veredito era refeito a cada tentativa.
	chunkSessionID := sessionFromChunk(content)
	if chunkSessionID == "" {
		// Junior Tip [quarentena na porta, NUNCA adivinhar a sessão]: escrever este
		// chunk sob qualquer outro id — o container, a conversa atual, a última
		// sessão registrada no cliente — fundiria duas conversas e violaria a regra
		// inviolável "1 sessão Claude = 1 sessão AnhurDB". Guardamos inteiro, com o
		// motivo, e deixamos a decisão para um humano.
		if quarantineErr := store.EnqueueQuarantined(content, "no session marker in chunk header"); quarantineErr != nil {
			logLine(cfg, "ERROR: quarantine enqueue failed, using flat file: "+quarantineErr.Error())
			queueChunkToFile(cfg, content)
			return
		}
		logLine(cfg, "ERROR: chunk has no identifiable session — QUARANTINED (never retried; needs a human)")
		return
	}

	if enqueueErr := store.EnqueueChunk(chunkSessionID, content); enqueueErr != nil {
		logLine(cfg, "queue enqueue FAILED, using flat file: "+enqueueErr.Error())
		queueChunkToFile(cfg, content)
		return
	}
	logLine(cfg, "queued chunk (session="+chunkSessionID+") -> "+store.path)
}

// migrateLegacyQueueFiles move os .txt do formato antigo para a fila com estado.
//
// Junior Tip [o arquivo só é apagado DEPOIS de o novo lar estar durável]: a ordem
// aqui é insere-commita-e-só-então-remove. Invertida, uma falha entre as duas
// operações apagaria a única cópia. Nenhum arquivo é abandonado: o que não puder
// migrar permanece no disco e será tentado de novo na próxima execução, e o piso
// (flushQueueFiles) continua sabendo drená-lo.
func migrateLegacyQueueFiles(cfg config, store *queueStore) {
	legacyEntries, readErr := os.ReadDir(cfg.queueDir())
	if readErr != nil {
		return // fila nunca usada: nada a migrar
	}
	migratedCount, failedCount := 0, 0
	for _, legacyEntry := range legacyEntries {
		if legacyEntry.IsDir() || !strings.HasSuffix(legacyEntry.Name(), ".txt") {
			continue
		}
		legacyPath := filepath.Join(cfg.queueDir(), legacyEntry.Name())
		legacyContent, contentErr := os.ReadFile(legacyPath)
		if contentErr != nil {
			failedCount++
			continue
		}
		// O instante vem do NOME do arquivo, não de agora — ver a Junior Tip de enqueueAt.
		queuedAt := legacyQueuedInstant(legacyEntry.Name())
		legacySessionID := sessionFromChunk(string(legacyContent))
		var migrateErr error
		if legacySessionID == "" {
			migrateErr = store.enqueueAt("(unknown)", string(legacyContent), queueStateQuarantined,
				"no session marker in chunk header (migrated from flat file)", queuedAt)
		} else {
			migrateErr = store.enqueueAt(legacySessionID, string(legacyContent), queueStatePending, "", queuedAt)
		}
		if migrateErr != nil {
			failedCount++
			continue
		}
		if removeErr := os.Remove(legacyPath); removeErr != nil {
			// O conteúdo já está no banco. O arquivo remanescente seria migrado de novo
			// na próxima execução e viraria duplicata — recuperável, ao contrário de perda,
			// mas ainda assim precisa ser VISÍVEL.
			logLine(cfg, "WARNING: migrated chunk but could not remove "+legacyPath+" (may duplicate): "+removeErr.Error())
		}
		migratedCount++
	}
	if migratedCount > 0 || failedCount > 0 {
		logLine(cfg, fmt.Sprintf("queue migration: %d chunk(s) moved into the state queue, %d left on disk for the next attempt",
			migratedCount, failedCount))
	}
}

// legacyQueuedInstant recupera o instante do nome antigo (ver queueChunkToFile).
// Um nome irreconhecível vira "agora", que ordena depois de tudo o que é datado —
// o mais seguro para algo cuja idade não pode ser provada.
func legacyQueuedInstant(chunkName string) time.Time {
	stampEnd := strings.Index(chunkName, "-")
	if stampEnd <= 0 {
		return time.Now().UTC()
	}
	queuedAt, parseErr := time.Parse("20060102T150405.000000000", chunkName[:stampEnd])
	if parseErr != nil {
		return time.Now().UTC()
	}
	return queuedAt.UTC()
}

// flushQueue tenta entregar tudo o que está represado e devolve o que continua
// preso, para cmdRecall mostrar no bloco <anhur-memory>.
func flushQueue(ctx context.Context, cfg config, mem *client.Memory) queueBacklog {
	store, storeErr := queueStoreFor(cfg)
	if storeErr != nil {
		// O piso. Não é caminho morto: é o que roda quando o banco não abre.
		logLine(cfg, "ERROR: queue store unavailable, draining flat files instead: "+storeErr.Error())
		return flushQueueFiles(ctx, cfg, mem)
	}
	return flushQueueStore(ctx, cfg, mem, store)
}

// flushQueueStore drena a fila com estado, em ordem cronológica, parando por
// sessão na primeira falha daquela sessão.
func flushQueueStore(ctx context.Context, cfg config, mem *client.Memory, store *queueStore) queueBacklog {
	var backlog queueBacklog

	claimedItems, claimErr := store.ClaimNextBatch(queueDrainLimit)
	if claimErr != nil {
		logLine(cfg, "ERROR: cannot read the queue (backlog may be under-reported): "+claimErr.Error())
		backlog.lastFlushErr = claimErr
		return backlog
	}
	if len(claimedItems) == queueDrainLimit {
		// Nenhum corte silencioso: se houver mais, o log diz que houve.
		logLine(cfg, fmt.Sprintf("queue drain hit the %d-item limit; the remainder goes out on the next persist/recall", queueDrainLimit))
	}

	blockedSessions := make(map[string]bool)
	deliveredCount := 0
	for _, item := range claimedItems {
		if blockedSessions[item.SessionID] {
			// Esta sessão já falhou neste dreno: soltar sem punir preserva a ordem da
			// conversa e não inflaciona o backoff de um item que nem foi tentado.
			if releaseErr := store.Release(item.ID); releaseErr != nil {
				logLine(cfg, "ERROR: cannot release queued item: "+releaseErr.Error())
			}
			continue
		}
		// Servidor exige sessão registrada antes do write (session-first). Idempotente.
		if _, createSessionErr := mem.CreateSession(ctx, client.WithCreateSessionID(item.SessionID)); createSessionErr != nil {
			logLine(cfg, fmt.Sprintf("flush CreateSession failed (session=%s, attempt %d): %v",
				item.SessionID, item.RetryCount+1, createSessionErr))
			markQueueFailure(cfg, store, item, createSessionErr, blockedSessions, &backlog)
			continue
		}
		// Junior Tip [sessão fixada EXPLICITAMENTE em todo Add]: CreateSession muda a
		// sessão registrada do cliente compartilhado como efeito colateral, e chunks de
		// conversas DIFERENTES passam por este laço em sequência. WithSessionID torna a
		// escrita independente desse estado compartilhado — um id explícito sempre vence
		// dentro do resolveWriteSessionID do SDK — então nenhum chunk herda a sessão do
		// vizinho.
		if _, addErr := mem.Add(ctx, item.Content, client.WithSessionID(item.SessionID)); addErr != nil {
			logLine(cfg, fmt.Sprintf("flush still failing (session=%s, attempt %d, queued %s): %v",
				item.SessionID, item.RetryCount+1, item.CreatedAt.UTC().Format(time.RFC3339), addErr))
			markQueueFailure(cfg, store, item, addErr, blockedSessions, &backlog)
			continue
		}
		if deliveredErr := store.MarkDelivered(item.ID); deliveredErr != nil {
			// Gravado no AnhurDB mas não removido da fila: será reenviado e pode duplicar.
			// Duplicata é recuperável; perda não é. Mas tem de ficar VISÍVEL.
			logLine(cfg, "WARNING: delivered but could not clear the queue entry (may resend): "+deliveredErr.Error())
		}
		deliveredCount++
	}
	if deliveredCount > 0 {
		logLine(cfg, fmt.Sprintf("flushed %d queued chunk(s)", deliveredCount))
	}

	summary, summarizeErr := store.Summarize()
	if summarizeErr != nil {
		logLine(cfg, "ERROR: cannot summarize the queue (backlog invisible): "+summarizeErr.Error())
		return backlog
	}
	backlog.chunkCount = summary.PendingCount
	backlog.oldestChunk = summary.OldestPendingAt
	backlog.quarantinedChunkCount = summary.QuarantinedCount
	backlog.oldestQuarantined = summary.OldestQuarantinedAt
	if backlog.lastFlushErr == nil && summary.LastError != "" {
		// Junior Tip [o motivo TEM de sobreviver ao silêncio, 2026-07-31]: quando
		// todos os itens presos estão cumprindo backoff, este dreno não tenta nada e
		// não produz erro — mas os itens continuam presos. Sem esta linha o recall
		// mostraria "N chunks parados" sem dizer por quê, que é exatamente o estado
		// que a fila com estado existe para acabar. O motivo vem persistido da última
		// tentativa real.
		backlog.lastFlushErr = errors.New(summary.LastError)
	}

	// "Assim que voltar, descarregar tudo que tem local e limpar o local pra não ter
	// risco" — a segunda metade da regra. Só limpa quando NADA resta (incluindo
	// quarentena, ver PurgeIfDrained), e a limpeza vem depois de o backlog já ter sido
	// lido: a limpeza nunca pode apagar o próprio aviso.
	if backlog.chunkCount == 0 && backlog.quarantinedChunkCount == 0 {
		purged, purgeErr := store.PurgeIfDrained()
		if purgeErr != nil {
			logLine(cfg, "WARNING: local queue is empty but could not be reclaimed: "+purgeErr.Error())
		} else if purged && deliveredCount > 0 {
			logLine(cfg, "local queue fully drained and cleared — nothing is held outside AnhurDB")
		}
	}
	return backlog
}

// Junior Tip [por que NÃO existe uma varredura que apaga .txt restantes, 2026-07-31]:
// a tentação é, com a fila zerada, limpar o diretório antigo de uma vez. Mas o piso
// (queueChunkToFile) escreve exatamente ali, e dois hooks podem rodar ao mesmo tempo:
// bastaria o processo B não conseguir abrir o banco, gravar seu .txt, e o processo A
// — cuja fila está vazia — apagá-lo. Seria perda silenciosa criada pela própria
// rotina de limpeza. O único lugar que remove um .txt é migrateLegacyQueueFiles, e
// só DEPOIS de o conteúdo estar durável no banco. Ler antes de apagar não é
// preciosismo: é a diferença entre limpar e destruir.

// markQueueFailure registra a falha, bloqueia a sessão pelo resto deste dreno e
// garante que o item volte para pending mesmo se o UPDATE falhar.
func markQueueFailure(cfg config, store *queueStore, item queueItem, cause error,
	blockedSessions map[string]bool, backlog *queueBacklog) {
	blockedSessions[item.SessionID] = true
	backlog.lastFlushErr = cause
	if failErr := store.MarkFailed(item.ID, item.RetryCount, cause); failErr != nil {
		// Sem isto o item ficaria em "sending" até o resgate de 5 minutos, atrasando
		// o dreno seguinte por um erro de contabilidade, não de entrega.
		logLine(cfg, "ERROR: cannot record the queue failure: "+failErr.Error())
		if releaseErr := store.Release(item.ID); releaseErr != nil {
			logLine(cfg, "ERROR: cannot release the item either (it will be reclaimed after the stale timeout): "+releaseErr.Error())
		}
	}
}
