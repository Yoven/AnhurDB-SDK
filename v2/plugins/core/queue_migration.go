package core

// queue_migration.go — mover a fila ANTIGA (um .txt por chunk) para a fila com
// estado (queue.db), uma vez, sem perder nada.
//
// Separado de queue_bridge.go (corte de 300 linhas da casa, 2026-11-20): a
// migração é um domínio próprio — roda uma vez por stateDir, na abertura da
// fila — e muda por motivos diferentes do dreno (formato de nome de arquivo
// antigo, não política de entrega).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
