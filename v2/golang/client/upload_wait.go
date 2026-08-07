package client

// upload_wait.go — WaitForUpload: polling de upload com tolerância a 404
// transiente (read-your-writes) e semântica IDÊNTICA nos três SDKs.
//
// Paridade: Python `wait_for_upload` / TypeScript `waitForUpload` — mesma
// máquina de estados, mesmos defaults. Qualquer mudança aqui exige o mesmo
// PR nos três SDKs (PARITY_SPEC.md).
//
// Junior Tip [por que 404 vira "pendente" no começo — medido 2026-08-07]:
// as leituras são load-balanced; logo após o POST de upload um follower que
// ainda não aplicou a entrada devolve 404 legítimo por alguns segundos. Antes
// deste helper cada cliente tratava isso de um jeito (o runner Go tolerava, o
// Python morria, o TS passava por sorte de timing). Dentro de NotFoundGrace o
// 404 é espera; DEPOIS dela é erro real — um id inválido não pode virar espera
// infinita (falhar alto, nunca engolir).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUploadWaitTimeout is returned by WaitForUpload when the upload did not
// reach a terminal status within Timeout. Use errors.Is to detect it.
var ErrUploadWaitTimeout = errors.New("upload wait timed out")

// WaitUploadOptions configures WaitForUpload. Zero values take the parity
// defaults shared by the three SDKs.
type WaitUploadOptions struct {
	// Timeout is the total wait budget (default 120s).
	Timeout time.Duration
	// Interval is the pause between polls (default 5s).
	Interval time.Duration
	// NotFoundGrace is how long an HTTP 404 is treated as "not applied yet"
	// before becoming a real error (default 30s).
	NotFoundGrace time.Duration
}

// waitUploadDefault* are the parity defaults — keep in lockstep with the
// Python and TypeScript helpers.
const (
	waitUploadDefaultTimeout       = 120 * time.Second
	waitUploadDefaultInterval      = 5 * time.Second
	waitUploadDefaultNotFoundGrace = 30 * time.Second
)

// uploadStatusIsTerminal mirrors the status strings the server emits for a
// finished ingest — success AND failure are both terminal: a failed payload is
// data the caller must inspect, not a transport error.
func uploadStatusIsTerminal(result *UploadStatusResult) bool {
	if result == nil {
		return false
	}
	if result.Completed || result.Error != "" {
		return true
	}
	switch strings.ToLower(result.Status) {
	case "completed", "saved", "done", "failed":
		return true
	}
	return false
}

// WaitForUpload polls UploadStatus until the upload reaches a terminal state.
// It returns the final status payload (including status="failed" — inspect
// it), ErrNotFound when the 404 persists beyond NotFoundGrace, or
// ErrUploadWaitTimeout with the last observed status on budget exhaustion.
func (m *Memory) WaitForUpload(ctx context.Context, uploadID int64, opts WaitUploadOptions) (*UploadStatusResult, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = waitUploadDefaultTimeout
	}
	if opts.Interval <= 0 {
		opts.Interval = waitUploadDefaultInterval
	}
	if opts.NotFoundGrace <= 0 {
		opts.NotFoundGrace = waitUploadDefaultNotFoundGrace
	}

	startedAt := time.Now()
	lastStatus := "never-seen"
	for {
		result, statusErr := m.UploadStatus(ctx, uploadID)
		switch {
		case statusErr == nil:
			if uploadStatusIsTerminal(result) {
				return result, nil
			}
			if result != nil && result.Status != "" {
				lastStatus = result.Status
			}
		case errors.Is(statusErr, ErrNotFound):
			if time.Since(startedAt) >= opts.NotFoundGrace {
				return nil, fmt.Errorf("upload %d still 404 after %s grace: %w",
					uploadID, opts.NotFoundGrace, ErrNotFound)
			}
			lastStatus = "not-found-yet"
		default:
			// Transport/5xx: transient by default — the poll loop IS the retry.
			lastStatus = "error: " + statusErr.Error()
		}

		if time.Since(startedAt)+opts.Interval > opts.Timeout {
			return nil, fmt.Errorf("upload %d not terminal after %s (last=%s): %w",
				uploadID, opts.Timeout, lastStatus, ErrUploadWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}
