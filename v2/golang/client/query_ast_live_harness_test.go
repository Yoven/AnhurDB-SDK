//go:build astlive

package client

// query_ast_live_harness_test.go — the plumbing that lets the AST matrix run
// against the REAL AnhurDB router while still capturing the exact bytes the Go
// SDK put on the wire.
//
// Domain: transport capture + run-level guards. No assertions about the query
// grammar live here; those are in query_ast_live_operators_test.go and
// query_ast_live_errors_test.go.
//
// Junior Tip [why a forwarding proxy and NOT an httptest mock, 2026-09-14]:
// a mock server answers whatever the person who wrote it believed the server
// does. This project has been burned by exactly that — a fixture that agrees
// with the code it tests proves only that the author was self-consistent. The
// proxy below is transparent: it copies the request verbatim upstream to the
// production router, copies the response verbatim back, and only WRITES DOWN
// what passed through. Every status code and every row in this suite therefore
// came from the real server; the capture is a tap, not a substitute.
//
// Junior Tip [the key never touches the log]: the proxy copies headers by
// reference into the upstream request and never serialises them. Only the body
// is recorded. Do not "improve" the capture by dumping req.Header.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// astWireCapture is one recorded round trip against POST /api/v1/query.
type astWireCapture struct {
	Case        string `json:"case"`
	BuilderCall string `json:"builder_call"`
	WireBody    string `json:"wire_body"`
	HTTPStatus  int    `json:"http_status"`
	ResultIDs   []int  `json:"result_ids"`
	ErrorText   string `json:"error"`
	Note        string `json:"note,omitempty"`
}

// astLiveHarness owns the proxy, the Memory pointed at it, and the capture log.
type astLiveHarness struct {
	proxy        *httptest.Server
	memory       *Memory
	upstreamURL  string
	sessionUUID  string
	containerTag string

	mutex        sync.Mutex
	lastBody     string // wire body of the most recent POST /api/v1/query
	lastStatus   int
	lastResponse []byte
	captures     []astWireCapture
	createdIDs   []int64
}

// astWireOutputPath is where phase 3 expects the byte-for-byte comparison input.
const astWireOutputPath = "/tmp/claude-1000/-home-junior-Projects-yoven-Anhur/" +
	"e352e771-a074-42d0-a82e-a6151ebb7083/scratchpad/ast/go_wire.jsonl"

// newASTLiveHarness wires the SDK to production through the capturing proxy.
//
// Junior Tip [fail, never skip, when the environment is missing]: a t.Skip here
// would let this whole suite report PASS while never having contacted the
// server — the vacuous run this exercise exists to prevent. The build tag is
// the opt-in; once you have opted in, an unusable environment is a FAILURE.
func newASTLiveHarness(testHandle *testing.T) *astLiveHarness {
	testHandle.Helper()

	apiKey := strings.TrimSpace(os.Getenv("ANHUR_API_KEY"))
	if apiKey == "" {
		testHandle.Fatal("ANHUR_API_KEY is empty — the astlive suite must talk to a real server, refusing to run vacuously")
	}
	upstreamURL := strings.TrimSpace(os.Getenv("ANHUR_URL"))
	if upstreamURL == "" {
		testHandle.Fatal("ANHUR_URL is empty — refusing to guess the router address")
	}

	harness := &astLiveHarness{upstreamURL: strings.TrimRight(upstreamURL, "/")}
	harness.proxy = httptest.NewServer(http.HandlerFunc(harness.forward))
	testHandle.Cleanup(harness.proxy.Close)

	harness.containerTag = fmt.Sprintf("ast-teste-go-%d", time.Now().Unix())
	harness.memory = NewMemory(apiKey,
		WithURL(harness.proxy.URL),
		WithUserID(harness.containerTag),
		WithTimeout(120*time.Second),
	)
	return harness
}

// forward is the transparent proxy: verbatim upstream, verbatim back, body taped.
func (harness *astLiveHarness) forward(responseWriter http.ResponseWriter, incoming *http.Request) {
	requestBody, readError := io.ReadAll(incoming.Body)
	if readError != nil {
		http.Error(responseWriter, "proxy could not read request body", http.StatusInternalServerError)
		return
	}

	upstreamRequest, buildError := http.NewRequestWithContext(
		incoming.Context(), incoming.Method,
		harness.upstreamURL+incoming.URL.RequestURI(),
		bytes.NewReader(requestBody))
	if buildError != nil {
		http.Error(responseWriter, "proxy could not build upstream request", http.StatusInternalServerError)
		return
	}
	// Header values (including X-API-Key) are copied, never inspected or logged.
	//
	// Junior Tip [Accept-Encoding is the one header this proxy must NOT forward,
	// 2026-09-14]: Go's transport adds `Accept-Encoding: gzip` itself and then
	// transparently DECOMPRESSES the reply — but only when it added the header.
	// Copying the client's header through makes the transport treat compression
	// as the caller's business, so upstreamResponse.Body arrives as gzip bytes.
	// The SDK under test still decoded fine (its own transport decompressed), but
	// the TAP recorded compressed bytes, json.Unmarshal quietly failed, and every
	// recorded result set came back as zero rows. Two full production runs were
	// spent chasing "the writes are invisible" before this was the answer. The
	// fixture guard is what refused to let that ship as a green suite.
	for headerName, headerValues := range incoming.Header {
		if strings.EqualFold(headerName, "Accept-Encoding") {
			continue
		}
		for _, headerValue := range headerValues {
			upstreamRequest.Header.Add(headerName, headerValue)
		}
	}

	upstreamClient := &http.Client{Timeout: 120 * time.Second}
	upstreamResponse, callError := upstreamClient.Do(upstreamRequest)
	if callError != nil {
		http.Error(responseWriter, "proxy upstream call failed: "+callError.Error(), http.StatusBadGateway)
		return
	}
	defer upstreamResponse.Body.Close()

	responseBody, responseReadError := io.ReadAll(upstreamResponse.Body)
	if responseReadError != nil {
		http.Error(responseWriter, "proxy could not read upstream body", http.StatusBadGateway)
		return
	}

	if incoming.Method == http.MethodPost && incoming.URL.Path == "/api/v1/query" {
		harness.mutex.Lock()
		harness.lastBody = string(requestBody)
		harness.lastStatus = upstreamResponse.StatusCode
		harness.lastResponse = responseBody
		harness.mutex.Unlock()
	}

	for headerName, headerValues := range upstreamResponse.Header {
		for _, headerValue := range headerValues {
			responseWriter.Header().Add(headerName, headerValue)
		}
	}
	responseWriter.WriteHeader(upstreamResponse.StatusCode)
	_, _ = responseWriter.Write(responseBody)
}

// snapshot returns the last captured POST /api/v1/query exchange.
func (harness *astLiveHarness) snapshot() (string, int, []byte) {
	harness.mutex.Lock()
	defer harness.mutex.Unlock()
	return harness.lastBody, harness.lastStatus, append([]byte(nil), harness.lastResponse...)
}

// resetSnapshot clears the tap so a case that never reaches the wire is visible
// as an empty body instead of inheriting the previous case's bytes.
//
// Junior Tip [this is the "did the measurement run?" guard]: a client-side
// rejection sends NOTHING. Without this reset the JSONL would show the previous
// case's body next to the new case's name, and the phase-3 byte comparison
// would be comparing a lie.
func (harness *astLiveHarness) resetSnapshot() {
	harness.mutex.Lock()
	harness.lastBody = ""
	harness.lastStatus = 0
	harness.lastResponse = nil
	harness.mutex.Unlock()
}

// record appends one capture line for the phase-3 cross-SDK diff.
func (harness *astLiveHarness) record(capture astWireCapture) {
	harness.mutex.Lock()
	harness.captures = append(harness.captures, capture)
	harness.mutex.Unlock()
}

// flushWire writes every capture as JSONL. Called from the test's cleanup so a
// failing assertion still leaves the evidence behind.
func (harness *astLiveHarness) flushWire(testHandle *testing.T) {
	testHandle.Helper()
	if mkdirError := os.MkdirAll(filepath.Dir(astWireOutputPath), 0o755); mkdirError != nil {
		testHandle.Errorf("creating wire output dir: %v", mkdirError)
		return
	}
	outputFile, createError := os.Create(astWireOutputPath)
	if createError != nil {
		testHandle.Errorf("creating wire output file: %v", createError)
		return
	}
	defer outputFile.Close()

	harness.mutex.Lock()
	defer harness.mutex.Unlock()
	for _, capture := range harness.captures {
		if capture.ResultIDs == nil {
			capture.ResultIDs = []int{}
		}
		encoded, marshalError := json.Marshal(capture)
		if marshalError != nil {
			testHandle.Errorf("marshalling capture %q: %v", capture.Case, marshalError)
			continue
		}
		if _, writeError := outputFile.Write(append(encoded, '\n')); writeError != nil {
			testHandle.Errorf("writing capture %q: %v", capture.Case, writeError)
			return
		}
	}
	fmt.Printf("WIRE_CAPTURES=%d file=%s\n", len(harness.captures), astWireOutputPath)
}
