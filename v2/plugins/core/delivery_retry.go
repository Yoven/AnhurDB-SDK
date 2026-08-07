package core

// delivery_retry.go — gravar local é ÚLTIMO RECURSO, não primeiro reflexo.
//
// Junior Tip [a regra do dono, 2026-07-31]: "só pode gravar local caso o banco
// não responda depois de 3 tentativas; assim que voltar, descarregar tudo que tem
// local e limpar o local pra não ter risco". Antes desta regra, UMA falha
// qualquer — um blip de rede, uma eleição de líder de 200 ms — já criava estado
// local. Estado local é uma segunda cópia da verdade: enquanto ele existe, a
// memória do usuário está em dois lugares e só um deles é o AnhurDB. A fila tem
// de ser um túnel, não um armário.
//
// Junior Tip [por que 3 tentativas e NÃO 3 tentativas em tudo]: o dono disse "o
// banco não responde". Não responder é uma coisa; responder "não" é outra. Um 401
// (chave errada) ou um 409 (sessão cheia) são o servidor RESPONDENDO, e a
// resposta não muda na segunda tentativa — repetir só gastaria o tempo de um hook
// para chegar ao mesmo lugar. Então: 3 tentativas é o TETO para o que pode
// melhorar sozinho (timeout, conexão recusada, 5xx, 429, 408), e o que já foi
// decidido vai direto para a fila. O chunk é enfileirado nos dois casos — a regra
// muda QUANDO se grava local, nunca SE a memória é preservada.

import (
	"context"
	"errors"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// maxDeliveryAttempts é o número do dono: três tentativas antes de aceitar o disco.
const maxDeliveryAttempts = 3

// deliveryRetryBackoff é a espera ANTES de cada tentativa (a primeira é imediata).
//
// Junior Tip [por que tão curto]: isto roda dentro de um hook que segura o fim do
// turno do usuário. O alvo é o blip — eleição de líder, reconexão, um 503 de
// deploy —, não uma queda longa. Queda longa é justamente o que a fila resolve, e
// a fila não tem pressa; o hook tem.
var deliveryRetryBackoff = []time.Duration{0, 300 * time.Millisecond, 1200 * time.Millisecond}

// deliveryOutcome descreve como uma entrega terminou. Existe para o chamador poder
// LOGAR a diferença entre "o banco não respondeu 3 vezes" e "o banco recusou" —
// eram indistinguíveis no log antigo, e foi por isso que uma rejeição permanente
// (HTTP 409) passou 10 dias parecendo uma queda temporária.
type deliveryOutcome struct {
	err          error
	attemptsMade int
	terminal     bool // o servidor respondeu e a resposta não vai mudar sozinha
}

// deliverWithRetry executa attemptDelivery até maxDeliveryAttempts vezes, parando
// cedo quando o erro é terminal ou o contexto foi cancelado.
func deliverWithRetry(ctx context.Context, attemptDelivery func() error) deliveryOutcome {
	var lastErr error
	for attemptIndex := 0; attemptIndex < maxDeliveryAttempts; attemptIndex++ {
		if waitBefore := deliveryRetryBackoff[attemptIndex]; waitBefore > 0 {
			select {
			case <-ctx.Done():
				// Junior Tip [cancelamento não é sucesso]: devolver nil aqui faria o
				// chamador acreditar que gravou. O chunk tem de ir para a fila.
				return deliveryOutcome{err: ctx.Err(), attemptsMade: attemptIndex, terminal: false}
			case <-time.After(waitBefore):
			}
		}
		lastErr = attemptDelivery()
		if lastErr == nil {
			return deliveryOutcome{attemptsMade: attemptIndex + 1}
		}
		if isTerminalDeliveryError(lastErr) {
			return deliveryOutcome{err: lastErr, attemptsMade: attemptIndex + 1, terminal: true}
		}
	}
	return deliveryOutcome{err: lastErr, attemptsMade: maxDeliveryAttempts}
}

// terminalityLabel dá ao log a palavra que faltava.
//
// Junior Tip [a distinção que custou 10 dias]: no log antigo, "flush still
// failing" cobria tanto o banco fora do ar quanto uma rejeição permanente. São
// situações opostas — uma se resolve sozinha, a outra nunca — e quem lia o log não
// tinha como separá-las. Um HTTP 409 ficou 10 dias parecendo uma queda temporária.
func terminalityLabel(terminal bool) string {
	if terminal {
		return "server refused; retrying would not help"
	}
	return "no usable response; will retry from the queue"
}

// isTerminalDeliveryError responde: o servidor já decidiu, ou ainda pode mudar?
//
// Junior Tip [classificar por TIPO, não por texto]: o SDK devolve erros tipados
// (ErrUnauthorized, ErrNotFound, *APIError com StatusCode). Casar por substring na
// mensagem — "401", "unauthorized" — quebraria calado no dia em que alguém
// reescrevesse uma mensagem, e o custo do engano é assimétrico: classificar um
// transiente como terminal desiste cedo demais e cria estado local sem
// necessidade. Na dúvida, o padrão é TENTAR DE NOVO.
func isTerminalDeliveryError(deliveryErr error) bool {
	if deliveryErr == nil {
		return false
	}
	// Chave errada, sem permissão, rota inexistente: repetir dá o mesmo resultado.
	if errors.Is(deliveryErr, client.ErrUnauthorized) || errors.Is(deliveryErr, client.ErrNotFound) {
		return true
	}
	var apiErr *client.APIError
	if errors.As(deliveryErr, &apiErr) {
		switch apiErr.StatusCode {
		case 408, 429:
			// Tempo esgotado e excesso de requisições melhoram sozinhos: transientes.
			return false
		}
		// 4xx = o servidor entendeu e recusou (400 malformado, 409 sessão cheia,
		// 422 inválido). 5xx = o servidor tropeçou e pode voltar: transiente.
		return apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
	}
	// Rede, DNS, timeout, EOF, contexto: tudo isso é "não respondeu".
	return false
}
