package client

// query_validation.go — client-side validation for the AST query builder,
// bringing the Go SDK to parity with Python and TypeScript.
//
// Junior Tip [por que isto existe, e por que só agora]: Python
// (anhurdb/query/builder.py) e TypeScript (src/query.ts) sempre validaram campo,
// operador e paginação NO CLIENTE, com a mesma whitelist do servidor. O Go não
// validava nada: Where() aceitava qualquer string. Enquanto o servidor
// DESCARTAVA predicado inválido em silêncio, a assimetria era invisível — os
// três "funcionavam", e o Go apenas devolvia uma listagem sem filtro em vez de
// um erro. Em 2026-07-28 o servidor passou a recusar com 400 nomeado, e a
// assimetria virou visível: o mesmo engano vira erro local em dois SDKs e ida
// e volta na rede no terceiro.
//
// Junior Tip [por que acumular erro em vez de retorná-lo]: o builder do Go é
// fluente (cada método devolve o receiver para encadear), então mudar as
// assinaturas para (…, error) quebraria toda cadeia existente. O padrão idiomático
// para builder fluente é acumular e entregar na execução — é o que Query() faz,
// chamando Validate() antes de gastar uma requisição. Quem monta o QueryRequest
// à mão pode chamar Validate() diretamente.

import (
	"reflect"
	"sort"
	"strings"
)

// astAllowedFilterColumns espelha astQueryAllowedFilterColumns do servidor
// (AnhurDB/server/service/record_ast_query.go). Os três SDKs carregam a mesma
// lista porque nenhum deles importa o servidor.
//
// Junior Tip [cópia manual, e o que a trava]: se o servidor ganhar uma coluna,
// esta lista não quebra sozinha — o sintoma é um 400 "invalid filter field" que
// o usuário não consegue explicar. O teste de paridade compara as três listas
// entre si; a divergência com o SERVIDOR só aparece em integração.
var astAllowedFilterColumns = map[string]bool{
	"id": true, "uuid": true, "type": true, "dimension": true,
	"weight": true, "score": true, "status": true, "consolidated": true,
	"archived": true, "created_at": true, "updated_at": true,
	"prefix": true, "metadata": true, "summary": true,
	"superseded_by": true, "valid_from": true, "valid_until": true,
}

// astAllowedSortOrders são as direções que o servidor reconhece.
var astAllowedSortOrders = map[string]bool{"asc": true, "desc": true}

// astQueryLimitMax é o teto que o servidor aplica (record_ast_query.go).
const astQueryLimitMax = 1000

// sortedAllowedColumns rende a whitelist em ordem estável para a mensagem de
// erro — lista embaralhada a cada execução é ruído para quem está depurando.
func sortedAllowedColumns() string {
	columnNames := make([]string, 0, len(astAllowedFilterColumns))
	for columnName := range astAllowedFilterColumns {
		columnNames = append(columnNames, columnName)
	}
	sort.Strings(columnNames)
	return strings.Join(columnNames, ", ")
}

// isEmpty reports whether the operator object carries no operator at all.
//
// Junior Tip [o caso que mais dói no Go]: todos os campos de QueryOp são
// `omitempty`, então um QueryOp{} zero serializa como `{}` — e desde
// 2026-07-28 o servidor responde 400 "filter has no operator". Pior: um
// QueryOp{In: []interface{}{}} também vira `{}`, e o usuário recebe a mensagem
// de "sem operador" quando o problema real era a lista vazia. Detectar aqui
// devolve a mensagem CERTA antes de a requisição sair.
//
// Junior Tip [QueryOp{Eq: nil} não é uma lacuna do Go, 2026-09-14]: um nil
// interface é indistinguível de "campo não preenchido", então o Go não
// consegue emitir `$eq: null` — e isso está CERTO. Medido ao vivo no router de
// produção em 2026-09-14 (somente leitura): `{"superseded_by":{"$eq":null}}`
// devolveu HTTP 200 com count=0 num tenant cuja página sem filtro devolve 1000
// registros, e TODOS eles satisfazem `superseded_by IS NULL`. O servidor
// compila `$eq: null` para `col = ?` com NULL ligado, e em SQL `col = NULL`
// nunca é verdadeiro. Python e TypeScript conseguem mandar esse predicado; o
// que eles ganham é a capacidade de escrever uma consulta morta que responde
// 200. Ver o adendo datado em CHANGELOG.md e PARITY_SPEC.md.
func (operator QueryOp) isEmpty() bool {
	return operator.Eq == nil && operator.Gt == nil && operator.Gte == nil &&
		operator.Lt == nil && operator.Lte == nil && operator.In == nil
}

// hasEmptyInList reports the "$in with an empty array" case specifically.
func (operator QueryOp) hasEmptyInList() bool {
	return operator.In != nil && len(operator.In) == 0
}

// validateInElements refuses $in elements the grammar cannot honour: nil, and
// anything that is not a JSON scalar (nested arrays, objects, structs, …).
//
// Junior Tip [B1 — why elements are inspected at all, 2026-09-14]: the length
// check above was the ONLY inspection this list ever got, so
// `{"type":{"$in":["fact",null]}}` left the process, the server answered HTTP
// 200 and silently DROPPED the null (measured live against production on
// 2026-09-14 by the three-SDK differential harness) — the caller ran a
// narrower predicate than the one they wrote, with no error anywhere. The two
// refusals here have DIFFERENT server behaviours behind them, and each message
// says which: a null element is silently ignored (SQL IN never matches NULL);
// a non-scalar element is answered with HTTP 400. Python (_assert_in_list) and
// TypeScript (assertFilterValue) already refuse a null element client-side —
// this closes the Go gap.
func validateInElements(filterField string, operator QueryOp) error {
	for elementIndex, elementValue := range operator.In {
		if elementValue == nil {
			return newValidationError(
				"query: filter %q: $in[%d] is null — the server does not reject it, it silently DROPS the null "+
					"and matches only the remaining values (SQL IN never matches NULL), so the query runs narrower "+
					"than written with HTTP 200; remove the null element or compare against a real value",
				filterField, elementIndex)
		}
		if !isScalarFilterValue(elementValue) {
			return newValidationError(
				"query: filter %q: $in[%d] is a %T — $in elements must be scalars (bool, number or string); "+
					"the server answers HTTP 400 for a nested array or object",
				filterField, elementIndex, elementValue)
		}
	}
	return nil
}

// isScalarFilterValue reports whether a value marshals to a JSON scalar the
// server grammar accepts (bool, any numeric kind, or string). Everything else
// — slices, arrays, maps, structs, pointers — is refused by validateInElements.
func isScalarFilterValue(value interface{}) bool {
	switch reflect.ValueOf(value).Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
}

// Validate checks the request against the same rules Python and TypeScript
// enforce client-side. Query() calls it before spending a request; callers who
// build a QueryRequest by hand can call it directly.
//
// It returns the FIRST problem found, in a stable order (filters, then sort,
// then pagination), so the same malformed query always reports the same error.
func (request *QueryRequest) Validate() error {
	if request == nil {
		return newValidationError("query: request is nil")
	}
	if len(request.buildErrors) > 0 {
		return request.buildErrors[0]
	}

	filterFieldNames := make([]string, 0, len(request.Filters))
	for filterField := range request.Filters {
		filterFieldNames = append(filterFieldNames, filterField)
	}
	sort.Strings(filterFieldNames)

	for _, filterField := range filterFieldNames {
		if !astAllowedFilterColumns[filterField] {
			return newValidationError("query: field %q is not allowed in filters — allowed: %s",
				filterField, sortedAllowedColumns())
		}
		operator := request.Filters[filterField]
		if operator.hasEmptyInList() {
			return newValidationError("query: filter %q: $in requires a non-empty list of values", filterField)
		}
		if inElementErr := validateInElements(filterField, operator); inElementErr != nil {
			return inElementErr
		}
		if operator.isEmpty() {
			// Junior Tip [name the CAUSE, not the symptom, 2026-09-14]: the old text
			// read "has no operator: set one of Eq, Gt, ..." — and the single most
			// common way to reach it is QueryOp{Eq: nil}, where the caller DID set
			// Eq. Telling that caller to set Eq sends them back to code that already
			// does what the error asks. The operator did not go missing, it was
			// ERASED by `omitempty` during encoding, and the message has to say so or
			// it is worse than no message at all.
			return newValidationError("query: filter %q: no operator survived encoding — every QueryOp field is `omitempty`, "+
				"so QueryOp{Eq: nil} marshals to {} exactly like QueryOp{}; set a NON-NIL Eq, Gt, Gte, Lt, Lte or In. "+
				"A literal $eq:null is unreachable from Go on purpose: the server compiles it to `col = NULL`, "+
				"which is never true, so it would return 200 with zero rows for every input", filterField)
		}
	}

	for _, sortClause := range request.Sort {
		sortField := sortClause["field"]
		if !astAllowedFilterColumns[sortField] {
			return newValidationError("query: sort field %q is not allowed — allowed: %s",
				sortField, sortedAllowedColumns())
		}
		// Junior Tip [ordem desconhecida cai em DESC no servidor, em silêncio]:
		// o servidor aceita a clausula e usa DESC. Recusar aqui é a mesma postura
		// de Python/TS e evita um "por que a ordem não mudou?" indepurável.
		if sortOrder, present := sortClause["order"]; present && !astAllowedSortOrders[strings.ToLower(sortOrder)] {
			return newValidationError("query: sort order %q is not allowed — use \"asc\" or \"desc\"", sortOrder)
		}
	}

	if limitValue, present := request.Pagination["limit"]; present {
		if limitValue < 1 || limitValue > astQueryLimitMax {
			return newValidationError("query: limit must be between 1 and %d, got %d", astQueryLimitMax, limitValue)
		}
	}
	if offsetValue, present := request.Pagination["offset"]; present && offsetValue < 0 {
		return newValidationError("query: offset cannot be negative, got %d", offsetValue)
	}
	return nil
}
