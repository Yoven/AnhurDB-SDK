package core

// envfile.go — o plugin lê a PRÓPRIA configuração, sem depender de semântica de shell.
//
// Junior Tip [o blackout de 2026-07-18 a 2026-07-30 — por que este arquivo existe]:
// os hooks faziam `. $HOME/.anhur-<plugin>-memory/env 2>/dev/null` e o binário lia
// só `os.Getenv`. Em POSIX sh, `.` sem `export` cria variável de SHELL, que o
// processo filho NÃO herda. Em 2026-07-18T18:34:03Z o arquivo env foi reescrito a
// partir do .env.example (que também não tem `export`) e perdeu as três linhas
// `export` que os backups anteriores ainda têm. Dezesseis minutos depois veio o
// primeiro `ANHUR_API_KEY not set — skipping`, e a partir dali foram 743
// invocações de hook com 743 skips: 100% de perda por 12,8 dias, com `exit 0`,
// stdout vazio e stderr vazio. Nem o modelo nem o usuário tinham como saber.
//
// A correção NÃO é `export` no arquivo do dono (conserta uma máquina) nem
// `set -a` no hooks.json (continua refém do shell e engole erro de sintaxe com o
// `2>/dev/null`). É esta: o binário lê o arquivo, então funciona igual sob
// qualquer executor de hook, em qualquer shell, e — o que mais importa — o
// carregamento vira código testável em Go. Antes disto não havia UM teste para o
// carregamento de configuração do produto que é vendido como memória soberana.
//
// Junior Tip [precedência: ambiente VENCE arquivo]: uma variável já presente no
// ambiente do processo nunca é sobrescrita. Isso preserva override por invocação
// (CI, teste, `ANHUR_URL=... plugin recall`) e mantém compatível quem já exporta
// no shell. O efeito colateral perverso dessa mesma precedência foi o que
// mascarou a falha por 12 dias: nas poucas vezes em que a chave estava exportada
// no terminal, o persist funcionava — 187 sucessos intercalados que faziam o
// sistema parecer vivo. Por isso o chamador DEVE logar a FONTE da chave.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// envFileName é o nome do arquivo de configuração dentro do stateDir.
// Mesmo nome que os hooks historicamente faziam source — quem já tem o arquivo
// não precisa mover nada.
const envFileName = "env"

// KeySourceEnvironment e KeySourceFile identificam de onde a chave veio, para o
// log. Nunca acompanham o VALOR da chave — só a origem.
const (
	KeySourceEnvironment = "environment"
	KeySourceFile        = "file"
	KeySourceMissing     = "missing"
)

// loadEnvFileInto lê KEY=VALUE do arquivo e injeta no ambiente do processo
// APENAS as chaves ainda ausentes. Devolve quantas variáveis foram injetadas.
//
// Formato aceito, deliberadamente tolerante porque o arquivo é escrito à mão e
// por scripts de rotação:
//   - `KEY=value` e `export KEY=value` (o prefixo export é ignorado)
//   - aspas simples ou duplas ao redor do valor são removidas
//   - linhas vazias e comentários com `#` são ignorados
//   - espaço ao redor da chave e do sinal de igual é tolerado
//
// Junior Tip [por que não falhar em linha malformada]: o arquivo pode conter
// coisas que não são pares (uma linha de comentário sem `#`, um resto de merge).
// Abortar a leitura por causa de uma linha ruim descartaria a chave que está na
// linha seguinte — trocaria um problema por outro pior. Linha sem `=` é pulada;
// o chamador decide o que fazer se a chave essencial não apareceu.
func loadEnvFileInto(envFilePath string) (injectedCount int, readErr error) {
	openedFile, openErr := os.Open(envFilePath)
	if openErr != nil {
		return 0, openErr
	}
	defer openedFile.Close()

	lineScanner := bufio.NewScanner(openedFile)
	for lineScanner.Scan() {
		trimmedLine := strings.TrimSpace(lineScanner.Text())
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}
		trimmedLine = strings.TrimPrefix(trimmedLine, "export ")

		separatorIndex := strings.Index(trimmedLine, "=")
		if separatorIndex <= 0 {
			continue
		}
		variableName := strings.TrimSpace(trimmedLine[:separatorIndex])
		variableValue := strings.TrimSpace(trimmedLine[separatorIndex+1:])
		variableValue = trimSurroundingQuotes(variableValue)
		if variableName == "" {
			continue
		}
		// Ambiente vence: nunca sobrescreve o que já veio de fora.
		//
		// Junior Tip [DEFINIDA-E-VAZIA não é "veio de fora", 2026-07-31]: antes
		// bastava LookupEnv dizer "existe" para o valor do arquivo ser ignorado — e
		// uma variável definida como string vazia EXISTE. O efeito: um
		// `export ANHUR_API_KEY=` esquecido num shell rc, ou um `env:` vazio num
		// manifesto, DESLIGAVA a memória enquanto uma chave perfeitamente válida
		// estava no arquivo ao lado, e o log dizia "key source=missing" sem apontar
		// para o culpado. É a forma exata do apagão de 12,8 dias de julho de 2026:
		// não uma falha, um silêncio. Pior, o próprio Go já discordava de si mesmo —
		// envOr() (ANHUR_URL, ANHUR_CONTAINER, ANHUR_ARCHIVE_DIR) sempre tratou vazio
		// como ausente, então só a chave se comportava assim. Achado pelo harness de
		// paridade contra o Python, que já fazia certo (config.py _first_non_empty).
		if existingValue, alreadySet := os.LookupEnv(variableName); alreadySet &&
			strings.TrimSpace(existingValue) != "" {
			continue
		}
		if setErr := os.Setenv(variableName, variableValue); setErr == nil {
			injectedCount++
		}
	}
	return injectedCount, lineScanner.Err()
}

// trimSurroundingQuotes remove um par simétrico de aspas ao redor do valor.
func trimSurroundingQuotes(rawValue string) string {
	if len(rawValue) < 2 {
		return rawValue
	}
	firstByte, lastByte := rawValue[0], rawValue[len(rawValue)-1]
	if (firstByte == '"' && lastByte == '"') || (firstByte == '\'' && lastByte == '\'') {
		return rawValue[1 : len(rawValue)-1]
	}
	return rawValue
}

// resolveEnvFilePath devolve o arquivo de config a ler: ANHUR_ENV_FILE quando
// definido, senão <stateDir>/env.
func resolveEnvFilePath(stateDir string) string {
	if explicitPath := strings.TrimSpace(os.Getenv("ANHUR_ENV_FILE")); explicitPath != "" {
		return explicitPath
	}
	return filepath.Join(stateDir, envFileName)
}

// apiKeySource classifica de onde a chave veio, para o log — nunca o valor.
func apiKeySource(keyWasInEnvironmentBefore bool, apiKeyNow string) string {
	switch {
	case apiKeyNow == "":
		return KeySourceMissing
	case keyWasInEnvironmentBefore:
		return KeySourceEnvironment
	default:
		return KeySourceFile
	}
}
