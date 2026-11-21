package client

// envelope_only_test.go — one domain: proving the 4.0.0 removal of the
// bare-array response fallbacks on the listing and entity reads. The server
// always answers these routes with an object envelope, so a bare JSON array is
// now a decode error, while the two shapes the server really sends for "no
// rows" — "records": null and an empty envelope — still decode to an empty
// slice. (ListSessions and Recent have the same proof in list_sessions_test.go
// and add_options_test.go.)
//
// Server truth: global manifest — server/handler/search_aux.go:224; session
// manifest — server/handler/record_session.go:305-311; chat listing —
// record_session.go:184; recent — record_session.go:455; entities search —
// server/handler/entity.go:254-257; record entities — entity.go:563-567.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestBareArrayIsRejectedOnEveryEnvelopeRoute(t *testing.T) {
	const bareArray = `[{"id":7,"name":"n"}]`
	testCases := []struct {
		name      string
		call      func(memory *Memory) (int, error)
		wantError string
	}{
		{
			name: "ManifestGlobal",
			call: func(memory *Memory) (int, error) {
				page, callErr := memory.ManifestGlobal(context.Background(), "", 10, 0)
				if page == nil {
					return 0, callErr
				}
				return len(page.Records), callErr
			},
			wantError: "parsing global manifest response",
		},
		{
			name: "ManifestSession",
			call: func(memory *Memory) (int, error) {
				page, callErr := memory.ManifestSession(context.Background(), "s1", "", 10, 0)
				if page == nil {
					return 0, callErr
				}
				return len(page.Records), callErr
			},
			wantError: "parsing session manifest response",
		},
		{
			name: "ListChat",
			call: func(memory *Memory) (int, error) {
				records, callErr := memory.ListChat(context.Background(), "s1", nil, "")
				return len(records), callErr
			},
			wantError: "parsing list chat response",
		},
		{
			name: "SearchEntities",
			call: func(memory *Memory) (int, error) {
				entities, callErr := memory.SearchEntities(context.Background(), "q", "", 10)
				return len(entities), callErr
			},
			wantError: "parsing entities response",
		},
		{
			name: "GetRecordEntities",
			call: func(memory *Memory) (int, error) {
				entities, callErr := memory.GetRecordEntities(context.Background(), 7)
				return len(entities), callErr
			},
			wantError: "parsing record entities response",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			rowCount, callErr := testCase.call(fixedBodyMemory(t, bareArray))
			if callErr == nil {
				t.Fatalf("a bare array must not decode any more, got %d rows", rowCount)
			}
			if !strings.Contains(callErr.Error(), testCase.wantError) {
				t.Fatalf("want %q in the error, got: %v", testCase.wantError, callErr)
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
