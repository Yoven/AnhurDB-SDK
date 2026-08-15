# longmemeval-dense

Corpus **denso** do LongMemEval: uma sessão por sessão do oracle, **um episodic
por turno**, ingerido pelo caminho que roda a extração.

## Por que existe

O harness `longmemeval-pooled` escreve cada sessão como UM episodic via
`POST /records`, que **não passa pela extração**. O corpus resultante mede
**69% episodic / 31% estruturado**. O tenant de memória real é o oposto:
**12% episodic / 88% estruturado**.

Qualquer sinal que dependa de tipo — o prior de tipo, o peso cognitivo — só tem
informação onde existem tipos. Medir esses botões no corpus achatado responde a
pergunta errada.

## Dimensão (medida no oracle, não estimada)

| perguntas | sessões | episodics | episodics/sessão |
|---|---|---|---|
| 100 | 265 | 3.094 | ~11,7 |
| **200** | **491** | **5.724** | **11,7** |
| 500 | 940 | 10.960 | ~11,7 |

Custo dominante: **uma chamada de LLM por episodic** na extração. Escolha o
tamanho por aí, não pelo número de perguntas.

## Uso

```bash
export ANHUR_API_KEY=...                      # nunca aparece em log
export ANHUR_BASE=https://anhurdb.yoven.ai
export DENSE_TENANT=<client_id>_<tenant>      # tenant NOVO, vazio
export DENSE_ORACLE=/caminho/longmemeval_oracle.json
export DENSE_QUESTIONS=200

python3 build_corpus.py plan      # mostra o tamanho, não escreve
python3 build_corpus.py ingest    # escreve (idempotente)
```

Depois da ingestão o pipeline precisa **maturar**: extração → consolidação →
hub. Antes de medir, confira a composição:

```bash
curl -s -G "$ANHUR_BASE/api/v1/sessions/stats" \
  -H "X-API-Key: $ANHUR_API_KEY" -H "X-Tenant-ID: $DENSE_TENANT" \
  --data-urlencode limit=50 | python3 -m json.tool | head -40
```

A proporção de estruturados tem que parar de subir. Medir com o pipeline
atrasado mede um corpus a meio caminho.

## Medição

```bash
python3 rbench_dense.py compare recall lexical-pure
python3 rbench_dense.py compare recall lexical        # com vs sem rank cognitivo
```

Modos: `recall` (híbrido), `recall-pure` (híbrido sem rank cognitivo),
`lexical` (léxico + rank cognitivo), `lexical-pure` (só o braço léxico).

## O que este harness reporta, e por quê

Ele **nunca imprime uma diferença sem o intervalo de confiança dela**, e compara
de forma **pareada** (mesma consulta nas duas configurações).

Isso não é rigor decorativo. Sobre 30 consultas, diferenças de 0,02–0,04 de MRR
foram lidas como achado quando o IC atravessava zero de ponta a ponta — 7
consultas melhores contra 6 piores. Três decisões saíram disso, incluindo uma
reversão em produção e uma frase num paper. Duas execuções da mesma
configuração diferiam 0,002: a medição era estável, o **tamanho** é que não
enxergava.

Um IC que atravessa zero significa "este n não resolve esta diferença" —
**não** significa "as configurações são iguais".

Falha de transporte devolve `-1` e a consulta é **excluída**, nunca contada
como miss. Uma pergunta não medida não é uma pergunta errada.
