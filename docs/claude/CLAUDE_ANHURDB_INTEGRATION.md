# Como dar memória permanente ao Claude Code com AnhurDB

> Guia passo a passo para o Claude Code lembrar de tudo entre sessões —
> automaticamente, sem você pedir e sem depender de ele "lembrar de gravar".

---

## O que vai acontecer depois de configurar?

- No início de cada sessão, o Claude recebe sua memória do AnhurDB (decisões, fatos, preferências).
- No fim de cada turno, a conversa é gravada no AnhurDB.
- Se o AnhurDB estiver fora do ar, o turno vai para uma fila em disco e entra depois. Nada se perde.
- Você pergunta "o que decidimos semana passada?" e ele sabe.

Nada disso depende de o Claude cooperar: quem grava e quem lê são **hooks** do Claude Code chamando
um binário. O modelo não pode esquecer, porque não é ele quem faz.

---

## O que você precisa

| Item | Como conseguir |
|---|---|
| Uma API key de tenant (`anhur_…`) | [ControlPlane](https://anhur.yoven.ai/app) — **não** use a master key |
| Claude Code | [claude.ai/code](https://claude.ai/code) |
| macOS ou Linux (arm64/amd64) | Windows via WSL |

---

## Passo 1: Instalar

No Claude Code:

```
/plugin marketplace add Yoven/AnhurDB-SDK
/plugin install anhurdb-memory@anhur
```

Não precisa de Go nem compilar nada — o plugin já traz o binário pronto para cada plataforma.

> **Prefere sem plugin?** São um binário e três hooks, e essa é a opção com menos peças móveis.
> Veja [v2/plugins/claude/README.md](../../v2/plugins/claude/README.md) → *Install → Option A*.

---

## Passo 2: Configurar a chave

A chave mora **num único arquivo fora de qualquer repositório**, modo `0600`. Nunca dentro de um
script, nunca no `CLAUDE.md`, nunca commitada:

```bash
install -m 700 -d "$HOME/.anhur-claude-memory"
umask 177
cat > "$HOME/.anhur-claude-memory/env" <<'EOF'
export ANHUR_API_KEY="anhur_…sua_chave_de_tenant…"
export ANHUR_URL="https://anhurdb.yoven.ai"
export ANHUR_CONTAINER="claude-ltm"
EOF
```

`ANHUR_CONTAINER` é o nome do seu perfil de memória. **Escolha uma vez e não mude:** trocar faz o
recall parar de trazer o que foi salvo com o nome antigo (nada é perdido — só deixa de ser trazido).

---

## Passo 3: Abrir uma sessão nova

Os hooks são registrados no **início da sessão**, então a configuração vale a partir da *próxima* —
não na que você está agora. Abra uma sessão nova e pronto: a memória chega sozinha.

---

## Como ter certeza de que funcionou

Pergunte ao próprio Claude algo que só a memória responde:

```
Sem usar ferramentas: você recebeu um bloco <anhur-memory>? Se sim, cite a primeira Decisão dele.
```

Se ele citar sua memória de volta, o ciclo está fechado: AnhurDB → hook → contexto → modelo.

> **Por que não olhar o log?** Porque o log prova que *alguém* rodou o binário — não que um hook
> rodou, e muito menos que o bloco chegou ao modelo. Perguntar ao modelo é a única verificação que
> prova o caminho inteiro. Diagnóstico (nunca a chave) fica em `~/.anhur-claude-memory/plugin.log`.

---

## Opcional: as ferramentas MCP

O plugin também registra as tools `mcp__anhurdb__*`, para busca explícita durante a conversa
("procure no AnhurDB o que decidimos sobre X"). Elas são **independentes** do ciclo de memória — se
você só quer memória automática, pode ignorá-las.

Sem plugin, aponte um `.mcp.json` no seu projeto:

```json
{ "mcpServers": { "anhurdb": { "type": "http", "url": "https://anhurdb.yoven.ai/mcp" } } }
```

---

## Perguntas frequentes

### Preciso mandar o Claude gravar?

Não — e não deve. A gravação é feita pelo hook `Stop`, a cada turno, sem o modelo participar.
Pedir para o Claude gravar à mão (via `create_memory`/`ingest_memory`) **duplica e fragmenta** a
memória: o hook já gravou aquele turno, e a gravação manual cria um registro concorrente, muitas
vezes numa sessão diferente. Deixe o hook trabalhar.

### E se o AnhurDB estiver fora do ar?

O Claude continua normal. O turno vai para uma fila em disco e é reenviado no próximo `persist` ou
no início da próxima sessão — o que vier primeiro. Nada é descartado.

### Funciona em todos os meus projetos?

Sim, se você instalar no escopo `user` — é o certo para memória de longo prazo, que não deveria
ficar presa a um repositório.

### Posso ver ou apagar o que foi gravado?

Sim: use as tools MCP, os SDKs (Python/TypeScript/Go), ou o Viewer do AnhurDB.

### Outras IAs podem usar o AnhurDB?

Sim. Qualquer ferramenta que fale MCP se conecta, e há SDKs em Python, TypeScript e Go.

---

## Resumo

```
~/.anhur-claude-memory/env      ← sua chave (0600, fora de qualquer repo)
   + o plugin instalado         ← ou três hooks apontando para o binário
```

Um arquivo e um comando. Sem script de enforcement, sem chave em código, sem depender da boa vontade
do modelo.

---

*AnhurDB-SDK — Guia de integração com Claude Code*
