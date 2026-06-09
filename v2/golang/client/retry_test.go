package client

import (
	"context"
	"errors"
	"testing"
)

// TestIsTransientWriteError pins the classifier: only not_leader (5xx) and the
// episodic-anchor 422 are transient; permanent validation errors are not.
func TestIsTransientWriteError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not_leader 500", &APIError{StatusCode: 500, Body: `{"error":"not_leader: redirect to node-2"}`}, true},
		{"not leader spaced", &APIError{StatusCode: 503, Body: "raft: not leader"}, true},
		{"episodic anchor 422", &APIError{StatusCode: 422, Body: `{"error":"cannot create semantic without an episodic anchor in session s1"}`}, true},
		{"permanent 422 bad type", &APIError{StatusCode: 422, Body: `{"error":"invalid type 'banana'"}`}, false},
		{"permanent 400", &APIError{StatusCode: 400, Body: "malformed json"}, false},
		{"unauthorized", ErrUnauthorized, false},
		{"not found", ErrNotFound, false},
		{"connection fail not retried", ErrConnectionFail, false},
		{"wrapped not_leader string", errors.New("post failed: not_leader"), true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := isTransientWriteError(testCase.err)
			if got != testCase.want {
				t.Fatalf("isTransientWriteError(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

// TestWithWriteRetrySucceedsAfterTransient verifies a transient failure is
// retried and a later success is returned.
func TestWithWriteRetrySucceedsAfterTransient(t *testing.T) {
	attempts := 0
	result, err := withWriteRetry(context.Background(), func() (int, error) {
		attempts++
		if attempts < 2 {
			return 0, &APIError{StatusCode: 422, Body: "without an episodic anchor"}
		}
		return 99, nil
	})
	if err != nil {
		t.Fatalf("expected success after retry, got err=%v", err)
	}
	if result != 99 {
		t.Fatalf("expected result 99, got %d", result)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

// TestWithWriteRetryStopsOnPermanent verifies a permanent error is returned
// immediately with no extra attempts.
func TestWithWriteRetryStopsOnPermanent(t *testing.T) {
	attempts := 0
	_, err := withWriteRetry(context.Background(), func() (int, error) {
		attempts++
		return 0, ErrUnauthorized
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("permanent error must not retry; attempts=%d", attempts)
	}
}

// TestWithWriteRetryExhausts verifies the attempt cap is honoured.
func TestWithWriteRetryExhausts(t *testing.T) {
	attempts := 0
	_, err := withWriteRetry(context.Background(), func() (int, error) {
		attempts++
		return 0, &APIError{StatusCode: 500, Body: "not_leader"}
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != writeRetryMaxAttempts {
		t.Fatalf("expected %d attempts, got %d", writeRetryMaxAttempts, attempts)
	}
}
