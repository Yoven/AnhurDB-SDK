package client

// read_option_refusal_test.go — one domain: proving that an option the endpoint
// cannot honour fails at CALL TIME instead of reaching a server that answers
// 200 and ignores it.
//
// Every case here fails against the pre-3.0.0 code: Walk wrote `_ = opts`, and
// SearchByType / SmartSearch forwarded the two or three params they knew about
// while silently swallowing the other sixteen.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// captureServer records the request the SDK actually sent and answers with a
// fixed JSON body.
func captureServer(t *testing.T, responseJSON string, seen *http.Request, body *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		rawBody, _ := io.ReadAll(request.Body)
		if body != nil {
			*body = rawBody
		}
		if seen != nil {
			*seen = *request
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, responseJSON)
	}))
}

// TestWalkWiresAsOfOntoTheWire proves the ONE option POST /api/v1/walk honours
// actually reaches the request body. Before 3.0.0 Walk did `_ = opts`, so this
// body carried no as_of at all.
func TestWalkWiresAsOfOntoTheWire(t *testing.T) {
	var requestBody []byte
	server := captureServer(t, `{"nodes":[],"edges":[],"truncated":false}`, nil, &requestBody)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, walkErr := memoryClient.Walk(context.Background(), 18, 2,
		WithAsOf("2026-01-01T00:00:00Z")); walkErr != nil {
		t.Fatalf("Walk returned error: %v", walkErr)
	}

	var payload map[string]interface{}
	if unmarshalErr := json.Unmarshal(requestBody, &payload); unmarshalErr != nil {
		t.Fatalf("walk body is not JSON: %v (%s)", unmarshalErr, requestBody)
	}
	if payload["as_of"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("as_of missing from walk body: %s", requestBody)
	}
}

// TestWalkOmitsAsOfWhenUnset keeps the historical request byte-identical for a
// caller who asked for no snapshot. An always-present key is a key the server
// can never default.
func TestWalkOmitsAsOfWhenUnset(t *testing.T) {
	var requestBody []byte
	server := captureServer(t, `{"nodes":[],"edges":[],"truncated":false}`, nil, &requestBody)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, walkErr := memoryClient.Walk(context.Background(), 18, 2); walkErr != nil {
		t.Fatalf("Walk returned error: %v", walkErr)
	}
	if strings.Contains(string(requestBody), "as_of") {
		t.Fatalf("as_of must be absent when the caller did not ask: %s", requestBody)
	}
}

// TestUnsupportedReadOptionsAreRefusedAtCallTime is the core regression test for
// the nineteen discarded options. Each case passes an option the endpoint does
// not parse and requires the SDK to fail BEFORE any request leaves.
//
// Junior Tip [why the test asserts "no request reached the server"]: the whole
// defect class was "the server answers 200 and drops it". A refusal that still
// spends the round trip would leave the caller's log full of successful
// requests that did nothing.
func TestUnsupportedReadOptionsAreRefusedAtCallTime(t *testing.T) {
	testCases := []struct {
		name       string
		call       func(memoryClient *Memory) error
		wantOption string
		wantMethod string
	}{
		{
			name: "Walk refuses WithSince (server silently drops it)",
			call: func(memoryClient *Memory) error {
				_, err := memoryClient.Walk(context.Background(), 18, 2, WithSince("2026-01-01T00:00:00Z"))
				return err
			},
			wantOption: "WithSince",
			wantMethod: "Walk",
		},
		{
			name: "Walk refuses WithUntil",
			call: func(memoryClient *Memory) error {
				_, err := memoryClient.Walk(context.Background(), 18, 2, WithUntil("2026-01-01T00:00:00Z"))
				return err
			},
			wantOption: "WithUntil",
			wantMethod: "Walk",
		},
		{
			name: "Walk refuses WithLimit",
			call: func(memoryClient *Memory) error {
				_, err := memoryClient.Walk(context.Background(), 18, 2, WithLimit(5))
				return err
			},
			wantOption: "WithLimit",
			wantMethod: "Walk",
		},
		{
			name: "SearchByType refuses WithScope (this route is not plane-aware)",
			call: func(memoryClient *Memory) error {
				_, err := memoryClient.SearchByType(context.Background(), "fact", SessionsAll(), 10,
					WithScope("client_shared"))
				return err
			},
			wantOption: "WithScope",
			wantMethod: "SearchByType",
		},
		{
			name: "SearchByType refuses WithAsOf",
			call: func(memoryClient *Memory) error {
				_, err := memoryClient.SearchByType(context.Background(), "fact", SessionsAll(), 10,
					WithAsOf("2026-01-01T00:00:00Z"))
				return err
			},
			wantOption: "WithAsOf",
			wantMethod: "SearchByType",
		},
		{
			name: "SmartSearch refuses WithSince",
			call: func(memoryClient *Memory) error {
				_, err := memoryClient.SmartSearch(context.Background(), "q", SessionsAll(), 10,
					WithSince("2026-01-01T00:00:00Z"))
				return err
			},
			wantOption: "WithSince",
			wantMethod: "SmartSearch",
		},
		{
			name: "SmartSearch refuses WithDebugSignals (lexical route, no signals)",
			call: func(memoryClient *Memory) error {
				_, err := memoryClient.SmartSearch(context.Background(), "q", SessionsAll(), 10,
					WithDebugSignals())
				return err
			},
			wantOption: "WithDebugSignals",
			wantMethod: "SmartSearch",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			requestsSeen := 0
			server := httptest.NewServer(http.HandlerFunc(
				func(responseWriter http.ResponseWriter, request *http.Request) {
					requestsSeen++
					io.WriteString(responseWriter, `{}`)
				}))
			defer server.Close()

			memoryClient := NewMemory("k", WithURL(server.URL))
			callErr := testCase.call(memoryClient)
			if callErr == nil {
				subTest.Fatalf("%s accepted %s — the option never reaches the server",
					testCase.wantMethod, testCase.wantOption)
			}
			if !errors.Is(callErr, ErrUnsupportedOption) {
				subTest.Fatalf("error %v does not match ErrUnsupportedOption", callErr)
			}
			var unsupported *UnsupportedOptionError
			if !errors.As(callErr, &unsupported) {
				subTest.Fatalf("error %v is not an *UnsupportedOptionError", callErr)
			}
			if unsupported.Option != testCase.wantOption || unsupported.Method != testCase.wantMethod {
				subTest.Fatalf("error names %s/%s, want %s/%s",
					unsupported.Option, unsupported.Method, testCase.wantOption, testCase.wantMethod)
			}
			if !strings.Contains(callErr.Error(), testCase.wantOption) ||
				!strings.Contains(callErr.Error(), testCase.wantMethod) {
				subTest.Fatalf("message %q must name both the option and the method", callErr.Error())
			}
			if requestsSeen != 0 {
				subTest.Fatalf("%d request(s) reached the server — the refusal must happen first",
					requestsSeen)
			}
		})
	}
}

// TestHonouredReadOptionsStillReachTheWire is the other half: the three methods
// that KEEP opts must still forward what the handler parses, or this change
// would have traded a silent drop for a loud one.
func TestHonouredReadOptionsStillReachTheWire(t *testing.T) {
	var seenRequest http.Request
	server := captureServer(t, `{"records":[],"results":null,"count":0}`, &seenRequest, nil)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))

	if _, err := memoryClient.SearchByType(context.Background(), "fact", SessionsAll(), 10,
		WithKeyword("pricing")); err != nil {
		t.Fatalf("SearchByType returned error: %v", err)
	}
	if got := seenRequest.URL.Query().Get("q"); got != "pricing" {
		t.Fatalf("SearchByType q=%q, want pricing", got)
	}

	if _, err := memoryClient.SmartSearch(context.Background(), "memory", SessionsAll(), 10,
		WithTypeFilter("fact"), WithScope("client_shared")); err != nil {
		t.Fatalf("SmartSearch returned error: %v", err)
	}
	if got := seenRequest.URL.Query().Get("type"); got != "fact" {
		t.Fatalf("SmartSearch type=%q, want fact", got)
	}
	if got := seenRequest.URL.Query().Get("scope"); got != "client_shared" {
		t.Fatalf("SmartSearch scope=%q, want client_shared", got)
	}
}

// TestReadOptionUsesCoversEverySearchConfigField keeps the refusal sweep honest.
//
// Junior Tip [why reflection instead of a hand-kept count]: a new With* option
// whose field is missing from readOptionUses would pass every guard above — it
// would be invisible to the sweep and therefore forwardable to an endpoint that
// cannot honour it, which is the exact bug this release removed. Counting the
// struct's fields is the only assertion that fails when someone forgets.
func TestReadOptionUsesCoversEverySearchConfigField(t *testing.T) {
	configType := reflect.TypeOf(searchConfig{})
	if got, want := len(readOptionUses(searchConfig{})), configType.NumField(); got != want {
		t.Fatalf("readOptionUses lists %d options but searchConfig has %d fields — "+
			"add the missing row(s) in read_option_support.go", got, want)
	}
}
