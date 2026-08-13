# Contrato de erro dos SDKs (Go · Python · TypeScript)

**Status:** proposto · **Data:** 2026-08-13 · **Origem:** benchmark LongMemEval
abortado contra produção com erro de mensagem vazia e sem status

---

## O princípio

**O cliente precisa saber o suficiente para decidir; nunca o suficiente para
mapear o cluster.**

Três decisões o cliente toma a partir de um erro, e só três:

1. **Repetir agora?** (transitório)
2. **Repetir nunca?** (contrato errado — corrigir o código)
3. **Repetir com outra credencial?** (auth)

Tudo que não serve a uma dessas três decisões é ruído para o cliente e superfície
de ataque para quem observa. Endereço de líder, ID de nó, porta interna, nome de
grupo Raft, caminho de arquivo: **nunca**.

O servidor já faz a parte dele. `router/http_proxy.go:279` deliberadamente retém
o corpo do backend e devolve um 503 genérico, com o detalhe só no log —
justamente porque o corpo do backend embute o endereço interno do líder. **Este
documento é sobre o outro lado: o SDK não pode perder o que o servidor entregou.**

---

## Os três defeitos encontrados (Python; verificar equivalentes em Go/TS)

### 1. `asyncio.TimeoutError` escapa o tratamento inteiro

`connection.py` captura `except aiohttp.ClientError`. O timeout **total** do
aiohttp levanta `asyncio.TimeoutError`, que **não é** subclasse de `ClientError`
— verificado: `issubclass(asyncio.TimeoutError, aiohttp.ClientError) == False`, e
`connection.py` não menciona `TimeoutError` em nenhuma linha.

Resultado: a exceção sobe crua, com `str()` **vazio** (`repr(str(asyncio.TimeoutError())) == "''"`)
e sem `status_code`. Foi exatamente o que abortou o benchmark:

```
FATAL during search for question gpt4_7de946e7:
```

Um erro sem texto e sem status é indistinguível de um bug do próprio cliente.

### 2. `status_code` perdido no caminho GET

O mesmo caso HTTP é tratado de dois jeitos:

```python
# connection.py:383 — GET
raise AnhurError(f"Server error (HTTP {response.status}): {body_text[:500]}")     # sem status_code

# connection.py:496 — POST
raise AnhurError(f"Server error (HTTP {response.status}): ...",
                 status_code=response.status)                                      # com status_code
```

Os ramos 409, 415 e 429 do caminho GET têm o mesmo problema. Quem programa
`if err.status_code == 409` acerta em POST e erra em GET.

### 3. Não há classificação de "vale repetir"

Hoje o chamador precisa reimplementar a regra: 5xx e 429 são transitórios, 4xx
não, ausência de status significa que a requisição não saiu do cliente. Cada
integração vai errar essa regra de um jeito diferente.

---

## O contrato

Todo erro que sai do SDK carrega **quatro** campos:

| Campo | Tipo | Significado |
|---|---|---|
| `message` | string | frase legível, **sempre não-vazia** |
| `status` | int? | status HTTP; **ausente = a requisição nunca chegou ao servidor** |
| `retryable` | bool | se repetir a mesma chamada pode ter resultado diferente |
| `kind` | enum | `auth` · `invalid_request` · `not_found` · `conflict` · `rate_limited` · `unavailable` · `timeout` · `transport` · `server` |

### Regra de `retryable`

```
retryable = true   se  kind ∈ {rate_limited, unavailable, timeout, transport}
retryable = false  se  kind ∈ {auth, invalid_request, not_found, conflict}
retryable = false  se  status ∈ [400,500) e status ≠ 429
retryable = true   se  status ≥ 500
```

**`timeout` e `transport` são retryable, mas com uma ressalva que o SDK deve
documentar e não pode resolver sozinho:** numa escrita, um timeout significa
*"pode ou não ter sido gravado"*. O servidor já é explícito sobre isso
(`response.go:190`: *"the write may or may not have committed — not retried
automatically to avoid duplicates"*). O SDK **não deve** repetir escritas
automaticamente; ele expõe `retryable=true` e deixa a decisão de idempotência
com quem chama.

### Regra de `message`

Nunca vazia. Quando a origem não fornece texto (o caso do `TimeoutError`), o SDK
sintetiza a partir do `kind`:

```
timeout    → "request timed out after {N}s (the server may still have processed it)"
transport  → "could not reach AnhurDB: {ExceptionClassName}"
unavailable→ "service temporarily unavailable — retry"
```

O nome da classe da exceção é aceitável; **URL, host e porta não são** — a regra
de segurança que já existe em `connection.py:516` continua valendo.

---

## Paridade nos três SDKs

Mesmos nomes de campo, mesma enum, mesma regra de `retryable`. A nomenclatura
segue a convenção de cada linguagem, a semântica não muda.

**Python**
```python
class AnhurError(Exception):
    message: str
    status_code: Optional[int]
    retryable: bool
    kind: str
```

**Go**
```go
type AnhurError struct {
    Message   string
    Status    int    // 0 = não chegou ao servidor
    Retryable bool
    Kind      string
}
func (e *AnhurError) Error() string { return e.Message }
```

**TypeScript**
```ts
class AnhurError extends Error {
  readonly status?: number
  readonly retryable: boolean
  readonly kind: AnhurErrorKind
}
```

### Onde cada um precisa ser corrigido

| SDK | Ação |
|---|---|
| Python | capturar `asyncio.TimeoutError` **antes** de `aiohttp.ClientError`; passar `status_code` nos 4 ramos do caminho GET; derivar `kind`/`retryable` num único ponto |
| Go | auditar se `context.DeadlineExceeded` é embrulhado ou vaza cru — é o análogo exato do defeito nº 1 |
| TypeScript | auditar `AbortError` do `fetch` (timeout) e rejeições sem `.message`; erro de rede em `fetch` também chega sem status |

**A auditoria de Go e TS é obrigatória, não opcional.** O defeito do Python é
estrutural — "a exceção de timeout não é da família que eu capturo" — e as três
linguagens têm exatamente essa forma de armadilha.

---

## Testes que provam o contrato

Um por SDK, tabela única, sem servidor:

1. 401 → `kind=auth`, `retryable=false`, `status=401`, `message` não-vazia
2. 400 → `kind=invalid_request`, `retryable=false`
3. 409 → `kind=conflict`, `retryable=false`, **`status=409` presente** (regressão do defeito nº 2)
4. 429 → `kind=rate_limited`, `retryable=true`
5. 503 → `kind=unavailable`, `retryable=true`
6. timeout → `kind=timeout`, `retryable=true`, `status` **ausente**, **`message` não-vazia** (regressão do defeito nº 1)
7. conexão recusada → `kind=transport`, `retryable=true`, `status` ausente
8. corpo de erro contendo `10.20.1.2:19090` → o endereço **não** aparece em `message`

O teste 8 é o que impede que "melhorar as mensagens de erro" vire vazamento de
topologia. O servidor já retém esse corpo; o SDK não pode reintroduzi-lo por
outro caminho.

---

## Por que isto importa além da ergonomia

O benchmark abortou em 190/200 e **não** produziu um número falso — mas só
porque o harness trata exceção desconhecida como fatal. Um cliente que
classificasse aquele erro vazio como transitório teria repetido, contado a
pergunta como miss, e publicado um recall deflacionado sem nenhum sinal de que
algo deu errado.

Esse é precisamente o modo de falha que o produto existe para eliminar. Um SDK
que perde o erro é uma memória que perde a escrita em silêncio, uma camada acima.
