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
	"fmt"
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
func (operator QueryOp) isEmpty() bool {
	return operator.Eq == nil && operator.Gt == nil && operator.Gte == nil &&
		operator.Lt == nil && operator.Lte == nil && operator.In == nil
}

// hasEmptyInList reports the "$in with an empty array" case specifically.
func (operator QueryOp) hasEmptyInList() bool {
	return operator.In != nil && len(operator.In) == 0
}

// Validate checks the request against the same rules Python and TypeScript
// enforce client-side. Query() calls it before spending a request; callers who
// build a QueryRequest by hand can call it directly.
//
// It returns the FIRST problem found, in a stable order (filters, then sort,
// then pagination), so the same malformed query always reports the same error.
func (request *QueryRequest) Validate() error {
	if request == nil {
		return fmt.Errorf("query: request is nil")
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
			return fmt.Errorf("query: field %q is not allowed in filters — allowed: %s",
				filterField, sortedAllowedColumns())
		}
		operator := request.Filters[filterField]
		if operator.hasEmptyInList() {
			return fmt.Errorf("query: filter %q: $in requires a non-empty list of values", filterField)
		}
		if operator.isEmpty() {
			return fmt.Errorf("query: filter %q has no operator: set one of Eq, Gt, Gte, Lt, Lte or In", filterField)
		}
	}

	for _, sortClause := range request.Sort {
		sortField := sortClause["field"]
		if !astAllowedFilterColumns[sortField] {
			return fmt.Errorf("query: sort field %q is not allowed — allowed: %s",
				sortField, sortedAllowedColumns())
		}
		// Junior Tip [ordem desconhecida cai em DESC no servidor, em silêncio]:
		// o servidor aceita a clausula e usa DESC. Recusar aqui é a mesma postura
		// de Python/TS e evita um "por que a ordem não mudou?" indepurável.
		if sortOrder, present := sortClause["order"]; present && !astAllowedSortOrders[strings.ToLower(sortOrder)] {
			return fmt.Errorf("query: sort order %q is not allowed — use \"asc\" or \"desc\"", sortOrder)
		}
	}

	if limitValue, present := request.Pagination["limit"]; present {
		if limitValue < 1 || limitValue > astQueryLimitMax {
			return fmt.Errorf("query: limit must be between 1 and %d, got %d", astQueryLimitMax, limitValue)
		}
	}
	if offsetValue, present := request.Pagination["offset"]; present && offsetValue < 0 {
		return fmt.Errorf("query: offset cannot be negative, got %d", offsetValue)
	}
	return nil
}
