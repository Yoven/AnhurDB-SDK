// transport_error_sentinel_test.go — one domain: proving that the documented
// errors.Is(err, ErrConnectionFail) check fires for an unreachable server and
// stays false for a timeout.
package client

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"
)

// TestUnreachableServerMatchesErrConnectionFail goes through the real HTTP
// path against a closed listener, so the assertion is about what callers get.
func TestUnreachableServerMatchesErrConnectionFail(testHandle *testing.T) {
	closedServer := httptest.NewServer(nil)
	closedURL := closedServer.URL
	closedServer.Close()

	memory := NewMemory("test-key", WithURL(closedURL))
	_, searchError := memory.Search(context.Background(), "anything", SessionsAll())
	if searchError == nil {
		testHandle.Fatal("search against a closed server returned no error")
	}
	if !errors.Is(searchError, ErrConnectionFail) {
		testHandle.Fatalf("errors.Is(err, ErrConnectionFail) = false for %v", searchError)
	}
	var transportError *TransportError
	if !errors.As(searchError, &transportError) || transportError.Kind() != KindTransport {
		testHandle.Fatalf("want *TransportError of kind transport, got %#v", searchError)
	}
	var operationError *net.OpError
	if !errors.As(searchError, &operationError) {
		testHandle.Fatalf("the underlying net cause must stay reachable via errors.As, got %v", searchError)
	}
}

// TestTimeoutDoesNotMatchErrConnectionFail keeps "may have been processed"
// apart from "never connected", while the context cause stays matchable.
func TestTimeoutDoesNotMatchErrConnectionFail(testHandle *testing.T) {
	timeoutError := newTransportError("POST /api/v1/search", context.DeadlineExceeded)
	if errors.Is(timeoutError, ErrConnectionFail) {
		testHandle.Fatal("a timeout must not match ErrConnectionFail")
	}
	if !errors.Is(timeoutError, context.DeadlineExceeded) {
		testHandle.Fatal("errors.Is(err, context.DeadlineExceeded) must stay true")
	}
	if timeoutError.Kind() != KindTimeout {
		testHandle.Fatalf("kind = %s, want timeout", timeoutError.Kind())
	}
}
