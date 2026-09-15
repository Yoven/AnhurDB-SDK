package client

// query_execute_test.go — the two 2026-09-14 divergences closed on the AST
// execute path: Memory.Query no longer accepts read options it throws away,
// and the empty-operator error now names the cause instead of the symptom.
//
// Junior Tip [why a reflection test for a deleted parameter]: the bug was a
// variadic that COMPILED and did nothing. Deleting it makes the compiler the
// guard for callers — but nothing stops a future edit from adding
// `opts ...ReadOption` back with another `_ = opts`, and no ordinary test would
// notice, because the symptom of that bug is silence. Asserting the method's
// arity and non-variadicity is the only check that fails the moment the shape
// regresses.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v3/models"
)

// TestQueryAcceptsNoReadOptions pins the signature of Memory.Query.
//
// Until 2026-09-14 it read `Query(ctx, request, opts ...ReadOption)` with a bare
// `_ = opts` in the body: WithAsOf/WithSince/WithUntil/WithKeyword were accepted
// and discarded, so a point-in-time query silently ran unscoped. POST
// /api/v1/query has no temporal or keyword surface at all (probed live against
// the production router on 2026-09-14: `?as_of`, `?since`, `?until` and `?q`
// changed nothing, and `as_of` as a top-level body key is ignored), so the
// honest fix is to make the option uncompilable.
func TestQueryAcceptsNoReadOptions(t *testing.T) {
	methodType := reflect.TypeOf((*Memory).Query)

	if methodType.IsVariadic() {
		t.Fatalf("Memory.Query is variadic again — a ...ReadOption tail on this method " +
			"is accepted-and-discarded by definition, because the endpoint has no temporal surface")
	}
	// receiver + ctx + request
	if methodType.NumIn() != 3 {
		argumentNames := make([]string, 0, methodType.NumIn())
		for argumentIndex := 0; argumentIndex < methodType.NumIn(); argumentIndex++ {
			argumentNames = append(argumentNames, methodType.In(argumentIndex).String())
		}
		t.Fatalf("Memory.Query takes %d arguments (%s) — want exactly 3 "+
			"(*Memory, context.Context, *QueryRequest)", methodType.NumIn(), strings.Join(argumentNames, ", "))
	}
	if methodType.In(1) != reflect.TypeOf((*context.Context)(nil)).Elem() {
		t.Fatalf("Memory.Query arg 1 is %s — want context.Context", methodType.In(1))
	}
	if methodType.In(2) != reflect.TypeOf((*QueryRequest)(nil)) {
		t.Fatalf("Memory.Query arg 2 is %s — want *QueryRequest", methodType.In(2))
	}
	if methodType.NumOut() != 2 || methodType.Out(0) != reflect.TypeOf([]models.Record(nil)) {
		t.Fatalf("Memory.Query returns %d values, first is %s — want ([]models.Record, error)",
			methodType.NumOut(), methodType.Out(0))
	}
}

// TestQueryTemporalOptionsRemainOnTheRoutesThatHonourThem is the other half of
// the same claim: removing the variadic from Query must NOT have removed the
// temporal read options from the manifest routes, which really do carry them as
// query parameters.
func TestQueryTemporalOptionsRemainOnTheRoutesThatHonourThem(t *testing.T) {
	var capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedQuery = request.URL.RawQuery
		io.WriteString(responseWriter, `{"records":[],"total":0,"limit":10,"offset":0,"has_more":false}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, manifestErr := memoryClient.ManifestGlobal(context.Background(), "", 10, 0,
		WithAsOf("2026-03-15T12:00:00Z")); manifestErr != nil {
		t.Fatalf("ManifestGlobal returned error: %v", manifestErr)
	}
	if !strings.Contains(capturedQuery, "as_of=2026-03-15T12%3A00%3A00Z") {
		t.Fatalf("manifest query string %q lost as_of — the temporal options must stay wired "+
			"on the route that actually reads them", capturedQuery)
	}
}

// TestQueryStillReachesTheWireWithoutOptions proves the signature change did not
// break the happy path: a valid request is posted and its records decoded.
func TestQueryStillReachesTheWireWithoutOptions(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/query" {
			t.Fatalf("request hit %q want /api/v1/query", request.URL.Path)
		}
		rawBody, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("reading request body: %v", readErr)
		}
		if unmarshalErr := json.Unmarshal(rawBody, &capturedBody); unmarshalErr != nil {
			t.Fatalf("request body is not valid JSON: %v", unmarshalErr)
		}
		io.WriteString(responseWriter, `{"records":[{"id":7}],"count":1}`)
	}))
	defer server.Close()

	// A since/until window is expressed as what it is: a created_at filter. This
	// is the documented replacement for WithSince/WithUntil on this route.
	request := NewQuery().
		Where("created_at", QueryOp{Gte: "2026-01-01T00:00:00Z", Lte: "2026-02-01T00:00:00Z"}).
		Limit(20)

	records, queryErr := NewMemory("k", WithURL(server.URL)).Query(context.Background(), request)
	if queryErr != nil {
		t.Fatalf("Query returned error: %v", queryErr)
	}
	if len(records) != 1 || records[0].ID != 7 {
		t.Fatalf("decoded %#v want one record with id 7", records)
	}

	filters, hasFilters := capturedBody["filters"].(map[string]interface{})
	if !hasFilters {
		t.Fatalf("wire body %#v carries no filters object", capturedBody)
	}
	createdAt, hasCreatedAt := filters["created_at"].(map[string]interface{})
	if !hasCreatedAt || createdAt["$gte"] != "2026-01-01T00:00:00Z" || createdAt["$lte"] != "2026-02-01T00:00:00Z" {
		t.Fatalf("created_at window did not survive to the wire: %#v", filters["created_at"])
	}
}

// TestEmptyOperatorErrorNamesOmitemptyNotTheUser covers the reworded message.
//
// The old text said `has no operator: set one of Eq, Gt, ...`. The most common
// way to trigger it is QueryOp{Eq: nil} — a caller who DID set Eq, and who is
// then told to set Eq. The message has to name the encoder, not the caller.
func TestEmptyOperatorErrorNamesOmitemptyNotTheUser(t *testing.T) {
	testCases := []struct {
		name     string
		operator QueryOp
	}{
		{name: "zero value", operator: QueryOp{}},
		{name: "explicit nil Eq", operator: QueryOp{Eq: nil}},
		{name: "explicit nil Gte", operator: QueryOp{Gte: nil}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			validationErr := NewQuery().Where("type", testCase.operator).Validate()
			if validationErr == nil {
				subTest.Fatalf("accepted in silence — this encodes to {} and the server answers 400")
			}
			message := validationErr.Error()
			for _, requiredFragment := range []string{
				`filter "type"`,
				"no operator survived encoding",
				"omitempty",
				"QueryOp{Eq: nil}",
			} {
				if !strings.Contains(message, requiredFragment) {
					subTest.Fatalf("message %q does not name the cause — missing %q", message, requiredFragment)
				}
			}
			if strings.Contains(message, "set one of Eq, Gt, Gte, Lt, Lte or In") {
				subTest.Fatalf("message %q still tells a caller who set Eq to set Eq", message)
			}
		})
	}
}

// TestQueryOpNilFieldsVanishOnTheWire is the mechanical proof behind that
// message, and behind the corrected PARITY_SPEC entry: `omitempty` erases a nil
// interface, so QueryOp{Eq: nil} is byte-identical to QueryOp{} once encoded,
// and no QueryOp value can ever emit `"$eq":null`.
//
// Junior Tip [this is why $eq:null being unreachable from Go is CORRECT]: the
// server compiles `$eq: null` to `col = ?` bound to NULL, and `col = NULL` is
// never true in SQL. Probed live on 2026-09-14 against the production router:
// {"superseded_by":{"$eq":null}} answered HTTP 200 with count=0 on a tenant
// whose unfiltered page returns 1000 rows — every one of which satisfies
// superseded_by IS NULL. Python and TypeScript can send that predicate. What
// they have is not a capability, it is a way to write a query that can never
// match and never complains.
func TestQueryOpNilFieldsVanishOnTheWire(t *testing.T) {
	encodedZero, marshalZeroErr := json.Marshal(QueryOp{})
	if marshalZeroErr != nil {
		t.Fatalf("marshalling QueryOp{}: %v", marshalZeroErr)
	}
	encodedNilEq, marshalNilErr := json.Marshal(QueryOp{Eq: nil})
	if marshalNilErr != nil {
		t.Fatalf("marshalling QueryOp{Eq: nil}: %v", marshalNilErr)
	}

	if string(encodedZero) != "{}" {
		t.Fatalf("QueryOp{} encoded to %s want {}", encodedZero)
	}
	if string(encodedNilEq) != string(encodedZero) {
		t.Fatalf("QueryOp{Eq: nil} encoded to %s but QueryOp{} encoded to %s — if these ever "+
			"differ, the omitempty story in the error message and PARITY_SPEC is stale",
			encodedNilEq, encodedZero)
	}

	// No reachable QueryOp emits an explicit JSON null for any operator.
	encodedFull, marshalFullErr := json.Marshal(QueryOp{Eq: nil, Gt: nil, Gte: nil, Lt: nil, Lte: nil, In: nil})
	if marshalFullErr != nil {
		t.Fatalf("marshalling the all-nil QueryOp: %v", marshalFullErr)
	}
	if strings.Contains(string(encodedFull), "null") {
		t.Fatalf("an all-nil QueryOp emitted %s — a literal null operator is supposed to be "+
			"unreachable from Go", encodedFull)
	}
}
