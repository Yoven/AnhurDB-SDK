package client_test

// upload_wait_test.go — WaitForUpload: polling tolerante a 404 transiente.
//
// Junior Tip [por que 404 é "pending" no começo — medido em produção
// 2026-08-07]: as leituras do AnhurDB são load-balanced; logo após o POST de
// upload, um follower que ainda não aplicou a entrada devolve 404 LEGÍTIMO por
// alguns segundos (read-your-writes). O runner Go tolerava, o Python morria e
// o TS só passava por sorte de timing — este helper dá aos três SDKs a MESMA
// semântica: 404 dentro da janela de graça = pendente; depois dela = erro real
// (id inválido não pode virar espera infinita — falhar alto).

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

func TestWaitForUpload_ToleratesEarly404ThenCompletes(t *testing.T) {
	var callCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		currentCall := callCount.Add(1)
		if currentCall <= 2 {
			http.Error(responseWriter, `{"error":"record not found"}`, http.StatusNotFound)
			return
		}
		io.WriteString(responseWriter, `{"record_id":42,"status":"completed","completed":true}`)
	}))
	defer server.Close()

	mem := client.NewMemory("key", client.WithURL(server.URL))
	result, waitErr := mem.WaitForUpload(context.Background(), 42, client.WaitUploadOptions{
		Timeout:       5 * time.Second,
		Interval:      20 * time.Millisecond,
		NotFoundGrace: 2 * time.Second,
	})
	if waitErr != nil {
		t.Fatalf("WaitForUpload must tolerate early 404s: %v", waitErr)
	}
	if !result.Completed || result.RecordID != 42 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if callCount.Load() < 3 {
		t.Fatalf("expected at least 3 polls, got %d", callCount.Load())
	}
}

func TestWaitForUpload_404BeyondGraceIsARealError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Error(responseWriter, `{"error":"record not found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	mem := client.NewMemory("key", client.WithURL(server.URL))
	startedAt := time.Now()
	_, waitErr := mem.WaitForUpload(context.Background(), 999999, client.WaitUploadOptions{
		Timeout:       5 * time.Second,
		Interval:      20 * time.Millisecond,
		NotFoundGrace: 150 * time.Millisecond,
	})
	if !errors.Is(waitErr, client.ErrNotFound) {
		t.Fatalf("404 beyond grace must surface ErrNotFound, got %v", waitErr)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("must fail at the grace boundary, not the full timeout (took %s)", elapsed)
	}
}

func TestWaitForUpload_FailedStatusIsTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		io.WriteString(responseWriter, `{"record_id":7,"status":"failed","error":"extract crashed"}`)
	}))
	defer server.Close()

	mem := client.NewMemory("key", client.WithURL(server.URL))
	result, waitErr := mem.WaitForUpload(context.Background(), 7, client.WaitUploadOptions{
		Timeout:  2 * time.Second,
		Interval: 20 * time.Millisecond,
	})
	if waitErr != nil {
		t.Fatalf("failed status is terminal data, not a transport error: %v", waitErr)
	}
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("caller must receive the failed payload to inspect: %+v", result)
	}
}

func TestWaitForUpload_TimeoutSurfacesLastStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		io.WriteString(responseWriter, `{"record_id":8,"status":"processing"}`)
	}))
	defer server.Close()

	mem := client.NewMemory("key", client.WithURL(server.URL))
	_, waitErr := mem.WaitForUpload(context.Background(), 8, client.WaitUploadOptions{
		Timeout:  200 * time.Millisecond,
		Interval: 20 * time.Millisecond,
	})
	if !errors.Is(waitErr, client.ErrUploadWaitTimeout) {
		t.Fatalf("timeout must wrap ErrUploadWaitTimeout, got %v", waitErr)
	}
}
