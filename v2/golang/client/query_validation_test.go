package client

// query_validation_test.go — a validação do builder Go tem de recusar o MESMO
// conjunto que Python (query/builder.py) e TypeScript (src/query.ts) recusam.

import "testing"

func TestQueryValidate_RejectsWhatTheOtherSDKsReject(t *testing.T) {
	testCases := []struct {
		name    string
		request *QueryRequest
		wantErr string
	}{
		{"campo fora da whitelist", NewQuery().Where("nao_existe", QueryOp{Eq: 1}), "not allowed in filters"},
		{"consolidate_id nao e filtravel", NewQuery().Where("consolidate_id", QueryOp{Eq: 1}), "not allowed in filters"},
		{"operador ausente (QueryOp zero)", NewQuery().Where("type", QueryOp{}), "has no operator"},
		{"$in vazio", NewQuery().Where("type", QueryOp{In: []interface{}{}}), "$in requires a non-empty"},
		{"limit zero", NewQuery().Where("type", QueryOp{Eq: "fact"}).Limit(0), "limit must be between"},
		{"limit acima do teto", NewQuery().Where("type", QueryOp{Eq: "fact"}).Limit(5000), "limit must be between"},
		{"offset negativo", NewQuery().Where("type", QueryOp{Eq: "fact"}).Offset(-3), "offset cannot be negative"},
		{"campo de sort invalido", NewQuery().Where("type", QueryOp{Eq: "fact"}).OrderBy("inventado", "desc"), "sort field"},
		{"ordem invalida", NewQuery().Where("type", QueryOp{Eq: "fact"}).OrderBy("created_at", "cima"), "sort order"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			validationErr := testCase.request.Validate()
			if validationErr == nil {
				t.Fatalf("aceito em silencio — o servidor responderia 400 (ou pior, listagem sem filtro)")
			}
			if !contains(validationErr.Error(), testCase.wantErr) {
				t.Fatalf("mensagem %q nao contem %q", validationErr.Error(), testCase.wantErr)
			}
		})
	}
}

// TestQueryValidate_AcceptsTheValidShapes garante que a validacao nao virou um
// muro: uma consulta legitima tem de passar.
func TestQueryValidate_AcceptsTheValidShapes(t *testing.T) {
	valid := []*QueryRequest{
		NewQuery(),
		NewQuery().Where("type", QueryOp{Eq: "fact"}),
		NewQuery().Where("score", QueryOp{Gte: 7}).OrderBy("created_at", "DESC").Limit(20).Offset(0),
		NewQuery().Where("uuid", QueryOp{In: []interface{}{"a", "b"}}).Limit(1000),
	}
	for index, request := range valid {
		if validationErr := request.Validate(); validationErr != nil {
			t.Fatalf("caso %d: consulta valida recusada: %v", index, validationErr)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if haystack[start:start+len(needle)] == needle {
			return start
		}
	}
	return -1
}
