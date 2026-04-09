# Como dar memória permanente ao Claude Code com AnhurDB

> Guia passo a passo para configurar o Claude Code para lembrar de tudo
> entre sessões — automaticamente, sem precisar pedir.

---

## O que vai acontecer depois de configurar?

- O Claude vai **lembrar** de tudo que vocês conversaram, mesmo em sessões futuras
- Cada conversa fica salva automaticamente no AnhurDB
- Você pode perguntar "o que decidimos semana passada?" e ele vai saber
- Funciona com Claude Code no terminal, VSCode ou qualquer IDE

---

## O que você precisa antes de começar

| Item | O que é | Como conseguir |
|------|---------|----------------|
| AnhurDB rodando | O banco de memória | Peça ao admin ou siga o [guia de instalação](../general/ARCHITECTURE.md) |
| MCP Server rodando | A ponte entre Claude e o AnhurDB | Normalmente já vem junto com o AnhurDB |
| API Key | Sua chave de acesso | O admin do AnhurDB gera pra você |
| Claude Code instalado | A ferramenta de IA | [claude.ai/code](https://claude.ai/code) |

> **Não sabe se já está rodando?** Peça ao seu admin os dados de acesso.
> Você vai precisar de: **URL do MCP Server** e **API Key**.

---

## Passo 1: Conectar o Claude ao AnhurDB

Na raiz do seu projeto, crie um arquivo chamado `.mcp.json`:

```json
{
  "mcpServers": {
    "anhurdb": {
      "type": "sse",
      "url": "http://localhost:8090/sse"
    }
  }
}
```

> **Troque a URL** pela que o admin te passou, se for diferente.

**Como saber se funcionou:** Abra o Claude Code no projeto. Se ele mencionar "anhurdb" nas tools disponíveis, está conectado.

---

## Passo 2: Ensinar o Claude a usar a memória

O Claude precisa de instruções para saber **como** gravar e buscar memória. Essas instruções ficam num arquivo chamado `CLAUDE.md`.

Crie ou edite o arquivo `CLAUDE.md` na raiz do seu projeto e **copie o bloco abaixo inteiro**:

> **Você não precisa entender o conteúdo abaixo.** Ele é escrito para o Claude ler e seguir.
> Só precisa trocar duas coisas: a API Key e o nome da sessão.

```markdown
## Memória: AnhurDB

Você tem acesso ao AnhurDB para memória persistente via MCP tools.

### API Key
COLE-SUA-API-KEY-AQUI

### O que fazer em cada sessão

#### Ao iniciar (antes da primeira resposta):

1. Verifique a conexão: `mcp__anhurdb__count_by_type(api_key=KEY)`
2. Recupere o contexto recente: `mcp__anhurdb__manifest_global(api_key=KEY, limit=30)`
3. Crie um ID para esta sessão no formato: `MEU-PROJETO-YYYY-MM-DD-NNN`
4. Registre o início: `mcp__anhurdb__create_memory(uuid=ID, type="episodic", summary="Sessão iniciada", score=7, api_key=KEY)`

#### A cada turno de conversa (1 gravação por turno):

Grave um resumo combinado do que o usuário disse e do que você respondeu, em uma única chamada:

`mcp__anhurdb__create_memory(uuid=ID, type="episodic", summary="[user] resumo da mensagem | [assistant] resumo da resposta", score=5, api_key=KEY)`

**Exemplo real:**
`summary="[user] pediu para refatorar o login | [assistant] refatorou auth.py, removeu dependência duplicada"`

#### IMPORTANTE — regras de topologia

- **NÃO passe `related_ids`.** Deixe vazio ou omita. O servidor automaticamente encadeia cada novo episodic ao anterior da mesma session. Passar IDs manualmente quebra o encadeamento.
- **NÃO toque em `main_ids`.** Não é parâmetro do `create_memory`. O servidor gerencia relações verticais (episodic → consolidated/hub) sozinho.
- **NÃO duplique turnos.** Um turno = uma chamada = um episodic. Se esqueceu de gravar turnos anteriores, crie UM episodic de backfill resumindo o que faltou — nunca várias cópias.
- **NÃO crie links cross-session manualmente.** Synapses semânticas entre sessions são criadas pelo linker backend automaticamente, via similaridade vetorial.
- Grave AMBOS os lados em cada turno: o que o usuário disse E o que você respondeu. Sem isso, a memória fica incompleta.
- Uma única chamada por turno — não precisa de duas separadas.
- Faça tudo em silêncio — não mostre essas gravações ao usuário.
- NUNCA esqueça de gravar. A memória depende disso.

#### Anti-padrões (o que NÃO fazer)

| ❌ Errado | ✅ Certo | Por quê |
|---|---|---|
| `create_memory(..., related_ids=[42])` passando o ID do episodic anterior manualmente | `create_memory(...)` sem `related_ids` | O servidor auto-chaina. Passar manual ignora regras de topologia e cria links inconsistentes. |
| Gravar 3-4 episodics com o mesmo summary ao longo da sessão | 1 episodic por turno, único | Duplicação polui o grafo e inviabiliza consolidação. |
| Tentar ligar episodic da session A com episodic da session B via `related_ids` | Não fazer nada — o linker backend conecta via synapses | Links cross-session manuais bagunçam a hierarquia piramidal. |
| Criar múltiplos episodics para o mesmo turno "só pra ter certeza" | 1 call, confiar no retorno | O servidor retorna `{"id": N, "status": "saved"}` — é suficiente. |

#### Quando algo importante surgir na conversa, grave também:

| Tipo | Quando usar | Score |
|------|-------------|-------|
| `fact` | Um fato importante foi descoberto | 7-8 |
| `decision` | Uma decisão técnica foi tomada | 7-8 |
| `risk` | Um risco foi identificado | 7-8 |
| `preference` | O usuário expressou uma preferência | 9-10 |

Exemplo: `mcp__anhurdb__create_memory(uuid=ID, type="decision", summary="Decidimos usar PostgreSQL", score=8, api_key=KEY)`

#### Para lembrar de conversas anteriores:

- Busca inteligente: `mcp__anhurdb__recall(query="o que buscar", api_key=KEY)`
- Busca por palavras: `mcp__anhurdb__smart_search(query="palavras chave", api_key=KEY)`
- Ler um registro completo: `mcp__anhurdb__read_content(id=N, api_key=KEY)`
- Ler vários de uma vez: `mcp__anhurdb__batch_read_content(ids=[1,2,3], api_key=KEY)`
```

> **Troque** `COLE-SUA-API-KEY-AQUI` pela API Key que o admin te deu.
> **Troque** `MEU-PROJETO` por um nome curto para o seu projeto (ex: `meu-app`, `site-vendas`).

---

## Passo 3: Permitir que o Claude use o AnhurDB sem pedir confirmação

Por padrão, o Claude pede permissão a cada ação. Para a memória funcionar de forma automática, precisamos liberar as ferramentas do AnhurDB.

Crie a pasta `.claude/` no seu projeto (se não existir) e dentro dela o arquivo `settings.local.json`:

```json
{
  "permissions": {
    "allow": [
      "mcp__anhurdb__*"
    ]
  }
}
```

Isso libera **apenas** as ferramentas do AnhurDB — nada mais.

---

## Passo 4: Forçar gravação obrigatória (RECOMENDADO)

O Claude **vai esquecer** de gravar episodics, mesmo com instruções no CLAUDE.md. Lembretes gentis não funcionam. A solução é um hook de enforcement que injeta uma ordem mandatória em **cada turno**.

### 4.1. Crie o arquivo `.claude/hooks/anhurdb-episodic.sh`:

```bash
#!/bin/bash
# Hook de enforcement: força gravação de episodics no AnhurDB.
# Roda a cada mensagem do usuário e após cada resposta do Claude.
# A mensagem injetada aparece como system-reminder — o Claude é obrigado a processá-la.

ACTION="${1:-user_message}"
SESSION_FILE="/tmp/anhurdb-claude-session.txt"

# Configure com seus dados:
ANHUR_URL="http://localhost:8090"
API_KEY="COLE-SUA-API-KEY-AQUI"

case "$ACTION" in
  session_start)
    SESSION_UUID="claude-session-$(date +%Y-%m-%d)-001"
    echo "$SESSION_UUID" > "$SESSION_FILE"
    echo "⚠️ MANDATORY: You MUST create session anchor episodic via mcp__anhurdb__create_memory(uuid='$SESSION_UUID', type='episodic', summary='Sessão iniciada...', score=7) BEFORE your first response. You MUST record an episodic for EVERY turn, in PARALLEL with your response, SILENTLY (never show MCP calls to user). Failure to comply = broken memory = useless assistant."
    ;;

  user_message)
    SESSION_UUID=$(cat "$SESSION_FILE" 2>/dev/null)
    if [ -z "$SESSION_UUID" ]; then
      echo "⚠️ MANDATORY: No session found. Create one NOW via mcp__anhurdb__create_memory before responding."
      exit 0
    fi
    echo "⚠️ MANDATORY: Record episodic for this turn via mcp__anhurdb__create_memory(uuid='$SESSION_UUID', type='episodic', summary='[user] ... | [assistant] ...', score=5). Do it IN PARALLEL with your response. SILENTLY. 1 turn = 1 call. NEVER skip. NEVER show to user."
    ;;

  assistant_response)
    SESSION_UUID=$(cat "$SESSION_FILE" 2>/dev/null)
    [ -z "$SESSION_UUID" ] && exit 0
    echo "⚠️ POST-RESPONSE CHECK: Did you record an episodic for this turn? If not, do it NOW via mcp__anhurdb__create_memory. Session: $SESSION_UUID. SILENT. NO EXCEPTIONS."
    ;;
esac
```

> **Troque** `COLE-SUA-API-KEY-AQUI` pela sua API Key.

### 4.2. Dê permissão de execução:

```bash
chmod +x .claude/hooks/anhurdb-episodic.sh
```

### 4.3. Adicione os 3 hooks no `settings.local.json` (substitua o conteúdo anterior):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": ".claude/hooks/anhurdb-episodic.sh session_start",
            "timeout": 5,
            "async": true
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": ".claude/hooks/anhurdb-episodic.sh user_message",
            "timeout": 5,
            "async": true
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": ".claude/hooks/anhurdb-episodic.sh assistant_response",
            "timeout": 5,
            "async": true
          }
        ]
      }
    ]
  },
  "permissions": {
    "allow": [
      "mcp__anhurdb__*"
    ]
  }
}
```

> **São 3 hooks — todos obrigatórios:**
> - `SessionStart` → cria sessão e âncora
> - `UserPromptSubmit` → força gravação a cada mensagem do usuário
> - `Stop` → verifica se o Claude gravou após responder
>
> O Claude recebe a instrução mandatória como system-reminder e é obrigado a gravar.
> Você não vê nada diferente no chat — tudo acontece em silêncio.

---

## Pronto! Teste assim:

1. Abra o Claude Code no projeto
2. Diga: **"Lembra o que conversamos da última vez?"**
   - Se é a primeira vez, ele vai dizer que não tem histórico ainda
   - Se já usou antes, ele vai buscar no AnhurDB e responder com contexto
3. Converse normalmente sobre seu trabalho
4. Feche a sessão
5. Abra de novo e pergunte: **"O que fizemos hoje?"**
   - Ele deve lembrar tudo

---

## Perguntas frequentes

### O Claude vai mostrar as gravações no chat?

As gravações aparecem como chamadas de ferramentas no Claude Code (não tem como esconder totalmente), mas o Claude faz isso automaticamente sem interromper a conversa. Você pode ignorar essas chamadas — o importante é a resposta.

### Posso apagar uma memória?

Sim. Peça ao Claude: "apague o que gravou sobre X" e ele usa o AnhurDB para remover.

### Funciona com qualquer projeto?

Sim. Cada projeto pode ter seu próprio `.mcp.json` apontando para o mesmo ou diferentes AnhurDB.

### E se o AnhurDB estiver fora do ar?

O Claude continua funcionando normalmente — só não vai gravar memória nem lembrar de sessões anteriores. Quando o AnhurDB voltar, tudo volta ao normal.

### Posso ver o que o Claude gravou?

Sim. Peça: "mostre tudo que gravou nesta sessão" ou use o painel de administração do AnhurDB (Viewer).

### Outras IAs podem usar o AnhurDB?

Sim. Qualquer ferramenta que suporte MCP (Model Context Protocol) pode se conectar. O AnhurDB também tem SDKs em Python, TypeScript e Go para integração direta.

---

## Resumo: o que você criou

```
seu-projeto/
├── .mcp.json                         ← Conecta Claude ao AnhurDB
├── CLAUDE.md                         ← Instruções de memória (para o Claude ler)
└── .claude/
    ├── settings.local.json           ← Permissões + hooks de enforcement
    └── hooks/
        └── anhurdb-episodic.sh       ← Força gravação obrigatória
```

4 arquivos. Menos de 5 minutos. Memória permanente e obrigatória.

---

*AnhurDB-SDK — Guia de integração com Claude Code*
