package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNormalizeSessionFilterContract pins ADR-0014 D1/D2 on the client side: the
// argument is mandatory, the wildcard stands alone, and neither an empty list
// nor an over-cap list is quietly repaired.
func TestNormalizeSessionFilterContract(testingState *testing.T) {
	overTheCap := make([]string, MaxSessionFilterUUIDs+1)
	for index := range overTheCap {
		overTheCap[index] = fmt.Sprintf("session-%d", index)
	}
	atTheCap := make([]string, MaxSessionFilterUUIDs)
	for index := range atTheCap {
		atTheCap[index] = fmt.Sprintf("session-%d", index)
	}

	testCases := []struct {
		name          string
		input         []string
		expectedWire  []string
		expectedError string
	}{
		{
			name:         "one session",
			input:        []string{"session-a"},
			expectedWire: []string{"session-a"},
		},
		{
			name:         "many sessions",
			input:        []string{"session-a", "session-b", "session-c"},
			expectedWire: []string{"session-a", "session-b", "session-c"},
		},
		{
			name:         "wildcard",
			input:        []string{SessionWildcard},
			expectedWire: []string{SessionWildcard},
		},
		{
			name:         "wildcard helper",
			input:        SessionsAll(),
			expectedWire: []string{"*"},
		},
		{
			name:         "surrounding whitespace is trimmed",
			input:        []string{"  session-a  "},
			expectedWire: []string{"session-a"},
		},
		{
			name:         "duplicates collapse",
			input:        []string{"session-a", "session-a", "session-b"},
			expectedWire: []string{"session-a", "session-b"},
		},
		{
			name:         "exactly at the cap is allowed",
			input:        atTheCap,
			expectedWire: atTheCap,
		},
		{
			name:          "absent",
			input:         nil,
			expectedError: `INVALID_PARAM: 'sessions' is required; use ["*"] for every session in scope`,
		},
		{
			name:          "empty list",
			input:         []string{},
			expectedError: `INVALID_PARAM: 'sessions' cannot be empty; use ["*"] for every session in scope`,
		},
		{
			name:          "empty entry",
			input:         []string{"session-a", "   "},
			expectedError: `INVALID_PARAM: 'sessions' contains an empty entry`,
		},
		{
			name:          "wildcard mixed with an explicit session",
			input:         []string{SessionWildcard, "session-a"},
			expectedError: `INVALID_PARAM: 'sessions' mixes "*" with 1 explicit session(s); the wildcard must stand alone`,
		},
		{
			name:  "above the cap",
			input: overTheCap,
			expectedError: fmt.Sprintf(
				`INVALID_PARAM: at most %d sessions per request (got %d); use ["*"] for all`,
				MaxSessionFilterUUIDs, MaxSessionFilterUUIDs+1),
		},
	}

	for _, testCase := range testCases {
		testingState.Run(testCase.name, func(subTest *testing.T) {
			resolvedSessions, normalizeErr := normalizeSessionFilter(testCase.input)

			if testCase.expectedError != "" {
				if normalizeErr == nil {
					subTest.Fatalf("expected error %q, got sessions=%v", testCase.expectedError, resolvedSessions)
				}
				if normalizeErr.Error() != testCase.expectedError {
					subTest.Fatalf("error=%q want %q", normalizeErr.Error(), testCase.expectedError)
				}
				if resolvedSessions != nil {
					subTest.Fatalf("rejected filter must not produce a wire value, got %v", resolvedSessions)
				}
				return
			}

			if normalizeErr != nil {
				subTest.Fatalf("unexpected error: %v", normalizeErr)
			}
			if len(resolvedSessions) != len(testCase.expectedWire) {
				subTest.Fatalf("sessions=%v want %v", resolvedSessions, testCase.expectedWire)
			}
			for index, expectedSession := range testCase.expectedWire {
				if resolvedSessions[index] != expectedSession {
					subTest.Fatalf("sessions[%d]=%q want %q", index, resolvedSessions[index], expectedSession)
				}
			}
		})
	}
}

// TestSearchSendsSessionsOnTheWire proves the mandatory filter reaches the body
// of POST /api/v1/search and that the retired singular `uuid` is gone.
func TestSearchSendsSessionsOnTheWire(testingState *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.Search(context.Background(), "hello", []string{"session-a", "session-b"})
	if searchErr != nil {
		testingState.Fatalf("Search: %v", searchErr)
	}

	sentSessions, isSlice := capturedBody["sessions"].([]interface{})
	if !isSlice {
		testingState.Fatalf("sessions=%v is not a JSON array", capturedBody["sessions"])
	}
	if len(sentSessions) != 2 || sentSessions[0] != "session-a" || sentSessions[1] != "session-b" {
		testingState.Fatalf("sessions=%v want [session-a session-b]", sentSessions)
	}
	if _, legacyPresent := capturedBody["uuid"]; legacyPresent {
		testingState.Fatalf("body still carries the retired singular 'uuid': %v", capturedBody)
	}
}

// TestSearchSessionSendsSingletonFilter proves the one-chat helper is expressed
// in the same grammar (sessions:[uuid]) instead of the old scalar uuid.
func TestSearchSessionSendsSingletonFilter(testingState *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.SearchSession(context.Background(), "conv-42", "hello")
	if searchErr != nil {
		testingState.Fatalf("SearchSession: %v", searchErr)
	}

	sentSessions, isSlice := capturedBody["sessions"].([]interface{})
	if !isSlice || len(sentSessions) != 1 || sentSessions[0] != "conv-42" {
		testingState.Fatalf("sessions=%v want [conv-42]", capturedBody["sessions"])
	}
	if _, legacyPresent := capturedBody["uuid"]; legacyPresent {
		testingState.Fatalf("body still carries the retired singular 'uuid': %v", capturedBody)
	}
}

// TestGetSearchesSendSessionsQueryParam proves the two GET paths that had NO
// session argument before now carry one, repeated per uuid.
func TestGetSearchesSendSessionsQueryParam(testingState *testing.T) {
	var capturedSessions []string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedSessions = request.URL.Query()["sessions"]
		io.WriteString(responseWriter, `{"results":[],"records":[],"count":0}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))

	if _, smartErr := memoryClient.SmartSearch(
		context.Background(), "engineering", []string{"conv-a", "conv-b"}, 10); smartErr != nil {
		testingState.Fatalf("SmartSearch: %v", smartErr)
	}
	if strings.Join(capturedSessions, ",") != "conv-a,conv-b" {
		testingState.Fatalf("smart sessions=%v want [conv-a conv-b]", capturedSessions)
	}

	capturedSessions = nil
	if _, typeErr := memoryClient.SearchByType(
		context.Background(), "fact", SessionsAll(), 5); typeErr != nil {
		testingState.Fatalf("SearchByType: %v", typeErr)
	}
	if strings.Join(capturedSessions, ",") != SessionWildcard {
		testingState.Fatalf("type sessions=%v want [*]", capturedSessions)
	}
}

// TestSearchFamilyRejectsBadSessionFilters proves EVERY search entry point
// refuses a contradictory filter before it reaches the network — a rejected
// call must never produce an HTTP request.
func TestSearchFamilyRejectsBadSessionFilters(testingState *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestCount++
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	ctx := context.Background()

	badFilters := map[string][]string{
		"absent":    nil,
		"empty":     {},
		"mixed":     {SessionWildcard, "conv-a"},
		"emptyItem": {""},
	}

	for filterName, badFilter := range badFilters {
		callers := map[string]func() error{
			"Search": func() error {
				_, err := memoryClient.Search(ctx, "q", badFilter)
				return err
			},
			"SearchSessions": func() error {
				_, err := memoryClient.SearchSessions(ctx, "q", badFilter)
				return err
			},
			"SearchTenantShared": func() error {
				_, err := memoryClient.SearchTenantShared(ctx, "q", badFilter)
				return err
			},
			"SearchClientShared": func() error {
				_, err := memoryClient.SearchClientShared(ctx, "q", badFilter)
				return err
			},
			"SearchShared": func() error {
				_, err := memoryClient.SearchShared(ctx, "q", badFilter)
				return err
			},
			"Recall": func() error {
				_, err := memoryClient.Recall(ctx, "q", badFilter, 5)
				return err
			},
			"SmartSearch": func() error {
				_, err := memoryClient.SmartSearch(ctx, "q", badFilter, 5)
				return err
			},
			"SearchByType": func() error {
				_, err := memoryClient.SearchByType(ctx, "fact", badFilter, 5)
				return err
			},
		}

		for methodName, callMethod := range callers {
			callErr := callMethod()
			if callErr == nil {
				testingState.Fatalf("%s accepted the %s session filter", methodName, filterName)
			}
			if !strings.HasPrefix(callErr.Error(), "INVALID_PARAM: ") {
				testingState.Fatalf("%s/%s error=%q want an INVALID_PARAM message", methodName, filterName, callErr.Error())
			}
		}
	}

	if requestCount != 0 {
		testingState.Fatalf("rejected filters still produced %d HTTP request(s)", requestCount)
	}
}
