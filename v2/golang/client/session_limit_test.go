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
// (a genuine Memory.Add against a stub server), for both the new structured
// body (ADR-0033) and the old text-only body production sends until the deploy.
// Constructing the error by hand would only prove the constructor.

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
		wantLimit       string
		wantCount       string
		wantInErrorText string
	}{
		{
			name:            "new structured body",
			body:            `{"error":"session has reached the maximum of 500 records (refused)","code":"session_record_limit","limit":500,"count":503,"session_uuid":"s-full-1"}`,
			wantSession:     "s-full-1",
			wantLimit:       "500",
			wantCount:       "503",
			wantInErrorText: "session=s-full-1, limit=500, count=503",
		},
		{
			name:            "old text-only body",
			body:            `{"error":"session has reached the maximum of 500 records"}`,
			wantSession:     "",
			wantLimit:       "500", // read back from the server's own sentence
			wantCount:       "unknown",
			wantInErrorText: "session=unknown, limit=500, count=unknown",
		},
		{
			name:            "code without the usual text",
			body:            `{"error":"cap","code":"session_record_limit","limit":7,"count":7,"session_uuid":"s2"}`,
			wantSession:     "s2",
			wantLimit:       "7",
			wantCount:       "7",
			wantInErrorText: "limit=7, count=7",
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
			if gotLimit := optionalIntText(limitError.Limit); gotLimit != testCase.wantLimit {
				t.Errorf("Limit = %s, want %s", gotLimit, testCase.wantLimit)
			}
			if gotCount := optionalIntText(limitError.Count); gotCount != testCase.wantCount {
				t.Errorf("Count = %s, want %s", gotCount, testCase.wantCount)
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

// The text must not be recognised on a non-409 status (a 500 quoting it, say).
func TestSessionLimitTextOnOtherStatusIsNotTyped(t *testing.T) {
	if sessionRecordLimitFromResponse(500, []byte(`{"error":"session has reached the maximum of 5 records"}`)) != nil {
		t.Fatal("only HTTP 409 carries the session-limit contract")
	}
}
