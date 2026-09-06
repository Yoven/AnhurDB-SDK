package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// search_plane_pin_test.go — the plane wrappers answer from the plane their
// NAME promises, even when the caller also supplies WithScope.
//
// Junior Tip [why this test exists, 2026-09-06]: until this date the four
// wrappers PREPENDED their scope, so options applied later won and
// SearchTenantShared(ctx, q, s, WithScope("client_shared")) answered from
// client_shared — a method named for one plane serving another, invisible at
// the call site. Reading the source does not catch that class of bug: the
// three SDKs each "looked right" and still disagreed. Only the WIRE BODY is
// an honest witness, so this drives every wrapper against a real HTTP server
// and asserts on the JSON that actually left the process.

// planePinCase is one wrapper driven with and without a caller-supplied scope.
type planePinCase struct {
	wrapperName  string
	expectedWire string
	invoke       func(memoryClient *Memory, callerOptions ...SearchOption) error
}

// planePinCases enumerates all four plane wrappers. Kept table-driven so a
// fifth plane cannot be added without an entry here.
func planePinCases() []planePinCase {
	return []planePinCase{
		{"SearchSessions", "sessions", func(memoryClient *Memory, callerOptions ...SearchOption) error {
			_, searchErr := memoryClient.SearchSessions(context.Background(), "q", SessionsAll(), callerOptions...)
			return searchErr
		}},
		{"SearchTenantShared", "tenant_shared", func(memoryClient *Memory, callerOptions ...SearchOption) error {
			_, searchErr := memoryClient.SearchTenantShared(context.Background(), "q", SessionsAll(), callerOptions...)
			return searchErr
		}},
		{"SearchClientShared", "client_shared", func(memoryClient *Memory, callerOptions ...SearchOption) error {
			_, searchErr := memoryClient.SearchClientShared(context.Background(), "q", SessionsAll(), callerOptions...)
			return searchErr
		}},
		{"SearchShared", "shared_all", func(memoryClient *Memory, callerOptions ...SearchOption) error {
			_, searchErr := memoryClient.SearchShared(context.Background(), "q", SessionsAll(), callerOptions...)
			return searchErr
		}},
	}
}

// capturingSearchServer answers /api/v1/search with an empty result set and
// records the scope field of the request body it received.
func capturingSearchServer(capturedScope *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, readError := io.ReadAll(request.Body)
		if readError != nil {
			http.Error(responseWriter, readError.Error(), http.StatusBadRequest)
			return
		}
		var requestBody map[string]interface{}
		_ = json.Unmarshal(bodyBytes, &requestBody)
		if scopeValue, present := requestBody["scope"].(string); present {
			*capturedScope = scopeValue
		} else {
			*capturedScope = "<absent>"
		}
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
}

// TestPlaneWrapperPinsScopeOverCallerSuppliedScope is the divergence test: the
// wrapper wins, the caller's WithScope is silently overridden, and this holds
// for all four planes.
func TestPlaneWrapperPinsScopeOverCallerSuppliedScope(testingState *testing.T) {
	// A plane that is never the answer for any wrapper below, so a leak is
	// unambiguous rather than accidentally equal to the expected value.
	const intruderScope = "client_shared"

	for _, planeCase := range planePinCases() {
		testingState.Run(planeCase.wrapperName, func(subTest *testing.T) {
			for _, callerScope := range []string{"", intruderScope} {
				var capturedScope string
				server := capturingSearchServer(&capturedScope)
				defer server.Close()

				memoryClient := NewMemory("k", WithURL(server.URL))
				var callerOptions []SearchOption
				if callerScope != "" {
					callerOptions = append(callerOptions, WithScope(callerScope))
				}
				if invokeErr := planeCase.invoke(memoryClient, callerOptions...); invokeErr != nil {
					subTest.Fatalf("%s: %v", planeCase.wrapperName, invokeErr)
				}
				subTest.Logf("WIRE go | %-18s | caller_scope=%-14q | wire_scope=%q",
					planeCase.wrapperName, callerScope, capturedScope)
				if capturedScope != planeCase.expectedWire {
					subTest.Fatalf("%s with caller scope %q sent scope=%q, want the pinned %q",
						planeCase.wrapperName, callerScope, capturedScope, planeCase.expectedWire)
				}
			}
		})
	}
}

// TestPlaneWrapperPinDoesNotAliasCallerOptions proves the pin never writes into
// the CALLER's own option array.
//
// Junior Tip [why the caller passes a SUB-SLICE]: append(opts, extra) reuses the
// caller's backing array whenever it has spare capacity, and the damage is
// invisible while the caller's slice ends exactly where its length ends — the
// wrapper overwrites a cell nobody reads. It becomes real the moment the caller
// hands over a PREFIX of a longer slice, which is what building one option list
// and sending narrower views of it looks like: the wrapper's pin silently
// replaces the caller's next element. pinPlaneScope copies for exactly this
// reason, and this test is what stops a future "simplification" back to append.
func TestPlaneWrapperPinDoesNotAliasCallerOptions(testingState *testing.T) {
	callerOptions := make([]SearchOption, 0, 4)
	callerOptions = append(callerOptions, WithLimit(3), WithScope("client_shared"))

	var capturedScope string
	server := capturingSearchServer(&capturedScope)
	defer server.Close()
	memoryClient := NewMemory("k", WithURL(server.URL))

	// The caller sends only its FIRST option to the wrapper and keeps the rest.
	if _, searchErr := memoryClient.SearchTenantShared(context.Background(), "q", SessionsAll(), callerOptions[:1]...); searchErr != nil {
		testingState.Fatalf("SearchTenantShared: %v", searchErr)
	}
	if capturedScope != "tenant_shared" {
		testingState.Fatalf("wire scope=%q want tenant_shared", capturedScope)
	}

	survivingOption := applyReadOptions(callerOptions[1:2])
	testingState.Logf("WIRE go | caller kept option[1] after the call | scope=%q", survivingOption.scope)
	if survivingOption.scope != "client_shared" {
		testingState.Fatalf("the wrapper's pin overwrote the caller's own option[1]: scope=%q want client_shared", survivingOption.scope)
	}
}

// TestPlaneWrapperPinDoesNotEatOtherKnobs is the three-SDK body comparison:
// the same call must produce a byte-comparable JSON object in Go, Python and
// TypeScript, not merely the same scope.
//
// Junior Tip [why the WHOLE body and not just scope]: the last three
// divergences in this family were knobs that were accepted by a signature and
// then never reached the wire. Asserting only on the field under repair is how
// a fix passes while the neighbour silently regresses.
func TestPlaneWrapperPinDoesNotEatOtherKnobs(testingState *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.SearchTenantShared(context.Background(), "q", SessionsAll(),
		WithScope("client_shared"), WithLimit(7))
	if searchErr != nil {
		testingState.Fatalf("SearchTenantShared: %v", searchErr)
	}
	canonicalBody, _ := json.Marshal(capturedBody)
	testingState.Logf("WIRE go | SearchTenantShared + limit=7 | body=%s", canonicalBody)

	if capturedBody["scope"] != "tenant_shared" {
		testingState.Fatalf("scope=%v want tenant_shared", capturedBody["scope"])
	}
	if capturedBody["limit"] != float64(7) {
		testingState.Fatalf("limit=%v want 7 — the pin ate a neighbouring knob", capturedBody["limit"])
	}
	if capturedBody["text"] != "q" {
		testingState.Fatalf("text=%v want q", capturedBody["text"])
	}
}
