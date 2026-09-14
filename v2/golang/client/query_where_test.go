package client

// query_where_test.go — proof that B3 (Where silently discarding a predicate)
// stays dead.
//
// Junior Tip [why the main test captures the WIRE, 2026-09-14]: B3 was invisible
// to every in-memory assertion path a fixture could mirror — the builder
// compiled, Validate passed, the server answered 200. The only place the defect
// existed was the REQUEST BODY, where `{"score":{"$gte":3}}` had vanished. A
// test that inspects request.Filters would still pass if some future encode step
// re-introduced the loss, so the assertion below reads the bytes an httptest
// server actually received — the same evidence the differential harness used to
// catch the bug against production.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWhereMergesOperatorsOnTheSameColumnOnTheWire chains two Where calls on
// one column and asserts BOTH operators reach the request body. Before the
// 2026-09-14 fix the second call replaced the first: only {"$lte":5} went out,
// the server answered 200, and Go returned 5 rows where TypeScript and Python
// returned 3.
func TestWhereMergesOperatorsOnTheSameColumnOnTheWire(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		bodyBytes, readErr := io.ReadAll(httpRequest.Body)
		if readErr != nil {
			t.Errorf("reading request body: %v", readErr)
		}
		capturedBody = bodyBytes
		io.WriteString(responseWriter, `{"records":[],"count":0}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("test-key", WithURL(server.URL))
	rangeQuery := NewQuery().
		Where("score", QueryOp{Gte: 3}).
		Where("score", QueryOp{Lte: 5})
	if _, queryErr := memoryClient.Query(context.Background(), rangeQuery); queryErr != nil {
		t.Fatalf("Query returned error for a legal range: %v", queryErr)
	}

	var decodedBody struct {
		Filters map[string]map[string]interface{} `json:"filters"`
	}
	if decodeErr := json.Unmarshal(capturedBody, &decodedBody); decodeErr != nil {
		t.Fatalf("request body is not the expected JSON shape: %v\nbody: %s", decodeErr, capturedBody)
	}
	scoreOperators, scorePresent := decodedBody.Filters["score"]
	if !scorePresent {
		t.Fatalf("no score filter reached the wire at all\nbody: %s", capturedBody)
	}
	if _, gtePresent := scoreOperators["$gte"]; !gtePresent {
		t.Fatalf("the $gte predicate vanished from the wire — this is exactly bug B3 "+
			"(second Where replaced the first; 5 rows instead of 3, HTTP 200, no error)\nbody: %s", capturedBody)
	}
	if _, ltePresent := scoreOperators["$lte"]; !ltePresent {
		t.Fatalf("the $lte predicate vanished from the wire\nbody: %s", capturedBody)
	}
}

// TestWhereRefusesTheSameOperatorTwiceOnOneColumn pins the behaviour this SDK
// chose for the case the merge cannot honour: two values for ONE operator slot.
// TypeScript and Python silently overwrite last-wins there (see the dated
// 2026-09-14 entry in PARITY_SPEC.md); Go refuses loudly with a typed
// invalid_request error, because silently keeping either value is the same
// defect class as B3 itself.
func TestWhereRefusesTheSameOperatorTwiceOnOneColumn(t *testing.T) {
	testCases := []struct {
		name    string
		request *QueryRequest
	}{
		{"dois $gte via Where", NewQuery().Where("score", QueryOp{Gte: 3}).Where("score", QueryOp{Gte: 4})},
		{"dois $eq via WhereEquals", NewQuery().WhereEquals("type", "fact").WhereEquals("type", "decision")},
		{"dois $in", NewQuery().
			Where("type", QueryOp{In: []interface{}{"fact"}}).
			Where("type", QueryOp{In: []interface{}{"decision"}})},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			validationErr := testCase.request.Validate()
			if validationErr == nil {
				t.Fatal("duplicate operator accepted in silence — one of the two values was discarded")
			}
			if !contains(validationErr.Error(), "is already set for this column") {
				t.Fatalf("wrong refusal message: %q", validationErr.Error())
			}
			var apiErr *APIError
			if !errors.As(validationErr, &apiErr) {
				t.Fatalf("duplicate-operator refusal is not a typed *APIError: %T", validationErr)
			}
			if apiErr.Kind() != KindInvalidRequest || apiErr.Retryable() {
				t.Fatalf("want Kind=invalid_request retryable=false, got Kind=%s retryable=%v",
					apiErr.Kind(), apiErr.Retryable())
			}
		})
	}
}

// TestWhereMergeAcrossDistinctOperatorsStaysValid guards the merge itself in
// memory (the wire test above guards the encoding): distinct operators on one
// column accumulate, and the merged request still passes Validate.
func TestWhereMergeAcrossDistinctOperatorsStaysValid(t *testing.T) {
	mergedRequest := NewQuery().
		Where("score", QueryOp{Gte: 3}).
		Where("score", QueryOp{Lte: 5}).
		Where("type", QueryOp{Eq: "fact"})
	if validationErr := mergedRequest.Validate(); validationErr != nil {
		t.Fatalf("legal range refused: %v", validationErr)
	}
	scoreOperator := mergedRequest.Filters["score"]
	if scoreOperator.Gte == nil || scoreOperator.Lte == nil {
		t.Fatalf("merge lost an operator: %+v", scoreOperator)
	}
}
