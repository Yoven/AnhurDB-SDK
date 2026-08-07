package core

// reconcile.go — o cursor diz "tentei"; este arquivo diz "chegou".
//
// Junior Tip [o buraco que isto fecha, 2026-07-31]: em cmdPersist o cursor avança
// ANTES de qualquer envio (core.go, writeCursor logo após extractConversation). O
// comentário de lá justifica: todo chunk abaixo é enviado ou enfileirado, nunca
// descartado. Isso é verdade SE o processo viver até o fim do laço. Se ele morrer
// no meio — SIGTERM no fim da sessão, máquina suspensa, OOM — o cursor já avançou
// e aquelas linhas nunca mais são olhadas: não foram enviadas, não entraram na
// fila, e a fila nem sabe que existiram. Somem do feed episódico para sempre.
//
// Mas elas estão no archive verbatim, que grava sempre e antes de tudo. A
// reconciliação recupera exatamente esse delta.
//
// Junior Tip [por que LINHA e não hash de conteúdo]: a ideia óbvia seria guardar
// o hash de cada chunk entregue e comparar depois. Não funciona: o chunk carrega
// time.Now() no cabeçalho e um rótulo [parte i/N] que depende de como AQUELE
// delta foi fatiado. Re-derivar do archive produz cabeçalho e fatiamento
// diferentes, logo hash diferente — a reconciliação veria buraco em tudo e
// reenviaria a sessão inteira. A faixa de linhas é estável; o hash não é.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// confirmedCursorSuffix distingue o cursor de confirmação do cursor de tentativa,
// que vivem no mesmo diretório e são lidos pelas mesmas funções.
const confirmedCursorSuffix = ".confirmed"

// confirmedCursorPath devolve o arquivo que marca até onde o AnhurDB confirmou.
func confirmedCursorPath(cfg config, sessionID string) string {
	return filepath.Join(cfg.cursorDir(), sanitize(sessionID)+confirmedCursorSuffix)
}

// markDeliveryConfirmed avança o cursor de confirmação — só o chamador que sabe
// que TODOS os chunks do delta foram aceitos deve chamá-lo.
//
// Junior Tip [tudo ou nada, de propósito]: se um chunk do delta foi para a fila,
// o cursor de confirmação NÃO avança. Ele fica atrás, a fila entrega, e o próximo
// persist confirma. Avançar parcialmente exigiria rastrear quais chunks dentro do
// delta faltaram — complexidade que só existiria para economizar um reenvio raro.
func markDeliveryConfirmed(cfg config, sessionID string, lineCount int) {
	writeCursor(confirmedCursorPath(cfg, sessionID), lineCount)
}

// resolveConfirmedCount devolve até onde o AnhurDB confirmou, SEMEANDO o cursor de
// confirmação a partir do de tentativa quando ele ainda não existe.
//
// Junior Tip [o incidente de 2026-07-31, causado por esta função não existir]: o
// cursor de confirmação nasce em zero. Numa instalação que já vinha rodando, TODA
// sessão antiga tem archive cheio e confirmação zero — então a primeira execução
// do código novo lê "o archive tem 755 linhas, nada foi confirmado" e reenvia a
// conversa inteira. Foi exatamente o que aconteceu no deploy: 6 chunks duplicados
// na memória real do dono, e outras 26 sessões prestes a fazer o mesmo no persist
// seguinte. A reconciliação, escrita para impedir perda, virou uma fábrica de
// duplicata — o modo de falha oposto, e igualmente meu.
//
// A semente correta é o cursor de TENTATIVA: o código antigo só o avançava depois
// de processar o delta, e todo chunk daquele delta foi enviado ou enfileirado (a
// fila cuida dos enfileirados). Tratar "tentado" como "confirmado" no momento da
// migração é conservador na direção certa: na pior hipótese deixa de recuperar um
// buraco antigo raro; a alternativa reescreve históricos inteiros, que é dano
// garantido e imediato.
//
// Junior Tip [semear é uma operação de UMA vez, e por isso ela GRAVA]: se apenas
// devolvêssemos o valor sem persistir, cada execução re-semearia a partir de um
// cursor de tentativa que continua avançando, e a reconciliação nunca enxergaria
// um buraco de verdade. Gravar fixa a linha de partida.
func resolveConfirmedCount(cfg config, sessionID string) int {
	confirmedPath := confirmedCursorPath(cfg, sessionID)
	if _, statErr := os.Stat(confirmedPath); statErr == nil {
		return readCursor(confirmedPath)
	}
	attemptedCount := readCursor(filepath.Join(cfg.cursorDir(), sanitize(sessionID)))
	if attemptedCount > 0 {
		logLine(cfg, fmt.Sprintf("reconcile: seeding the confirmed cursor for session %s at line %d "+
			"(pre-existing session; its history was already delivered by an earlier version)",
			sessionID, attemptedCount))
		writeCursor(confirmedPath, attemptedCount)
	}
	return attemptedCount
}

// reconcileSession compara o archive com o que foi confirmado e reenvia a
// diferença. Devolve quantas linhas foram recuperadas.
//
// Junior Tip [só roda com a fila VAZIA]: um chunk na fila já está sendo retentado
// pelo dreno. Reconciliar em cima disso enviaria a mesma coisa duas vezes — e a
// janela de idempotência do servidor é de 30s, curta demais para pegar um reenvio
// que chega minutos depois (ver a Junior Tip de reclaimStaleSendingItems). Fila
// vazia significa "não há nada em voo", que é a única condição em que um buraco
// entre o archive e o confirmado é mesmo um buraco.
func reconcileSession(ctx context.Context, cfg config, mem *client.Memory, sessionID string,
	backlog queueBacklog) int {
	if sessionID == "" {
		return 0 // sem sessão não há o que reconciliar, e adivinhar é proibido
	}
	if backlogNeedsAttention(backlog) {
		return 0 // há coisa em voo: a fila resolve, reconciliar duplicaria
	}

	archivePath := filepath.Join(cfg.archiveDir, sanitize(sessionID)+".jsonl")
	archivedLines, readErr := readLines(archivePath)
	if readErr != nil {
		// Sem archive não há do que reconciliar. Silêncio aqui é correto: a saúde do
		// archive já é reportada no bloco <anhur-memory> por inspectArchiveHealth.
		return 0
	}

	confirmedCount := resolveConfirmedCount(cfg, sessionID)
	if len(archivedLines) <= confirmedCount {
		return 0 // nada perdido
	}

	missingText := extractConversation(cfg, archivedLines[confirmedCount:])
	if len(missingText) == 0 {
		// O delta existia mas era só ruído de ferramenta, que o persist descarta de
		// propósito. Confirmar impede que ele seja reexaminado a cada sessão.
		markDeliveryConfirmed(cfg, sessionID, len(archivedLines))
		return 0
	}

	recoveredLineCount := len(archivedLines) - confirmedCount
	logLine(cfg, fmt.Sprintf("reconcile: %d line(s) are in the archive but were never confirmed by AnhurDB — recovering",
		recoveredLineCount))

	if deliverSessionText(ctx, cfg, mem, sessionID, missingText) {
		markDeliveryConfirmed(cfg, sessionID, len(archivedLines))
		logLine(cfg, fmt.Sprintf("reconcile: recovered %d line(s)", recoveredLineCount))
		return recoveredLineCount
	}
	// Falhou: os chunks estão na fila e o cursor de confirmação fica onde está.
	// A próxima sessão tenta de novo.
	return 0
}

// readLines é reusada do persist para ler o archive com o mesmo parser, de modo
// que a reconciliação enxergue exatamente as linhas que o persist enxergaria.
// (Declarada em core.go; referida aqui só para deixar a dependência explícita.)
var _ = readLines

// archiveExistsForSession diz se há cópia local desta sessão. Usada pelos testes
// e por quem precisa distinguir "nada a recuperar" de "não há archive".
func archiveExistsForSession(cfg config, sessionID string) bool {
	_, statErr := os.Stat(filepath.Join(cfg.archiveDir, sanitize(sessionID)+".jsonl"))
	return statErr == nil
}
