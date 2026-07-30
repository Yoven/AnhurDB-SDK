# Plataformas de plugin — Claude Code vs Hermes Agent

> **Documento canônico.** Escrito em 2026-07-30, no dia em que se descobriu o blackout da
> memória do Claude — 744 hooks executados sem gravar nada, 12,88 dias entre um write
> bem-sucedido e o seguinte (medição na seção 8). Toda afirmação aqui foi verificada contra código real: o código dos dois
> plugins (`v2/plugins/`), o código do Hermes Agent instalado
> (`~/.hermes/hermes-agent`, `hermes_agent 0.18.0`, editable install) e o estado em disco
> das instalações. O que **não** foi verificado está marcado como tal.
>
> **Para que serve:** decidir o que pode ser compartilhado entre os dois plugins e o que
> **não pode**, e impedir que o erro descrito na seção 3 se repita.

---

## 0. Conclusão executiva

1. **São duas plataformas incompatíveis em formato, em linguagem e em modelo de execução.**
   Nenhuma linha de código executável é compartilhável entre elas. O Claude Code executa um
   **processo externo** por hook; o Hermes Agent **importa Python dentro do processo do
   agente**.
2. **O que se compartilha é política, não código:** a identidade de memória
   (tenant + container), o invariante *1 sessão do agente = 1 sessão AnhurDB*, e a política
   de não-perda (fila, quarentena, arquivo lossless, falhar ALTO).
3. **Estado verificado hoje:** `v2/plugins/hermes/` **é um plugin do Claude Code**
   (`.claude-plugin/plugin.json` + `hooks/hooks.json` + binário Go), apenas apontado para
   outro container. O Hermes Agent **não tem como carregá-lo** — ele procura `plugin.yaml` e
   `__init__.py` Python, e aquele diretório não tem nenhum dos dois. Ver seção 3.
4. **O plugin no formato certo passou a existir hoje, em outro diretório:**
   `v2/plugins/hermes-agent/` (`plugin.yaml` com `name: anhurdb`, `kind: exclusive`,
   `__init__.py` com `register(ctx)`, `provider.py` com `AnhurDBMemoryProvider`). Ele é
   **novo, não commitado e não instalado** — pelo critério da seção 3 ainda não conta como
   plugin vivo. `v2/plugins/hermes/` está **superado** e não deve ser tratado como o plugin
   do Hermes.
5. **Nenhum binário publicado contém as correções de 2026-07-30.** Fonte corrigido em
   `80fb003`; `bin/` de ambos os plugins Go e o cache instalado do Claude Code ainda rodam o
   código pré-correção. Ver seção 6.1, item 3.

---

## 1. Tabela comparativa

| Dimensão | **Claude Code** | **Hermes Agent** |
|---|---|---|
| Manifesto | `.claude-plugin/plugin.json` (JSON) | `plugin.yaml` na **raiz** do plugin (YAML) |
| Campos do manifesto | `name`, `description`, `version`, `author`, `homepage`, `keywords` | `name`, `version`, `description`; opcionais `author`, `requires_env`, `optional_env`, `kind`, `provides_tools`, `provides_hooks`. **Verificado no código real:** também `pip_dependencies` (ex.: `plugins/memory/retaindb/plugin.yaml`) e `manifest_version` |
| Catálogo | **Marketplace**: `<repo>/.claude-plugin/marketplace.json` lista `{name, source}` de cada plugin | **Não existe marketplace.** Instalação é `git clone` direto |
| Descoberta | Só o que está instalado: `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/` | 4 origens: bundled `<repo>/plugins/**`; user `~/.hermes/plugins/<nome>/`; project `./.hermes/plugins/<nome>/`; entry point pip `hermes_agent.plugins` |
| Descoberta de **memory provider** | n/a (não existe a categoria) | bundled `plugins/memory/<nome>/` **ou** user `~/.hermes/plugins/<nome>/` — **plano, não `plugins/memory/`** (`plugins/memory/__init__.py:_get_user_plugins_dir`) |
| Instalação | `/plugin marketplace add <src>` + `/plugin install <plugin>@<marketplace>` | `hermes plugins install <owner/repo[/subdir]>` |
| Modelo de instalação | **Snapshot**: o diretório é *copiado* para o cache na hora do install | **Worktree git vivo** em `~/.hermes/plugins/<nome>/` |
| Atualização | Bump de `version` no manifesto invalida o cache → `/plugin update` / reinstalar | `hermes plugins update <nome>` = `git pull` no diretório |
| Linguagem / runtime | Qualquer executável — hooks são **comandos de shell**. Aqui: binário Go estático | **Python**, importado no processo do agente (`importlib.util.spec_from_file_location`) |
| Ponto de entrada | `hooks/hooks.json` aponta comandos; `.mcp.json` registra MCP | `__init__.py` **deve** exportar `register(ctx)` (fallback: uma subclasse de `MemoryProvider` no módulo) |
| Pontos de extensão | hooks (comando), servidores MCP, skills, commands, agents | `ctx.register_tool / register_hook / register_command / register_cli_command / register_memory_provider / register_skill`; `ctx.llm.complete[_structured]`; `ctx.inject_message` |
| Eventos de ciclo de vida | Os que **este** plugin usa (verificados em `claude/hooks/hooks.json`): `SessionStart` com matcher `startup\|resume\|clear\|compact`, `Stop`, `SessionEnd`. A plataforma oferece outros (pré/pós tool etc.) — não auditados aqui | `VALID_HOOKS` (~25, `hermes_cli/plugins.py:135`) — inclui `pre/post_llm_call`, `pre/post_tool_call`, `on_session_start/end`, e ainda `on_session_finalize`, `on_session_reset`, `subagent_start/stop`, `pre_verify`, `pre_gateway_dispatch` |
| **Como a memória é ligada** | **3 hooks de shell.** `SessionStart → recall` (o **stdout** do processo vira contexto); `Stop` e `SessionEnd → persist` (payload JSON no **stdin**) | **Um memory provider ativo**, escolhido por `memory.provider` em `~/.hermes/config.yaml`. O agente chama métodos do objeto **in-process** |
| Ativação da memória | Basta o plugin carregar: os hooks passam a existir | Duas coisas: o diretório existir **e** `memory.provider: <nome>` apontar para ele. Só **um** provider ativo por vez |
| Entrada de dados da conversa | Transcript **JSONL em disco** (`transcript_path`) + cursor por sessão | Objetos em memória: `user_content`, `assistant_content`, `messages` (lista estilo OpenAI) |
| Saída para o modelo | `stdout` do hook `SessionStart` | Retorno de `prefetch(query, session_id=...)` + `system_prompt_block()` |
| Isolamento de falha | Processo separado: pânico/erro do plugin não derruba a sessão | **In-process**: exceção do provider é capturada pelo `MemoryManager`, mas o custo/latência é do turno do agente |
| Onde vive o segredo | `~/.anhur-<plugin>-memory/env` (0600), **lido pelo próprio binário** desde `80fb003` | `~/.hermes/.env` / ambiente do processo; `hermes memory setup` grava secrets no `.env` a partir de `get_config_schema()` |
| Falha silenciosa característica | Plugin não carregado / hook não registrado → **nada roda**, sem erro, sem log | `is_available()` retorna `False` (ou diretório não encontrado) → `logger.debug("Memory provider '%s' not found or not available")` e a sessão segue **sem memória** (`agent/agent_init.py:1317-1320`) |

### 1.1 Equivalência de eventos (o mapa que não é 1:1)

| Intenção | Claude Code | Hermes Agent |
|---|---|---|
| Injetar memória no início | hook `SessionStart` → stdout | `initialize(session_id, **kwargs)` + `system_prompt_block()` + `prefetch()` |
| Injetar memória **a cada turno** | **não existe** (só no início) | `prefetch(query)` roda **antes de cada chamada de LLM** |
| Persistir o turno | hook `Stop` → lê o delta do transcript | `sync_turn(user_content, assistant_content, *, session_id, messages)` |
| Fim de sessão | hook `SessionEnd` (não dispara em `kill -9`) | `on_session_end(messages)` + `shutdown()` |
| **Troca de session_id no meio do processo** | **não existe** — `/clear` e `/compact` abrem um novo `SessionStart` | **`on_session_switch(new_session_id, parent_session_id=, reset=, rewound=)`** — dispara em `/resume`, `/branch`, `/reset`, `/new` e na **compressão de contexto** |
| Saber que o contexto é de cron/subagente | não existe | `initialize(**kwargs)` recebe `agent_context` ∈ {`primary`, `subagent`, `cron`, `flush`} |
| Ferramentas para o modelo | servidor MCP (`.mcp.json`) | `get_tool_schemas()` + `handle_tool_call()` |

> **Duas linhas dessa tabela decidem a corretude e não têm análogo no lado Claude:**
> `on_session_switch` e `agent_context`. Um provider que os ignore **mistura sessões**
> (viola o invariante inviolável) e **grava prompt de cron como se fosse conversa do
> usuário**. Nenhum código copiado do plugin do Claude tem como cobrir isso, porque no
> Claude Code esses eventos não existem.

---

## 2. Contratos reais (verificados, não lembrados)

### 2.1 Claude Code — o que o `core` Go realmente faz

`v2/plugins/core/core.go` **não é um motor de memória neutro**. Ele é um motor de hook do
Claude Code:

| Acoplamento | Onde | Consequência |
|---|---|---|
| `hookInput{session_id, transcript_path, cwd}` | `core.go:350-354` | é o payload de stdin **do Claude Code** |
| Fallback `~/.claude/projects/<cwd-mungido>/<session>.jsonl` | `core.go:518-523` | caminho **do Claude Code** |
| Formato de linha do transcript (`type: user\|assistant`, blocos `text`/`tool_use`/`tool_result`/`thinking`) | `core.go:531-603` | formato **do Claude Code** |
| Cabeçalho do chunk `"Claude Code session <id> — conversation excerpt…"` | `core.go:420-435` | **a fila inteira depende dessa string**: `sessionFromChunk` (`core.go:939-950`) faz o parse dela para devolver o chunk à sessão certa; sem ela o chunk vai para quarentena |
| Cursor por sessão em arquivo | `core.go:391-400` | pressupõe transcript em disco crescendo por append |

Ou seja: mesmo que o Hermes rodasse um binário Go, **este** binário não serviria — ele lê um
arquivo que o Hermes não escreve, num formato que o Hermes não usa.

O que é genuinamente reaproveitável é **política**, e está nas seguintes funções (leia-as
antes de escrever o equivalente Python): `Run` (guarda de chave + arquivo antes de desistir,
`core.go:187-232`), `flushQueue` + `quarantineChunk` (`core.go:780-902`),
`archiveTranscript` (`core.go:470-510`), `formatMemory` (backlog/quarentena **acima** da
memória, `core.go:276-344`), e `loadEnvFileInto` (`envfile.go:65-99`).

### 2.2 Hermes Agent — o contrato do memory provider

Fonte: `~/.hermes/hermes-agent/agent/memory_provider.py`,
`plugins/memory/__init__.py`, `agent/memory_manager.py`, `agent/agent_init.py:1256-1322`.

**Obrigatórios (`@abstractmethod`)** — sem os quatro a classe nem instancia:

```python
@property
def name(self) -> str: ...
def is_available(self) -> bool: ...
def initialize(self, session_id: str, **kwargs) -> None: ...
def get_tool_schemas(self) -> list[dict]: ...
```

**Opcionais que importam para nós** (default no-op — o silêncio é o default):
`system_prompt_block()`, `prefetch(query, *, session_id)`, `queue_prefetch(...)`,
`sync_turn(user_content, assistant_content, *, session_id, messages)`,
`handle_tool_call(tool_name, args, **kwargs) -> str`, `shutdown()`,
`on_turn_start(turn_number, message, **kwargs)`, `on_session_end(messages)`,
**`on_session_switch(new_session_id, *, parent_session_id, reset, rewound, **kwargs)`**,
`on_pre_compress(messages) -> str`, `on_delegation(task, result, *, child_session_id)`,
`on_memory_write(action, target, content, metadata)`, `get_config_schema()`,
`save_config(values, hermes_home)`, `backup_paths()`.

**`initialize(**kwargs)` sempre traz** `hermes_home` e `platform`; e conforme o caso
`agent_context`, `agent_identity`, `agent_workspace`, `parent_session_id`, `user_id`,
`user_id_alt`, `user_name`, `chat_id`, `chat_name`, `chat_type`, `thread_id`,
`gateway_session_key`, `session_title`. No CLI vêm também `warning_callback` e
`status_callback` (`agent_init.py:1278-1280`) — **os únicos canais do provider que chegam ao
olho do usuário**.

#### Três armadilhas verificadas no código (nenhuma está no resumo do contrato)

1. **`ctx.register_hook` e `ctx.register_tool` são NO-OP para um memory provider.**
   Quando o provider é carregado, o loader passa um contexto falso
   (`plugins/memory/__init__.py:319-336`, classe `_ProviderCollector`) que só implementa
   `register_memory_provider`; `register_tool`, `register_hook` e `register_cli_command`
   são `pass`. Além disso o `PluginManager` **pula** plugins `kind == "exclusive"`
   (`hermes_cli/plugins.py:1353-1364`) — e um plugin com `register_memory_provider` no
   `__init__.py` é auto-coagido para `exclusive`
   (`hermes_cli/plugins.py:1548-1568`). **Portanto: um plugin de memória não ganha hooks.**
   Tudo que ele precisa fazer no ciclo de vida tem de sair dos métodos do `MemoryProvider`.
   Traduzir "hook SessionStart" para `ctx.register_hook("on_session_start", ...)` produz
   código que **nunca executa e não reclama**.
2. **`is_available() == False` desliga a memória em nível DEBUG.**
   `agent_init.py:1269-1320`: se `is_available()` for falso, o provider nem é adicionado, o
   log é `logger.debug("Memory provider '%s' not found or not available")` e a sessão segue.
   Pior: `warning_callback`/`status_callback` só são entregues em `initialize()`, que nunca
   é chamado nesse caminho. **Logo, `is_available()` que devolve `False` por falta de chave
   precisa gritar sozinho** (stderr) antes de retornar — senão é o blackout de novo, com
   outro nome.
3. **O diretório do usuário é plano, e é o NOME DO DIRETÓRIO que manda.** Provider instalado
   pelo usuário vive em `~/.hermes/plugins/<dir>/`, **não** em
   `~/.hermes/plugins/memory/<dir>/` (`plugins/memory/__init__.py:_get_user_plugins_dir` +
   `_iter_provider_dirs`); `plugins/memory/<nome>/` é o caminho dos providers **bundled**
   dentro do repo do Hermes. E a chave de identidade é o **nome do diretório**:
   `_iter_provider_dirs` devolve `(child.name, child)` e `find_provider_dir(name)` resolve
   `user_dir / name` — portanto `memory.provider` tem de casar com o **diretório**, não com
   o campo `name` do manifesto. Os dois só coincidem porque `hermes plugins install` nomeia
   o diretório a partir de `manifest["name"]` (`hermes_cli/plugins_cmd.py:496-502`); numa
   cópia manual, quem decide é o `mkdir`.

**Contrato de handler de tool** (vale para `handle_tool_call`): `-> str`, **sempre** JSON
string, sucesso e erro; **nunca** levanta exceção; usa `.get()`, nunca indexação direta.

---

## 3. O erro histórico — para não repetir

**O que aconteceu:** `v2/plugins/hermes/` foi escrito como **cópia do plugin do Claude
Code**. Ele tem `.claude-plugin/plugin.json`, `hooks/hooks.json`, `cmd/…/main.go` e binários
Go em `bin/`. O nome diz *hermes*; o formato diz *Claude Code*.

**Por que isso nunca poderia funcionar:** o Hermes Agent procura `plugin.yaml` e um
`__init__.py` Python (seção 2.2). Ele não lê `.claude-plugin/`, não interpreta `hooks.json`,
não executa binários como pontos de extensão. Um plugin nesse formato é **invisível** para
ele — não dá erro, simplesmente não existe.

**Evidência do abandono, medida hoje:**

| Fato | Evidência |
|---|---|
| Nunca foi instalado no Claude Code | `~/.claude/plugins/installed_plugins.json` lista **só** `anhurdb-memory@anhur` (0.1.3). `~/.claude/plugins/cache/anhur/` contém só `anhurdb-memory/` |
| Nunca foi instalado no Hermes | `~/.hermes/plugins/` contém só `hermes-achievements/` (que nem é um plugin — são 3 arquivos de estado, sem `__init__.py`) |
| Nunca foi selecionado como provider | `~/.hermes/config.yaml` **não tem** a chave `memory.provider` |
| **Nunca persistiu um único registro** | `~/.anhur-hermes-memory/plugin.log`: **59 linhas**, última em **2026-07-17T12:30:10Z**, **zero** linhas `persist:`. Composição: 39 `recall: profile failed`, 12 `recall: injected memory block`, 4 `ANHUR_API_KEY not set — skipping`, 2 `usage:`, 2 `recall: wrote memory block` |
| As 59 linhas vieram do **Claude Code**, não do Hermes | O log só registra `recall`/`usage` — as ações dos hooks do Claude Code. O Hermes nunca executou esse binário |
| Houve uma tentativa anterior, também morta | `~/.hermes/anhur-memory-wrapper.sh` e `~/.hermes/anhur-hermes-persist.sh` (2026-06-22): shell scripts que apontam para binários **dentro do worktree git**, usam um state dir diferente (`~/.anhur-hermes`, não `~/.anhur-hermes-memory`), dependem de `$HERMES_SESSION_MESSAGES`/`$HERMES_SESSION_ID` (que **não existem** no contrato) e terminam todas as chamadas com `2>/dev/null \|\| true`. `~/.anhur-hermes/plugin.log` tem **1 linha**, de 2026-06-21 |

**Diagnóstico:** não foi bug de configuração. Foi **formato errado**. Nenhuma quantidade de
`hermes memory setup`, de chave, de container ou de rede faria aquele diretório virar um
plugin do Hermes.

**A regra que sai daqui:** *plugin novo só existe depois que a plataforma-alvo confirma que
o carregou.* No Claude Code isso é `claude plugin list` + o bloco chegando ao modelo; no
Hermes é `hermes memory status` mostrando o provider **available** + o `prefetch` chegando ao
modelo. Enquanto isso não acontecer, o que existe é um diretório, não um plugin.

**A regra irmã, do blackout do mesmo mês:** *um componente que não consegue cumprir sua
função tem de ser VISÍVEL, e a rede de segurança nunca pode depender daquilo que ela
protege.* No blackout, o `return` por chave ausente ficava **antes** do arquivo lossless — a
salvaguarda morreu junto com o que deveria salvaguardar (corrigido em `core.go:200-230`).
No Hermes, o análogo exato é `is_available()` devolvendo `False` em silêncio (armadilha 2 da
seção 2.2).

**Estado da correção (2026-07-30):** o plugin no formato do Hermes passou a existir em
`v2/plugins/hermes-agent/` (id de provider `anhurdb`). Pelo critério desta própria seção
ele **ainda não conta**: não está commitado, não está instalado e nenhum agente confirmou
que o carregou. Ver seção 5.2 para instalar e seção 6.2 para provar.

---

## 4. O que os dois compartilham de verdade — e o que não

### 4.1 Compartilhado (política, obrigatória nos dois)

| Política | Enunciado | Onde está no Go (referência para espelhar) |
|---|---|---|
| **Identidade de memória** | `ANHUR_API_KEY` seleciona o **tenant**; `ANHUR_CONTAINER` seleciona o **perfil de memória dentro do tenant**; `ANHUR_URL` o endpoint. Container é filtro **duro** do recall e deve ser escolhido uma vez e mantido | `core.go:99-145`, `newMemory` (`WithUserID`) |
| **1 sessão = 1 sessão (INVIOLÁVEL)** | Uma sessão do agente = **uma** sessão AnhurDB, com o id **da conversa**. `CreateSession` antes do primeiro `Add`. `WithSessionID` **explícito em todo write** — nunca herdar a sessão registrada no cliente | `core.go:411-447`, `core.go:832-843` |
| **Nunca adivinhar sessão** | Chunk sem sessão provável vai para **quarentena**, nunca para "a sessão atual" nem para o container. Perder disponibilidade é aceitável; perder atribuição não é | `core.go:815-831`, `quarantineChunk` `core.go:888-902` |
| **Não-perda** | Falha de rede → fila em disco + retry em toda operação seguinte; nome de arquivo único por escrita; falha de escrita na fila **falha ALTO** | `queueChunk` `core.go:720-737`, `flushQueue` `core.go:780-875` |
| **A rede de segurança vem antes da desistência** | O arquivo verbatim é gravado **antes** de qualquer `return` por falta de credencial | `core.go:211-228` |
| **Falhar ALTO** | Nada de `exit 0` mudo. Diagnóstico no canal que o humano lê (stderr) **e** no log | `core.go:200-209` |
| **Registrar a FONTE da credencial, nunca o valor** | `environment \| file \| missing` | `envfile.go:42-48`, `apiKeySource` |
| **O backlog sobe para o modelo** | Backlog e quarentena aparecem **no topo** do bloco de memória, com ordem explícita de contar ao usuário — log não alcança ninguém | `formatMemory` `core.go:286-308` |
| **Chave fora do transcript** | Só header `X-API-Key`. Nunca em stdout, log, transcrição ou argumento de tool | `core.go:64-65`, `logLine` `core.go:952-960` |

### 4.2 **Não** compartilhável: por que o core Go não vai para o Hermes

1. **Modelo de execução.** O Hermes importa Python no processo do agente e chama métodos
   síncronos cujo **valor de retorno** entra no contexto (`prefetch() -> str`). Um binário Go
   só entraria por subprocess a cada turno: um `fork+exec` por chamada de LLM, serialização
   do `messages` para stdin, e o retorno teria de ser re-parseado. Troca-se uma chamada de
   função por um protocolo — sem ganho algum.
2. **O core Go não é neutro** (seção 2.1): ele lê o transcript JSONL do Claude Code, num
   caminho do Claude Code, e a recuperação da fila depende literalmente da string
   `"Claude Code session <id>"`. No Hermes não existe nem o arquivo nem o formato.
3. **Faltam conceitos inteiros.** `on_session_switch` (o session_id **muda no meio do
   processo**) e `agent_context` (`primary`/`subagent`/`cron`/`flush`) não têm análogo no
   Claude Code e não existem no core Go. São exatamente os dois pontos onde o invariante
   1-sessão-1-sessão e a integridade do que é "conversa do usuário" se quebram.
4. **Empacotamento.** O Claude Code aceita binário porque o plugin é copiado e o hook é um
   comando; o Hermes instala por `git clone` e importa Python, com dependências declaradas
   em `pip_dependencies`. Enviar binários por plataforma dentro de um plugin Python é
   arrastar o problema de distribuição do Go para dentro do `pip`.
5. **Já existe a peça certa.** O SDK Python oficial (`v2/python`) tem paridade obrigatória
   com o Go (regra dos 3 SDKs). O plugin do Hermes deve **dogfoodar o SDK Python**,
   exatamente como o plugin do Claude dogfooda o SDK Go. Reimplementar HTTP à mão no plugin
   seria criar um quarto cliente sem paridade.

**Conclusão:** o compartilhamento acontece **uma camada abaixo** — no SDK, que já é
compartilhado por contrato — e **uma camada acima** — na política, que este documento fixa.
No meio, os dois plugins são implementações independentes.

### 4.3 O que precisa ser espelhado à mão

Cada item abaixo é uma linha da seção 4.1 e deve existir nas **duas** implementações, com
teste **em cada linguagem**:

| # | Política | Go (existe) | Python (a escrever) |
|---|---|---|---|
| P1 | Config lida do próprio arquivo, ambiente vence arquivo, fonte registrada | `envfile.go` + `envfile_test.go` | ler `~/.anhur-hermes-memory/env` (ou o `.env` do Hermes) sem depender de shell |
| P2 | Credencial ausente → grita (stderr/`warning_callback`) e **não** some | `core.go:200-230` | `is_available()` grita antes de devolver `False` |
| P3 | Arquivo lossless antes de desistir | `archiveTranscript` chamado no caminho de falha | idem, a partir de `messages` |
| P4 | `CreateSession` + `WithSessionID` explícito em todo write | `core.go:413`, `core.go:441` | idem via SDK Python |
| P5 | Fila em disco + retry + nome único + falha de fila loga alto | `queueChunk`/`flushQueue` | idem |
| P6 | Quarentena para chunk sem sessão provável, nunca adivinhar | `quarantineChunk` | idem |
| P7 | Backlog/quarentena no topo do bloco injetado | `formatMemory` | topo do `prefetch()` / `system_prompt_block()` |
| P8 | Chave nunca no transcript/log | `logLine` | idem — e **cuidado extra**: no Hermes o provider roda no mesmo processo do agente |
| P9 | **`on_session_switch`** mantém 1-sessão-1-sessão em `/resume`, `/branch`, `/reset`, `/new` e compressão | **não existe (não se aplica)** | **obrigatório** |
| P10 | `agent_context != "primary"` não escreve memória do usuário | **não existe (não se aplica)** | **obrigatório** |

### 4.4 A regra de paridade dos 3 SDKs se aplica aqui?

**Sim, adaptada — e é importante entender a diferença.**

Os 3 SDKs (Go/Python/TS) têm paridade de **API**: os mesmos métodos, os mesmos parâmetros,
porque falam o mesmo protocolo. Um bugfix vai nos três no mesmo PR porque é literalmente o
mesmo bug traduzido.

Os 2 plugins **não** têm paridade de API — as plataformas são diferentes por natureza. Eles
têm paridade de **política**. Então a regra vira:

> **Regra de paridade de plugins.** Qualquer PR que altere uma das políticas P1–P10 num
> plugin **deve** alterá-la no outro no mesmo PR, **ou** declarar no corpo do PR por que a
> política não se aplica àquela plataforma (as únicas exceções conhecidas hoje são P9 e P10,
> que não existem no Claude Code). A tabela 4.3 é a fonte da verdade; um teste com o mesmo
> nome deve existir dos dois lados.

**Como a deriva será detectada (e não apenas prometida):**

1. A tabela 4.3 vive **neste arquivo**, não na cabeça de ninguém.
2. Os testes espelhados usam o mesmo nome nas duas linguagens (ex.:
   `TestMissingKeyArchivesBeforeGivingUp` / `test_missing_key_archives_before_giving_up`),
   de modo que a ausência de um lado é visível por `grep`.
3. **Nenhum dos dois plugins conta como entregue enquanto a plataforma-alvo não confirmar o
   carregamento** (seção 6). O erro da seção 3 sobreviveu meses porque ninguém exigiu essa
   confirmação.

---

## 5. Matriz de instalação (comandos reais)

### 5.1 Claude Code — `anhurdb-memory` (existe e está instalado)

```
# dentro do Claude Code (slash commands)
/plugin marketplace add Yoven/AnhurDB-SDK      # ou: /plugin marketplace add /caminho/para/AnhurDB-SDK
/plugin install anhurdb-memory@anhur           # instalar em escopo USER
```

```bash
# configuração — arquivo 0600 FORA de qualquer repositório
install -m 700 -d "$HOME/.anhur-claude-memory"
umask 177
cat > "$HOME/.anhur-claude-memory/env" <<'EOF'
export ANHUR_API_KEY="anhur_…chave_do_tenant…"
export ANHUR_URL="https://anhurdb.yoven.ai"
export ANHUR_CONTAINER="claude-ltm"
EOF
```

```bash
# publicar uma mudança (o bump de version é o que invalida o cache — não é burocracia)
cd v2/plugins/claude
make release-binaries
$EDITOR .claude-plugin/plugin.json     # version: X.Y.Z+1
# reinstalar pelo marketplace
```

- Marketplace registrado hoje: `anhur`, source `directory`, path
  `/home/junior/Projects/yoven/Anhur/AnhurDB-SDK` (worktree **vivo** — renomear
  `.claude-plugin/marketplace.json` derruba a memória em silêncio; foi o incidente de
  2026-07-16).
- `make deploy` **não** é caminho de release: sobrescreve só o binário no cache e mascara o
  fato de que o cache é um snapshot.

### 5.2 Hermes Agent — `anhurdb` (fonte em `v2/plugins/hermes-agent/`)

> Os comandos abaixo foram verificados **no código do CLI instalado**
> (`hermes_cli/plugins_cmd.py`, `hermes_cli/memory_setup.py`, `hermes_cli/main.py`) e **não
> foram executados** nesta máquina: nada foi instalado. O plugin existe no repositório
> (`plugin.yaml` com `name: anhurdb`), mas ainda não está commitado nem instalado.

```bash
# instalar direto do monorepo (owner/repo/subdir é aceito por _resolve_git_url)
hermes plugins install Yoven/AnhurDB-SDK/v2/plugins/hermes-agent
#   → git clone; o diretório instalado recebe o NOME do campo `name` do plugin.yaml
#   → ~/.hermes/plugins/anhurdb/   ← É ESTE NOME que vai em memory.provider

# ativar como provider de memória (só UM ativo por vez)
hermes memory setup           # walkthrough guiado por get_config_schema()
hermes memory setup anhurdb   # ativar direto
hermes memory status          # o que está ativo e se está available

# manutenção
hermes plugins list
hermes plugins update anhurdb # git pull no diretório instalado
hermes plugins remove anhurdb
hermes memory off             # volta para a memória built-in (memory.provider = "")
```

Equivalente manual (útil em dev, sem git) — **o nome do diretório é o id do provider**:

```bash
mkdir -p ~/.hermes/plugins/anhurdb
cp v2/plugins/hermes-agent/*.py v2/plugins/hermes-agent/plugin.yaml ~/.hermes/plugins/anhurdb/
$EDITOR ~/.hermes/config.yaml     # memory: { provider: anhurdb }
```

| | Claude Code | Hermes Agent |
|---|---|---|
| Origem | marketplace git/dir | git clone (ou cópia manual) |
| Destino | `~/.claude/plugins/cache/anhur/<plugin>/<version>/` | `~/.hermes/plugins/<name>/` |
| Ativação | automática ao carregar | `memory.provider: <name>` em `~/.hermes/config.yaml` |
| Efeito imediato? | **não** — hooks são registrados no **início da sessão** | **não** — provider é resolvido no init do agente; reiniciar sessão/gateway |
| Atualização | bump de `version` + reinstall | `hermes plugins update <name>` |
| Toolchain | nenhum (binários commitados) | Python + `pip_dependencies` |

---

## 6. Checklist "a memória está viva?"

> **A lição central de 2026-07-30:** nenhum destes checks, exceto o último de cada lista,
> prova que a memória chegou ao modelo. Log não prova: ele só prova que **algo** executou o
> binário — execução manual e execução por hook são indistinguíveis no log. Exit 0 não prova.
> Ausência de erro não prova. **Só perguntar ao modelo prova.**

### 6.1 Claude Code

1. **O plugin carregou.**
   ```bash
   claude plugin list      # anhurdb-memory@anhur → Status ✔ enabled
   ```
   `✘ failed to load` = nenhum hook registrado = memória morta. Causa mais comum: marketplace
   `directory` pendurado.

2. **A versão instalada é a que você acha que é.**
   ```bash
   grep -A6 '"anhurdb-memory@anhur"' ~/.claude/plugins/installed_plugins.json | grep '"version"'
   grep '"version"' v2/plugins/claude/.claude-plugin/plugin.json
   ```
   **Divergência medida em 2026-07-30:** fonte `0.1.4`, instalado `0.1.3`.

3. **O binário instalado contém as correções que você espera.**
   ```bash
   BIN=$(ls ~/.claude/plugins/cache/anhur/anhurdb-memory/*/bin/anhur-claude-memory-linux-amd64 | tail -1)
   grep -ac ANHUR_ENV_FILE "$BIN"              # >0 ⇒ tem a leitura do env file (2026-07-30)
   grep -ac 'MEMORY IS NOT BEING SAVED' "$BIN" # >0 ⇒ tem o fail-loud
   grep -ac quarantine "$BIN"                  # >0 ⇒ tem a quarentena
   grep -ac ANHUR_API_KEY "$BIN"               # controle: deve ser >0 sempre
   ```
   **Resultado medido em 2026-07-30:** `0 / 0 / 0` com o controle em `2` — ou seja,
   **fonte corrigido, binário publicado não**. Vale para
   `v2/plugins/{claude,hermes}/bin/*` e para o cache instalado.

4. **A chave foi resolvida — e de onde.**
   ```bash
   grep 'config: key source=' ~/.anhur-claude-memory/plugin.log | tail -3
   ```
   Ausência dessa linha em execuções recentes = binário pré-correção (ver item 3).
   `key source=environment` em execução de hook é sinal de alerta: significa que a
   configuração só funciona porque **aquele** shell exportou a variável.

5. **O hook realmente disparou** (o log sozinho não diz).
   ```bash
   tail -3 ~/.anhur-claude-memory/plugin.log
   # abrir uma sessão NOVA e repetir: tem de aparecer linha nova com timestamp do início da sessão
   ```

6. **Nada preso na fila nem na quarentena.**
   ```bash
   ls ~/.anhur-claude-memory/queue/*.txt 2>/dev/null | wc -l
   ls ~/.anhur-claude-memory/queue/quarantine/*.txt 2>/dev/null | wc -l   # >0 exige humano
   ls -t ~/.anhur-claude-memory/archive | head -3                          # arquivo recente?
   ```

7. **✅ O único que fecha o loop — pergunte ao modelo.**
   ```bash
   claude -p "Sem usar ferramentas: você recebeu um bloco <anhur-memory>? \
   Se sim, cite a primeira Decision. Se não, responda SEM BLOCO." </dev/null
   ```

### 6.2 Hermes Agent

1. **O plugin está no lugar certo, com o formato certo.**
   ```bash
   ls ~/.hermes/plugins/<name>/plugin.yaml ~/.hermes/plugins/<name>/__init__.py
   grep -c 'register_memory_provider\|MemoryProvider' ~/.hermes/plugins/<name>/__init__.py
   ```
   Sem `__init__.py`, ou sem uma dessas duas strings nos primeiros 8192 bytes, o loader
   **não o reconhece como memory provider** (`_is_memory_provider_dir`).

2. **Ele foi selecionado.**
   ```bash
   grep -A2 '^memory:' ~/.hermes/config.yaml      # provider: <name>
   hermes memory status                           # lista providers e marca o ativo
   ```
   **Medido em 2026-07-30:** `~/.hermes/config.yaml` **não tem** `memory.provider`.

3. **Ele está `available`.**
   ```bash
   hermes doctor        # "<name> configured but not available" = desligado em silêncio
   hermes dump | grep memory_provider
   ```

4. **Ele ativou de fato na inicialização do agente.** O agente loga
   `Memory provider '<name>' activated` em **INFO** (`agent_init.py:1317`). A **ausência**
   dessa linha, com `memory.provider` setado, significa `is_available() == False` ou
   diretório não encontrado — e esse caminho negativo é registrado apenas em **DEBUG**
   (`agent_init.py:1319`). Rode com log em DEBUG para ver a causa.

5. **O estado próprio do plugin está saudável** (fila, quarentena, último sync, último erro).
   O plugin **deve** expor isso — por `hermes memory status` (via `get_config_schema`/CLI
   própria) ou por um arquivo de estado com carimbo de tempo. Sem isso, o item 4 é o único
   sinal, e ele é DEBUG.

6. **✅ O único que fecha o loop — pergunte ao agente.** Faça, numa sessão nova, uma pergunta
   que só o `prefetch` responde ("qual foi a última decisão que registrei sobre X?"). Se vier
   vazio, o provider não está injetando, **não importa o que os itens 1–5 digam**.

### 6.3 Regra comum

| Sinal | Prova o quê |
|---|---|
| Existe log / o arquivo cresceu | Que **algo** rodou o código. Nada além disso |
| Exit 0 / sem erro | Nada. O blackout inteiro foi exit 0, stdout vazio, stderr vazio |
| Plugin instalado / `enabled` | Que o disco está certo. Não que o código rodou |
| Provider `available` | Que a checagem de config passou. Não que a memória chegou |
| **O modelo cita a memória de volta** | **O loop está fechado** |

---

## 7. Afirmações que ficaram falsas nos documentos existentes

### 7.0 `v2/plugins/docs/`

Estava **vazio** — apenas os diretórios `superpowers/` e `superpowers/specs/`, zero arquivos.
Este documento é o primeiro conteúdo. Nada a corrigir, mas registre-se: **não havia nenhum
documento comparando as duas plataformas**, e é por isso que a seção 3 pôde acontecer.

### 7.1 `v2/plugins/claude/README.md`

| Linhas | Afirmação | Status | Evidência |
|---|---|---|---|
| 11-13 | "No silent loss at the boundary … **A crash risks at most the in-flight turn**" | **FALSO (incompleto)** | A fila só protege *depois* que o motor está rodando **e com chave**. Medido no log: **744 skips consecutivos** entre `2026-07-18T18:50:21Z` e `2026-07-30T17:25:14Z` (**11,94 dias**) e **zero** linhas de qualquer outro tipo na janela — a fila nunca chegou a existir. Do último `persist` bem-sucedido (`2026-07-17T20:35:18Z`) ao seguinte (`2026-07-30T17:39:29Z`) são **12,88 dias sem uma única gravação** — é esse o "12,8 dias" registrado em `envfile.go`. O risco não era "o turno em voo" |
| 149-150 | "**`. $HOME/.anhur-claude-memory/env`** is what loads the API key" | **FALSO** | Desde `80fb003` quem carrega é o **binário** (`envfile.go`). E era falso mesmo antes na prática: sem `export`, `.` cria variável de **shell**, que o filho não herda — foi exatamente a causa do blackout |
| 128, 132, 136 | Hooks recomendados com `. …/env 2>/dev/null;` | **OBSOLETO / perigoso** | O `2>/dev/null` engole erro de sintaxe do arquivo. Mantido só por compatibilidade; documentar que **não é mais o mecanismo** |
| 26-27, 74-75 | "registers the AnhurDB **MCP tools** for explicit recall/store during a session" | **ENGANOSO** | `core.go:331-341` documenta o oposto: toda tool `mcp__anhurdb__*` exige `api_key` **nos argumentos**, e a chave é deliberadamente mantida fora do contexto. O modelo **não pode** chamá-las. O bloco injetado hoje diz isso ao modelo; o README ainda vende |
| 80-81 | "(o marketplace também oferece `anhurdb-memory-hermes` — same engine pointed at a **separate tenant**/container, for a second, **isolated** agent identity)" | **FALSO em dois pontos** | (a) `~/.anhur-claude-memory/env` e `~/.anhur-hermes-memory/env` têm a **mesma chave** (comparado por SHA-256 do valor, sem exibi-lo) ⇒ **mesmo tenant**, só o container difere; (b) "second agent identity" sugere outro agente — é outro **container do mesmo Claude Code**, e nunca esteve instalado |
| 305-312 | "A hook that never runs fails silently … **This is the one failure mode** the no-silent-loss queue does not cover" | **FALSO** | Existe uma segunda, pior: o hook **rodou 744 vezes** e não salvou nada. Silêncio com execução é mais difícil de detectar do que silêncio sem execução |
| 310-312 | "`persist` advances a per-session cursor, so once the hooks are restored **the next run backfills every turn it missed**" | **FALSO para sessões encerradas** | O cursor de fato não avançou (a guarda retorna antes). Mas `persist` só roda por hook **da sessão viva**; nenhuma execução futura ocorre para as sessões já fechadas. Os transcripts continuam em `~/.claude/projects/`, mas a recuperação exige uma varredura **manual** — não acontece sozinha |
| 182-185 | Lista de variáveis opcionais | **DESATUALIZADO** | Falta `ANHUR_ENV_FILE` (`envfile.go:113-120`), que sobrepõe o caminho do arquivo de config |
| 214-219 | Check 1: `. "$HOME/.anhur-claude-memory/env"` antes de rodar o binário | **REDUNDANTE** | O binário lê o arquivo sozinho. Manter só se o objetivo for testar o caminho legado |
| 346-356 | Processo de release (bump de `version` é o que invalida o cache) | **VERDADEIRO, mas violado hoje** | fonte `0.1.4` × cache `0.1.3`; e **nenhum** binário em `bin/` contém as correções de 2026-07-30 (seção 6.1, item 3) |
| 18-27 | "one static Go binary and three hooks"; "zero runtime dependencies"; dogfood do SDK Go | **VERDADEIRO** | `core.go`, `go.mod` |

### 7.2 `v2/plugins/hermes/README.md` — descreve uma instalação que não funciona

| Linhas | Afirmação | Status | Evidência |
|---|---|---|---|
| 1-11 (título e premissa) | "AnhurDB memory **for Hermes** … dá ao Hermes uma memória de longo prazo" | **FALSO** | O diretório é um plugin do **Claude Code** (`.claude-plugin/plugin.json`, `hooks/hooks.json`, binário Go). O Hermes Agent procura `plugin.yaml` + `__init__.py` Python e **nunca o veria** |
| 7-11 | "reads/writes under **its OWN tenant** (via `ANHUR_API_KEY`) … Install both side-by-side to keep **two separate memories that never mix**" | **FALSO** | Mesma chave nos dois arquivos de env (verificado por hash) ⇒ **mesmo tenant**. `ANHUR_CONTAINER` (`fable-1` × `hermes-1`) é filtro de recall, **não** isolamento. Isolamento real exige chave de tenant dedicada |
| 58-88 (todo o "Install") | `cd v2/plugins/hermes && make build`; `/plugin marketplace add Yoven/AnhurDB-SDK`; `/plugin install anhurdb-memory-hermes@anhur` | **NÃO FUNCIONA para o propósito declarado** | São comandos do **Claude Code**. Para o Hermes o comando é `hermes plugins install …` + `hermes memory setup`. E o plugin **nunca foi instalado em lugar nenhum**: ausente de `~/.claude/plugins/installed_plugins.json` e de `~/.hermes/plugins/` |
| 86-88 | "Prefer no marketplace at all? `make install` … wire three hooks yourself — see ../claude/README.md → Option B" | **FALSO para o Hermes** | Option B são hooks em `~/.claude/settings.json`. O Hermes não tem esse arquivo nem esse mecanismo |
| 24-27 | "The plugin also registers the AnhurDB **MCP tools** for explicit recall/store" | **ENGANOSO** | Mesma ressalva de 7.1 (api_key obrigatório nos args) |
| 60-63 | "Build from source (**optional** — the plugin ships prebuilt binaries)" | **PERIGOSO hoje** | Os binários existem, mas **sem** as correções de 2026-07-30 (`ANHUR_ENV_FILE`, fail-loud, quarentena) — medido |
| 93-105 | "Verify it works": `export ANHUR_API_KEY="…"` + `./bin/anhur-hermes-memory recall` | **FRÁGIL / desatualizado** | Ensina exatamente o caminho que quebrou (chave só no shell) e roda um binário **de dentro do worktree** — o que o próprio `claude/README.md` proíbe para hooks |
| 105 | "Diagnostics … `~/.anhur-hermes-memory/plugin.log`" | **VERDADEIRO — e é a prova do abandono** | 59 linhas, última em 2026-07-17T12:30:10Z, **zero** `persist:` |
| 137-149 | Tabela dos 3 campos de `core.Config` (`StateDirName`, `DefaultContainer`, `BinaryName`) | **VERDADEIRO** | Conferido em `hermes/cmd/anhur-hermes-memory/main.go` e `core.go:53-62` |
| 29-44 ("What's in here") | Árvore de arquivos | **VERDADEIRO** | Confere com o disco — e é justamente a árvore **errada** para o Hermes |

### 7.3 `v2/plugins/hermes/.env.example`

| Afirmação | Status | Evidência |
|---|---|---|
| "`ANHUR_MCP_URL` — only needed if you let THIS plugin register the MCP server (`.mcp.json`)" | **SEM EFEITO** | `hermes/.mcp.json` traz a URL **literal** (`https://anhurdb.yoven.ai/mcp`), diferente de `claude/.mcp.json`, que usa `${ANHUR_MCP_URL:-…}`. A variável nunca é lida no lado hermes |
| `export ANHUR_CONTAINER=hermes-ltm` | **DIVERGE do real** | O arquivo vivo `~/.anhur-hermes-memory/env` usa `hermes-1` (e o do claude usa `fable-1`, não `claude-ltm`). Documentação e runtime discordam |
| Bloco de `export` obrigatório em todas as linhas | **CORRETO e importante** | O arquivo vivo `~/.anhur-hermes-memory/env` tem **0 linhas com `export`** — está exatamente no estado que produziu o blackout do lado claude. Só não quebrou porque nunca foi usado |

---

## 8. Como reproduzir as evidências deste documento

```bash
# --- estado das instalações ---
cat ~/.claude/plugins/installed_plugins.json         # anhurdb-memory@anhur 0.1.3; hermes AUSENTE
cat ~/.claude/plugins/known_marketplaces.json        # anhur → source "directory"
ls ~/.hermes/plugins/                                # só hermes-achievements (não é plugin)
grep -c '^memory:' ~/.hermes/config.yaml             # 0 → nenhum provider selecionado

# --- o log que prova o abandono do plugin hermes ---
wc -l ~/.anhur-hermes-memory/plugin.log              # 59
tail -1 ~/.anhur-hermes-memory/plugin.log            # 2026-07-17T12:30:10Z
grep -c 'persist:' ~/.anhur-hermes-memory/plugin.log # 0
# (grep sem os dois-pontos devolve 2, e as duas ocorrências são a linha "usage: … <recall|persist>")

# --- o blackout no plugin claude ---
L=~/.anhur-claude-memory/plugin.log
awk '$0<"2026-07-18T18:50"' $L | grep 'persist: lines' | tail -1  # último write OK: 2026-07-17T20:35:18Z
awk '$0>"2026-07-18T18:34:03"' $L | grep -c  skipping             # 744 skips consecutivos
awk '$0>"2026-07-18T18:34:03"' $L | grep -v  skipping | head -1   # 1ª não-skip: 2026-07-30T17:39:29Z
# ⇒ 744 skips em 11,94 dias (18:50:21 07-18 → 17:25:14 07-30) com ZERO linhas de outro tipo
#   na janela; e 12,88 dias entre um write bem-sucedido e o seguinte (o "12,8 dias" do envfile.go)

# --- correções presentes no fonte, ausentes no binário publicado ---
for b in v2/plugins/claude/bin/anhur-claude-memory-linux-amd64 \
         v2/plugins/hermes/bin/anhur-hermes-memory-linux-amd64; do
  echo "$b: env_file=$(grep -ac ANHUR_ENV_FILE $b) loud=$(grep -ac 'MEMORY IS NOT BEING SAVED' $b) \
quarantine=$(grep -ac quarantine $b) controle=$(grep -ac ANHUR_API_KEY $b)"
done   # medido: 0 0 0 2  → sem as correções

# --- mesma chave nos dois plugins (sem NUNCA imprimir a chave) ---
for f in ~/.anhur-claude-memory/env ~/.anhur-hermes-memory/env; do
  grep -E '^(export )?ANHUR_API_KEY=' "$f" | head -1 \
    | sed -E 's/^(export )?ANHUR_API_KEY=//; s/^["'"'"']//; s/["'"'"']$//' \
    | sha256sum | cut -c1-12
done   # os dois hashes são IGUAIS ⇒ mesmo tenant

# --- contrato real do Hermes (fonte instalado) ---
H=~/.hermes/hermes-agent
sed -n '1,40p'      $H/agent/memory_provider.py     # docstring do ciclo de vida
sed -n '319,337p'   $H/plugins/memory/__init__.py   # _ProviderCollector: register_hook é NO-OP
sed -n '1257,1323p' $H/agent/agent_init.py          # ativação; DEBUG quando não disponível
sed -n '135,175p'   $H/hermes_cli/plugins.py        # VALID_HOOKS
sed -n '1548,1568p' $H/hermes_cli/plugins.py        # auto-coerção para kind="exclusive"
```

---

## 9. Pendências conhecidas (não resolvidas por este documento)

1. **O plugin de memória do Hermes existe no repositório, mas não está vivo.**
   `v2/plugins/hermes-agent/` tem o formato certo; falta commitar, instalar
   (`~/.hermes/plugins/anhurdb/`), apontar `memory.provider: anhurdb` e **confirmar pelo
   agente** (seção 6.2, item 6). Até lá vale o critério da seção 3: é um diretório, não um
   plugin.
2. **`v2/plugins/hermes/` ficou órfão.** É um plugin do Claude Code com nome de Hermes,
   nunca instalado, superado por `hermes-agent/`. Manter os dois convida a repetir a
   confusão: ou vira explicitamente "a segunda identidade de memória do Claude Code" (e o
   README para de falar em Hermes), ou sai do marketplace e do repositório.
3. **Os binários publicados estão atrás do fonte** (seção 6.1, item 3), incluindo o que o
   Claude Code está executando agora. `make release-binaries` + bump de `version` +
   reinstalar — nesta ordem — é o que fecha isso.
4. **Divergência de manifesto:** fonte `0.1.4` × cache instalado `0.1.3`.
5. **`anhurdb-memory-hermes` está anunciado no marketplace** (`.claude-plugin/marketplace.json`)
   como identidade isolada, e **não é isolada** (mesma chave/tenant). Ou se cria uma chave de
   tenant dedicada, ou a descrição precisa parar de prometer isolamento.
6. **O tenant da chave atual é desconhecido** e só pode ser confirmado por
   `GET /internal/admin/keys` com acesso `MasterOnly` no ambiente hospedado. Nada aqui
   afirma qual é.
7. **Recuperação dos 12,88 dias:** os transcripts das sessões afetadas ainda existem em
   `~/.claude/projects/`, mas **nenhum caminho automático** os persistirá (o hook só roda
   para a sessão viva). É trabalho manual, e ainda não foi feito.
8. **Monitor do lado Claude em construção (não verificado aqui):** existe
   `v2/plugins/claude/scripts/anhur-memory-watch.sh`, escrito para transformar `plugin.log`
   em notificações dentro da sessão. No momento desta escrita o
   `v2/plugins/claude/monitors/monitors.json` que ele declara **não existe**, então o
   monitor ainda não está ligado. Quando estiver, ele vira mais um item da seção 6.1 —
   mas **não** substitui o item 7 (perguntar ao modelo): um monitor declarado pelo plugin
   morre junto com o plugin que não carrega.
