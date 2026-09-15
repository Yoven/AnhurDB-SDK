package client_test

// query_typed_errors_test.go — proof that B2 (client-side query rejections
// carrying no Kind/Retryable) stays dead, written from OUTSIDE the package,
// exactly the way a caller writes it.
//
// Junior Tip [why an external test package, 2026-09-14]: the defect was not
// "the error text is wrong" — the text was fine. The defect was that
// `errors.As(err, &apiErr)` came back FALSE for every rejection Validate()
// produced, because they were bare fmt.Errorf values, so a caller's retry
// policy (branch on Kind/Retryable, never on strings) fell through to its
// unknown-error path. Only a test that imports the package like a consumer and
// runs that exact errors.As dance can pin the contract; an internal test could
// accidentally rely on unexported details a caller never sees.

import (
	"errors"
	"testing"

	client "github.com/Yoven/AnhurDB-SDK/v2/golang/v3/client"
)

// TestQueryBuilderRejectionsAreTypedInvalidRequest runs one representative of
// every client-side rejection family through the caller's own error-handling
// idiom and asserts the triple the SDK promises: StatusCode 400,
// Kind "invalid_request", Retryable false.
func TestQueryBuilderRejectionsAreTypedInvalidRequest(t *testing.T) {
	testCases := []struct {
		name    string
		request *client.QueryRequest
	}{
		{"campo fora da whitelist (via builder)", client.NewQuery().Where("nao_existe", client.QueryOp{Eq: 1})},
		{"operador ausente (QueryOp zero)", client.NewQuery().Where("type", client.QueryOp{})},
		{"$in vazio", client.NewQuery().Where("type", client.QueryOp{In: []interface{}{}})},
		{"$in com null (B1)", client.NewQuery().Where("type", client.QueryOp{In: []interface{}{nil}})},
		{"limit fora de faixa", client.NewQuery().Limit(5000)},
		{"offset negativo", client.NewQuery().Offset(-1)},
		{"sort invalido", client.NewQuery().OrderBy("inventado", "desc")},
		{"operador duplicado (B3)", client.NewQuery().
			Where("score", client.QueryOp{Gte: 3}).
			Where("score", client.QueryOp{Gte: 4})},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			validationErr := testCase.request.Validate()
			if validationErr == nil {
				t.Fatal("expected a rejection, got nil")
			}
			// This block IS the contract: it is what a caller's retry policy does.
			var apiErr *client.APIError
			if !errors.As(validationErr, &apiErr) {
				t.Fatalf("errors.As found no *client.APIError — the rejection regressed to a bare "+
					"fmt.Errorf with no status, which is bug B2; got %T: %v", validationErr, validationErr)
			}
			if apiErr.StatusCode != 400 {
				t.Fatalf("StatusCode = %d, want 400 (the status this request WOULD have come back with)", apiErr.StatusCode)
			}
			if apiErr.Kind() != client.KindInvalidRequest {
				t.Fatalf("Kind() = %q, want %q", apiErr.Kind(), client.KindInvalidRequest)
			}
			if apiErr.Retryable() {
				t.Fatal("Retryable() = true for an invalid request — a retry loop would spin on a permanent error")
			}
		})
	}
}
