package client

// search_semantic_timeout_test.go — the per-request semantic budget knob.
//
// Domain, in one sentence: prove that semantic_timeout_ms is refused when it is
// impossible and omitted when it is the sentinel.
//
// Junior Tip [why its own file, 2026-09-05]: search_adr0031_test.go covers the
// three ADR-0031 knobs together and was already at 267 lines against this
// project's ~300-line cut; house law forbids GROWING a file that would cross
// it, so the part being added went to a file of its own. The seam is real —
// everything here is about ONE knob's legal domain, which is the only knob of
// the three with a numeric range to get wrong.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNegativeSemanticTimeoutIsRefusedBeforeTheRoundTrip pins the 2026-09-05
// parity fix: a negative semantic budget must be REFUSED, not swallowed.
//
// Junior Tip [what this test would have caught]: WithSemanticTimeoutMs did no
// validation, and buildSearchPayload gated on `> 0`. A caller who passed -1
// (usually an unchecked subtraction of two deadlines) had the knob silently
// dropped: the request left without the field, the server applied its own
// 700ms default, and the caller believed they had capped the embedder. Python
// already raised INVALID_PARAM for the same input, so the same call behaved
// differently in two SDKs — the exact divergence ADR-0031 parity exists to
// remove. Asserting the exact message, not a substring, is what keeps the three
// wordings from drifting apart again.
func TestNegativeSemanticTimeoutIsRefusedBeforeTheRoundTrip(t *testing.T) {
	requestReached := false
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestReached = true
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		WithSemanticTimeoutMs(-1))
	if searchErr == nil {
		t.Fatal("Search accepted a negative semantic_timeout_ms; it must be refused before the request leaves")
	}
	if requestReached {
		t.Fatal("the request reached the server; validation must happen client-side")
	}

	const expectedMessage = "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0"
	if searchErr.Error() != expectedMessage {
		t.Fatalf("error=%q want %q", searchErr.Error(), expectedMessage)
	}

	apiError, isAPIError := searchErr.(*APIError)
	if !isAPIError {
		t.Fatal("a client-side rejection must still be a *APIError so callers can branch on Kind()")
	}
	if apiError.StatusCode != 400 || apiError.Kind() != KindInvalidRequest || apiError.Retryable() {
		t.Fatalf("status=%d kind=%q retryable=%v want 400/invalid_request/false",
			apiError.StatusCode, apiError.Kind(), apiError.Retryable())
	}
}

// TestZeroSemanticTimeoutStaysTheOmitSentinel proves the fix did not turn the
// legal "use the server default" sentinel into an error, and did not start
// writing a meaningless key on the wire.
func TestZeroSemanticTimeoutStaysTheOmitSentinel(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		_ = json.NewDecoder(httpRequest.Body).Decode(&capturedBody)
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		WithSemanticTimeoutMs(0)); searchErr != nil {
		t.Fatalf("Search returned error for the 0 sentinel: %v", searchErr)
	}
	if _, present := capturedBody["semantic_timeout_ms"]; present {
		t.Fatalf("payload carries semantic_timeout_ms=%#v; 0 means 'use the server default' and must be omitted",
			capturedBody["semantic_timeout_ms"])
	}
}
