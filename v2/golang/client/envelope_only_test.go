package client

// envelope_only_test.go — one domain: proving the 4.0.0 envelope rule
// (envelope.go) on all eight listing routes: anything that is not a JSON object
// carrying the route's list key is a typed *UnexpectedResponseError, while a
// present key with a null value (the server's zero-row answer) is empty.
//
// Server truth: global manifest — server/handler/search_aux.go:224; session
// manifest — server/handler/record_session.go:305-311; chat listing —
// record_session.go:184; recent — record_session.go:455; entities search —
// server/handler/entity.go:254-257; record entities — entity.go:563-567.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixedBodyMemory returns a Memory whose every request is answered 200 with body.
func fixedBodyMemory(t *testing.T, body string) *Memory {
	t.Helper()
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(responseWriter, body)
	}))
	t.Cleanup(stubServer.Close)
	return NewMemory("k", WithURL(stubServer.URL))
}

// envelopeRouteCall runs one of the eight envelope routes against memory and
// returns only its error.
type envelopeRouteCall struct {
	route string
	key   string
	call  func(memory *Memory) error
}

// everyEnvelopeRoute is the cross-SDK list (Python tests/test_envelope_rule.py,
// TypeScript src/test/envelope_rule.test.ts cover the same eight).
func everyEnvelopeRoute() []envelopeRouteCall {
	backgroundContext := context.Background()
	return []envelopeRouteCall{
		{"/api/v1/sessions/stats", "sessions", func(memory *Memory) error {
			_, callErr := memory.ListSessions(backgroundContext)
			return callErr
		}},
		{"/api/v1/recent", "records", func(memory *Memory) error {
			_, callErr := memory.Recent(backgroundContext, 10)
			return callErr
		}},
		{"/api/v1/manifest", "records", func(memory *Memory) error {
			_, callErr := memory.ManifestGlobal(backgroundContext, "", 10, 0)
			return callErr
		}},
		{"/api/v1/chats/{uuid}/manifest", "records", func(memory *Memory) error {
			_, callErr := memory.ManifestSession(backgroundContext, "s1", "", 10, 0)
			return callErr
		}},
		{"/api/v1/chats/{uuid}", "records", func(memory *Memory) error {
			_, callErr := memory.ListChat(backgroundContext, "s1", nil, "")
			return callErr
		}},
		{"/api/v1/entities", "entities", func(memory *Memory) error {
			_, callErr := memory.SearchEntities(backgroundContext, "q", "", 10)
			return callErr
		}},
		{"/api/v1/entities/list", "entities", func(memory *Memory) error {
			_, callErr := memory.ListEntities(backgroundContext, 10, 0)
			return callErr
		}},
		{"/api/v1/records/{id}/entities", "entities", func(memory *Memory) error {
			_, callErr := memory.GetRecordEntities(backgroundContext, 7)
			return callErr
		}},
	}
}

// TestNonEnvelopeIsUnexpectedResponseOnEveryRoute proves the 4.0.0 rule: a
// bare array, an empty body, an object without the key, or a non-list value
// is a typed *UnexpectedResponseError of kind "server" with the cross-SDK
// message — never "no rows".
func TestNonEnvelopeIsUnexpectedResponseOnEveryRoute(t *testing.T) {
	badBodies := map[string]string{
		"bare array":   `[{"id":7,"name":"n"}]`,
		"empty body":   ``,
		"json null":    `null`,
		"missing key":  `{"count":0}`,
		"non-list key": `{"records":{},"sessions":{},"entities":{}}`,
	}
	for _, route := range everyEnvelopeRoute() {
		for bodyName, body := range badBodies {
			t.Run(route.route+"/"+bodyName, func(t *testing.T) {
				callErr := route.call(fixedBodyMemory(t, body))
				var shapeError *UnexpectedResponseError
				if !errors.As(callErr, &shapeError) || !errors.Is(callErr, ErrUnexpectedResponse) {
					t.Fatalf("want *UnexpectedResponseError, got %T: %v", callErr, callErr)
				}
				wantMessage := "unexpected response from GET " + route.route + ": expected a JSON object with \"" + route.key + "\""
				if callErr.Error() != wantMessage {
					t.Fatalf("Error() = %q, want %q", callErr.Error(), wantMessage)
				}
				if shapeError.Kind() != KindServer {
					t.Fatalf("Kind() = %s, want server", shapeError.Kind())
				}
			})
		}
	}
}

// TestNullListIsEmptyOnEveryRoute pins what was KEPT: a present key whose
// value is null (the server's zero-row answer) is an empty result.
func TestNullListIsEmptyOnEveryRoute(t *testing.T) {
	nullBody := `{"records":null,"sessions":null,"entities":null,"count":0,"has_more":false}`
	for _, route := range everyEnvelopeRoute() {
		t.Run(route.route, func(t *testing.T) {
			if callErr := route.call(fixedBodyMemory(t, nullBody)); callErr != nil {
				t.Fatalf("null list must be empty, got %v", callErr)
			}
		})
	}
}

// TestNullRecordsStillDecodeEmpty pins what was KEPT: the server emits
// "records": null for zero hits, and that is an empty slice, not an error.
func TestNullRecordsStillDecodeEmpty(t *testing.T) {
	memory := fixedBodyMemory(t, `{"records":null,"count":0,"limit":10,"offset":0,"has_more":false}`)

	page, manifestErr := memory.ManifestGlobal(context.Background(), "", 10, 0)
	if manifestErr != nil || page.Records == nil || len(page.Records) != 0 {
		t.Fatalf("manifest: want empty non-nil records, got %+v err=%v", page, manifestErr)
	}
	recentRecords, recentErr := memory.Recent(context.Background(), 10)
	if recentErr != nil || recentRecords == nil || len(recentRecords) != 0 {
		t.Fatalf("recent: want empty non-nil slice, got %v err=%v", recentRecords, recentErr)
	}
	chatRecords, chatErr := memory.ListChat(context.Background(), "s1", nil, "")
	if chatErr != nil || chatRecords == nil || len(chatRecords) != 0 {
		t.Fatalf("list chat: want empty non-nil slice, got %v err=%v", chatRecords, chatErr)
	}
}
