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

## Recusa tipada: limite de records por sessão (2026-11-20, ADR-0033)

Um 409 **não** é como os outros. "A sessão está cheia" é uma recusa que nunca
melhora repetindo na mesma sessão, e um cliente que só vê `HTTP 409` não a
distingue de um conflito qualquer — foi assim que uma sessão cheia passou 10 dias
parecendo queda no plugin de memória. Os três SDKs agora levantam um erro
**tipado**, subclasse do erro de 409 que já existia (nenhum `catch` antigo quebra):

| SDK | Tipo | Campos |
|---|---|---|
| Go | `*client.SessionRecordLimitError` + sentinela `client.ErrSessionRecordLimit` | `SessionUUID string`, `Limit *int`, `Count *int`, `ServerMessage string`; `Kind()=conflict`, `Retryable()=false`, `StatusCode()=409`; `Unwrap()` devolve a sentinela **e** o `*APIError` 409 |
| Python | `AnhurSessionRecordLimitError(AnhurQueryError)` | `session_uuid`, `limit`, `count` (`Optional[int]`), `server_message`, `code`; `kind="conflict"`, `retryable=False`, `status_code=409` |
| TypeScript | `AnhurSessionRecordLimitError extends AnhurError` | `sessionUuid`, `limit`, `count` (`number \| null`), `serverMessage`, `code`; `kind="conflict"`, `retryable=false`, `statusCode=409` |

**Reconhecimento — as duas gerações de servidor:**

- servidor NOVO: HTTP 409 com `{"error": "session has reached the maximum of N records ...", "code": "session_record_limit", "limit": N, "count": M, "session_uuid": "..."}`;
- servidor ANTIGO (produção até o deploy): HTTP 409 com só `{"error": "session has reached the maximum of N records"}` (ou o mesmo texto cru).

Casa por `code == "session_record_limit"` **ou** pelo prefixo de texto
`"session has reached the maximum of"`, e **só** em HTTP 409. Campo ausente vira
`nil`/`None`/`null`, nunca zero inventado — com uma exceção declarada: `limit` é
relido da própria frase do servidor ("maximum of N records") quando o campo falta,
porque o número está no texto que ele mandou. `count` e `session_uuid` não têm
essa fonte e ficam nulos.

**gRPC:** o contrato do servidor é `codes.ResourceExhausted` + `google.rpc.ErrorInfo`
(reason `SESSION_RECORD_LIMIT`, domain `anhurdb`, metadata `session_uuid`/`limit`/`count`),
e servidores antigos mandam `codes.AlreadyExists` com o mesmo texto. **Nenhum dos
três SDKs tem transporte gRPC hoje** (todos falam REST), então não há caminho gRPC a
classificar; um transporte gRPC futuro TEM de reconhecer os dois códigos.

**Não-retryable não é "descarte".** Repetir na mesma sessão dá a mesma recusa até o
teto mudar no servidor. Quem não pode perder a escrita guarda e tenta DEPOIS; gravar
em outra sessão para driblar o teto fundiria conversas (no plugin de memória:
1 sessão Claude = 1 sessão AnhurDB, inviolável).

---

## Recusas tipadas da biblioteca L2 (2026-11-20, ADR-0029)

Servidor AnhurDB `fix/l2-lib-scope`. Três respostas novas, reconhecidas no
transporte dos três SDKs **depois** do teste de limite de sessão (que continua
casando só por `code`/prefixo próprios — as duas recusas 409 nunca se confundem):

| Resposta do servidor | Go | Python | TypeScript | kind / retryable |
|---|---|---|---|---|
| Colisão: HTTP **409** `{"error":"<frase>","code":"tenant_library_collision"}` em **todas** as portas — busca, by-id, delete-by-file **e upload** (servidor 4574c4d, `handler/search_scope_request.go` `writeScopeLegError`). Os SDKs ramificam **primeiro no `code`**; a frase `"tenant library store id is a registered tenant"` é só fallback para corpo **sem** `code` (servidor anterior a 4574c4d, que respondia o upload com **400**). Corpo com outro `code` nunca é relido pela frase | `*TenantLibraryCollisionError` + `ErrTenantLibraryCollision` (const `TenantLibraryCollisionCode`) | `AnhurTenantLibraryCollisionError(AnhurQueryError)` | `AnhurTenantLibraryCollisionError` | conflict (409; invalid_request só no 400 legado) / **false** |
| Upload status ambíguo: 409 `"record N is a file upload in more than one plane; ..."` (`handler/upload_status_scope.go:186-191`) | `*UploadStatusAmbiguousError` + `ErrUploadStatusAmbiguous` | `AnhurUploadStatusAmbiguousError` | `AnhurUploadStatusAmbiguousError` | conflict / false |
| Delete-by-file parcial: 500 `{"error","partial":true,"legs":[...]}` (`handler/record_delete_file_library.go:53-57`) | `*LibraryDeletePartialError` (`.Legs`) + `ErrLibraryDeletePartial` | `AnhurLibraryDeletePartialError` (`.legs`) | `AnhurLibraryDeletePartialError` (`.legs`) | server / **true** (o delete é idempotente; repetir é a recuperação documentada) |

Os três desembrulham/estendem o erro HTTP que já existia (Go: `errors.As(err,
&apiErr)` acha o `*APIError`; Python/TS: subclasse de `AnhurQueryError`/`AnhurError`),
então nenhum `catch` antigo quebra. As pernas do delete parcial são lidas do corpo
INTEIRO (TS passou a ler até 64 KiB do corpo de erro; a mensagem continua citando
500 caracteres), porque a frase do servidor sozinha pode passar de 500.

`ErrTenantRegistryUnavailable` (o servidor não conseguiu ler `api_keys` para provar
que não há colisão) chega como 503 em todas as portas — **inclusive o upload**, que
antes respondia 400 — e continua o erro comum `unavailable` / retryable; não é tipado.

gRPC (nenhum SDK tem transporte gRPC hoje): a colisão é `FAILED_PRECONDITION` com
`ErrorInfo{reason: "TENANT_LIBRARY_COLLISION", domain: "anhurdb"}` — o mesmo formato
do limite de sessão. Um transporte gRPC futuro deve ramificar no `reason`.

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
9. 409 de sessão cheia — corpo NOVO estruturado e corpo ANTIGO só-texto → erro tipado de limite de sessão, `retryable=false`, campos ausentes nulos; um 409 genérico continua genérico (`session_limit_test.go`, `test_session_record_limit.py`, `session_record_limit.test.ts`)
10. Recusas L2 (colisão 409 com `code` em toda porta + fallback pela frase sem `code` — 409/400 legado, upload 503 = unavailable comum, upload ambíguo 409, delete parcial 500 com pernas além do excerto) — tipadas, e um 409 genérico/sessão cheia continua o que era (`scope_errors_test.go`, `test_record_scope.py`, `record_scope.test.ts`)

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
