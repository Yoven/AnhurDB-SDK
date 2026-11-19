package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type countingTransport struct {
	n    atomic.Int32
	base http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.base.RoundTrip(r)
}

func TestWithHTTPClientIsUsedAndRedirectsStayBlocked(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect followed to %s — X-API-Key would leak", r.URL)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	tr := &countingTransport{base: http.DefaultTransport}
	callerClient := &http.Client{Transport: tr} // no CheckRedirect: would follow by default
	m := NewMemory("k", WithURL(srv.URL), WithHTTPClient(callerClient), WithTimeout(5*time.Second))
	_, _ = m.Health(context.Background())
	if tr.n.Load() == 0 {
		t.Fatal("injected client was not used")
	}
	if callerClient.CheckRedirect != nil {
		t.Fatal("caller's client must not be mutated")
	}
}
