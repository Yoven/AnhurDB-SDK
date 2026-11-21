package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The session-record-limit refusal must arrive TYPED through the real transport
// (a genuine Memory.Add against a stub server) for the structured body the
// server always sends (ADR-0033, server/handler/service_bridge.go:109-125).
// Constructing the error by hand would only prove the constructor. Since 4.0.0
// the text-only body of pre-ADR-0033 servers is NOT recognised any more — see
// TestSessionLimitSentenceWithoutCodeIsGenericConflict.

// sessionLimitStubServer answers every request with the given status and body.
func sessionLimitStubServer(t *testing.T, statusCode int, body string) *Memory {
	t.Helper()
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(statusCode)
		_, _ = responseWriter.Write([]byte(body))
	}))
	t.Cleanup(stubServer.Close)
	return NewMemory("test-key", WithURL(stubServer.URL))
}

func TestSessionRecordLimitIsTyped(t *testing.T) {
	testCases := []struct {
		name            string
		body            string
		wantSession     string
		wantLimit       int
		wantCount       int
		wantInErrorText string
	}{
		{
			name:            "structured body",
			body:            `{"error":"session has reached the maximum of 500 records (refused)","code":"session_record_limit","limit":500,"count":503,"session_uuid":"s-full-1"}`,
			wantSession:     "s-full-1",
			wantLimit:       500,
			wantCount:       503,
			wantInErrorText: "Conflict (HTTP 409): session has reached the maximum of 500 records (refused) (session record limit: session=s-full-1, limit=500, count=503)",
		},
		{
			name:            "code without the usual text",
			body:            `{"error":"cap","code":"session_record_limit","limit":7,"count":7,"session_uuid":"s2"}`,
			wantSession:     "s2",
			wantLimit:       7,
			wantCount:       7,
			wantInErrorText: "cap (session record limit: session=s2, limit=7, count=7)",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			memory := sessionLimitStubServer(t, http.StatusConflict, testCase.body)

			_, addErr := memory.Add(context.Background(), "turn text", WithSessionID("s-full-1"))

			var limitError *SessionRecordLimitError
			if !errors.As(addErr, &limitError) {
				t.Fatalf("expected *SessionRecordLimitError, got %T: %v", addErr, addErr)
			}
			if !errors.Is(addErr, ErrSessionRecordLimit) {
				t.Error("errors.Is(err, ErrSessionRecordLimit) must match")
			}
			if limitError.SessionUUID != testCase.wantSession {
				t.Errorf("SessionUUID = %q, want %q", limitError.SessionUUID, testCase.wantSession)
			}
			if limitError.Limit != testCase.wantLimit {
				t.Errorf("Limit = %d, want %d", limitError.Limit, testCase.wantLimit)
			}
			if limitError.Count != testCase.wantCount {
				t.Errorf("Count = %d, want %d", limitError.Count, testCase.wantCount)
			}
			if limitError.Retryable() || limitError.Kind() != KindConflict || limitError.StatusCode() != 409 {
				t.Errorf("want non-retryable conflict 409, got retryable=%v kind=%s status=%d",
					limitError.Retryable(), limitError.Kind(), limitError.StatusCode())
			}
			if !strings.Contains(addErr.Error(), testCase.wantInErrorText) {
				t.Errorf("Error() = %q, want it to contain %q", addErr.Error(), testCase.wantInErrorText)
			}
			// Backward compatibility: callers branching on *APIError 409 still match.
			var apiError *APIError
			if !errors.As(addErr, &apiError) || apiError.StatusCode != 409 || apiError.Retryable() {
				t.Errorf("errors.As(*APIError) must still find a non-retryable 409, got %+v", apiError)
			}
		})
	}
}

// An ordinary 409 must stay an ordinary *APIError — the typed refusal is for
// the session cap only, never for every conflict.
func TestOtherConflictIsNotSessionRecordLimit(t *testing.T) {
	memory := sessionLimitStubServer(t, http.StatusConflict, `{"error":"record already superseded"}`)

	_, addErr := memory.Add(context.Background(), "turn text", WithSessionID("s1"))

	if errors.Is(addErr, ErrSessionRecordLimit) {
		t.Fatalf("a generic 409 was classified as a session limit: %v", addErr)
	}
	var apiError *APIError
	if !errors.As(addErr, &apiError) || apiError.StatusCode != 409 {
		t.Fatalf("expected plain *APIError 409, got %T: %v", addErr, addErr)
	}
}

// The code must not be recognised on a non-409 status (a 500 quoting it, say).
func TestSessionLimitCodeOnOtherStatusIsNotTyped(t *testing.T) {
	if sessionRecordLimitFromResponse(500, []byte(`{"error":"x","code":"session_record_limit","limit":5,"count":5,"session_uuid":"s"}`)) != nil {
		t.Fatal("only HTTP 409 carries the session-limit contract")
	}
}

// TestSessionLimitSentenceWithoutCodeIsGenericConflict proves the 4.0.0
// removal: a 409 that carries only the old sentence (the pre-ADR-0033 body, no
// code) is now an ordinary *APIError conflict, not the typed refusal — the
// sentence and the "maximum of N records" number parse are gone.
func TestSessionLimitSentenceWithoutCodeIsGenericConflict(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{"old JSON body without code", `{"error":"session has reached the maximum of 500 records"}`},
		{"plain-text body", `session has reached the maximum of 500 records`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			memory := sessionLimitStubServer(t, http.StatusConflict, testCase.body)

			_, addErr := memory.Add(context.Background(), "turn text", WithSessionID("s1"))

			var limitError *SessionRecordLimitError
			if errors.As(addErr, &limitError) || errors.Is(addErr, ErrSessionRecordLimit) {
				t.Fatalf("a 409 without the code must not be typed any more: %v", addErr)
			}
			var apiError *APIError
			if !errors.As(addErr, &apiError) || apiError.StatusCode != 409 || apiError.Kind() != KindConflict {
				t.Fatalf("want a plain *APIError 409 conflict, got %T: %v", addErr, addErr)
			}
		})
	}
}

// TestSessionLimitCodeWithMalformedFieldsIsGenericConflict pins the cross-SDK
// rule (Python session_limit.py, TypeScript sessionLimit.ts): a body with the
// code but a missing, null or mistyped limit/count/session_uuid breaks the
// contract and stays the generic 409 — Go must detect PRESENCE, never decode a
// missing number as 0.
func TestSessionLimitCodeWithMalformedFieldsIsGenericConflict(t *testing.T) {
	malformedBodies := map[string]string{
		"limit missing":        `{"error":"x","code":"session_record_limit","count":5,"session_uuid":"s"}`,
		"count missing":        `{"error":"x","code":"session_record_limit","limit":5,"session_uuid":"s"}`,
		"session_uuid missing": `{"error":"x","code":"session_record_limit","limit":5,"count":5}`,
		"limit null":           `{"error":"x","code":"session_record_limit","limit":null,"count":5,"session_uuid":"s"}`,
		"limit as string":      `{"error":"x","code":"session_record_limit","limit":"5","count":5,"session_uuid":"s"}`,
		"count fractional":     `{"error":"x","code":"session_record_limit","limit":5,"count":5.5,"session_uuid":"s"}`,
		"session_uuid number":  `{"error":"x","code":"session_record_limit","limit":5,"count":5,"session_uuid":7}`,
	}
	for caseName, body := range malformedBodies {
		t.Run(caseName, func(t *testing.T) {
			if sessionRecordLimitFromResponse(409, []byte(body)) != nil {
				t.Fatalf("malformed body was typed: %s", body)
			}
		})
	}
}

// TestSessionLimitMessageIsTheCrossSDKLayout pins the exact message Python and
// TypeScript also produce, including the 500-character server excerpt.
func TestSessionLimitMessageIsTheCrossSDKLayout(t *testing.T) {
	longSentence := strings.Repeat("é", 600)
	refusal := sessionRecordLimitFromResponse(409, []byte(`{"error":"`+longSentence+`","code":"session_record_limit","limit":3,"count":4,"session_uuid":"s9"}`))
	if refusal == nil {
		t.Fatal("well-formed body not typed")
	}
	wantMessage := "Conflict (HTTP 409): " + strings.Repeat("é", 500) + " (session record limit: session=s9, limit=3, count=4)"
	if refusal.Error() != wantMessage {
		t.Fatalf("Error() = %q\nwant      %q", refusal.Error(), wantMessage)
	}
	if refusal.ServerMessage != longSentence {
		t.Error("ServerMessage must stay verbatim (only the message is excerpted)")
	}
}
