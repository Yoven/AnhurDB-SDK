package client

// scope_errors.go — the three typed answers the L2 tenant library (ADR-0029) added to
// the error contract: a library store that collides with a registered tenant, an
// upload id that is ambiguous across planes, and a library delete-by-file that
// failed on one of its two stores.
//
// One domain in one sentence: "recognise each of those three server answers in an
// HTTP error body and return a typed, correctly classified error".
//
// Wire shapes (AnhurDB fix/l2-lib-scope, 4574c4d):
//   - collision: HTTP 409 {"error": "...tenant library store id is a registered
//     tenant; rename one of the two tenants before using tenant_shared", "code":
//     "tenant_library_collision"} on EVERY door — search, by-id, delete-by-file and
//     upload (handler/search_scope_request.go:35-43 and writeScopeLegError :109-118;
//     the upload door uses the same writer, handler/upload.go:133-135). An unreadable
//     tenant registry is HTTP 503 (upload included) and stays an ordinary retryable
//     *APIError.
//   - ambiguous upload status: HTTP 409 {"error": "record N is a file upload in more
//     than one plane; pass ?scope=..."} (handler/upload_status_scope.go:186-191).
//     The server sends NO code for this one, so the stable sentence is the contract.
//   - library delete partial: HTTP 500 {"error": "...", "partial": true, "legs":
//     [...]} (handler/record_delete_file_library.go:53-57).
//
// Junior Tip [the collision is recognised by its code ONLY, 4.0.0]: the server's
// sentence names the store and the fix and may be reworded; the "code" is the
// contract. Until 3.x the SDK also matched the sentence (and an upload 400) for
// servers older than 4574c4d; every server now sends the code on a 409, so a body
// without it is an ordinary *APIError. The session-record-limit 409 has its own
// code ("session_record_limit") and is checked FIRST in response.go, so the two can
// never be confused.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// TenantLibraryCollisionCode is the machine-readable "code" of the collision refusal
// (server service.TenantLibraryCollisionErrorCode).
const TenantLibraryCollisionCode = "tenant_library_collision"

// uploadStatusAmbiguousText is the stable part of the ambiguous-upload-status 409.
const uploadStatusAmbiguousText = "is a file upload in more than one plane"

var (
	// ErrTenantLibraryCollision matches every *TenantLibraryCollisionError.
	ErrTenantLibraryCollision = errors.New("anhurdb: tenant library store collides with a registered tenant")
	// ErrUploadStatusAmbiguous matches every *UploadStatusAmbiguousError.
	ErrUploadStatusAmbiguous = errors.New("anhurdb: upload id is ambiguous across planes")
	// ErrLibraryDeletePartial matches every *LibraryDeletePartialError.
	ErrLibraryDeletePartial = errors.New("anhurdb: tenant library delete-by-file partially failed")
)

// TenantLibraryCollisionError: the caller's tenant library store {c}_{t}_lib is the
// registered store of ANOTHER tenant (an existing tenant named "t_lib"), so the server
// refuses every tenant_shared read, by-id, upload and delete for this caller.
// Not retryable: it heals only when one of the two tenants is renamed.
type TenantLibraryCollisionError struct {
	// Status is the HTTP status the server used (always 409).
	Status        int
	ServerMessage string
	response      *APIError
}

func (collisionErr *TenantLibraryCollisionError) Error() string {
	return fmt.Sprintf("AnhurDB refused the tenant library (HTTP %d): %s (not retryable: rename one of the two tenants)",
		collisionErr.Status, truncateErrorBody(collisionErr.ServerMessage))
}

// Unwrap lets errors.Is(err, ErrTenantLibraryCollision) and errors.As(err, **APIError) match.
func (collisionErr *TenantLibraryCollisionError) Unwrap() []error {
	return []error{ErrTenantLibraryCollision, collisionErr.response}
}

// StatusCode returns the HTTP status the server used.
func (collisionErr *TenantLibraryCollisionError) StatusCode() int { return collisionErr.Status }

// Kind follows the status (conflict for the 409 the server sends).
func (collisionErr *TenantLibraryCollisionError) Kind() ErrorKind {
	return KindFor(collisionErr.Status)
}

// Retryable is always false: the same request is refused until an operator acts.
func (collisionErr *TenantLibraryCollisionError) Retryable() bool { return false }

// UploadStatusAmbiguousError: an unscoped upload-status poll matched a file row in
// more than one plane. Pass WithRecordScope(UploadResult.Scope). Not retryable.
type UploadStatusAmbiguousError struct {
	ServerMessage string
	response      *APIError
}

func (ambiguousErr *UploadStatusAmbiguousError) Error() string {
	return fmt.Sprintf("Conflict (HTTP 409): %s (pass WithRecordScope(UploadResult.Scope))", truncateErrorBody(ambiguousErr.ServerMessage))
}

// Unwrap lets errors.Is(err, ErrUploadStatusAmbiguous) and errors.As(err, **APIError) match.
func (ambiguousErr *UploadStatusAmbiguousError) Unwrap() []error {
	return []error{ErrUploadStatusAmbiguous, ambiguousErr.response}
}

// StatusCode is 409.
func (ambiguousErr *UploadStatusAmbiguousError) StatusCode() int { return 409 }

// Kind is conflict.
func (ambiguousErr *UploadStatusAmbiguousError) Kind() ErrorKind { return KindConflict }

// Retryable is false: the same unscoped poll is ambiguous every time.
func (ambiguousErr *UploadStatusAmbiguousError) Retryable() bool { return false }

// LibraryDeletePartialError: a tenant-library delete-by-file ran on its two stores
// (two Raft groups, no cross-group transaction) and at least one failed. Legs holds
// EVERY leg's outcome, the successful ones too.
//
// Junior Tip [retryable on purpose]: the server's delete is keyed by (session, prefix)
// and is idempotent, and a leg already emptied is not dispatched again, so repeating
// the same DeleteFile is the documented recovery. The caller can only retry what it
// was told failed — that is why Legs is surfaced instead of a bare 500.
type LibraryDeletePartialError struct {
	ServerMessage string
	Legs          []DeleteFileLeg
	response      *APIError
}

func (partialErr *LibraryDeletePartialError) Error() string {
	return fmt.Sprintf("Server error (HTTP 500): %s (partial tenant library delete; retry is safe)", partialErr.ServerMessage)
}

// Unwrap lets errors.Is(err, ErrLibraryDeletePartial) and errors.As(err, **APIError) match.
func (partialErr *LibraryDeletePartialError) Unwrap() []error {
	return []error{ErrLibraryDeletePartial, partialErr.response}
}

// StatusCode is 500.
func (partialErr *LibraryDeletePartialError) StatusCode() int { return 500 }

// Kind is server.
func (partialErr *LibraryDeletePartialError) Kind() ErrorKind { return KindServer }

// Retryable is true — see the type's Junior Tip.
func (partialErr *LibraryDeletePartialError) Retryable() bool { return true }

// scopeErrorWireBody is the subset of an error body this file reads.
type scopeErrorWireBody struct {
	Error   string          `json:"error"`
	Code    string          `json:"code"`
	Partial bool            `json:"partial"`
	Legs    []DeleteFileLeg `json:"legs"`
}

// decodeScopeErrorBody reads an error body; a non-JSON body becomes its own text.
func decodeScopeErrorBody(body []byte) scopeErrorWireBody {
	var wireBody scopeErrorWireBody
	if decodeErr := json.Unmarshal(body, &wireBody); decodeErr != nil {
		return scopeErrorWireBody{Error: strings.TrimSpace(string(body))}
	}
	wireBody.Error = strings.TrimSpace(wireBody.Error)
	return wireBody
}

// scopeErrorFromResponse returns the typed L2 error for (status, full body), or nil.
// The caller passes the UNTRUNCATED body: the partial-delete legs can exceed the
// 500-character excerpt used in messages.
func scopeErrorFromResponse(statusCode int, body []byte) error {
	if statusCode != 409 && statusCode != 500 {
		return nil
	}
	wireBody := decodeScopeErrorBody(body)
	excerpt := truncateErrorBody(string(body))
	switch {
	case statusCode == 409 && wireBody.Code == TenantLibraryCollisionCode:
		return &TenantLibraryCollisionError{
			Status:        statusCode,
			ServerMessage: wireBody.Error,
			response:      &APIError{StatusCode: statusCode, Body: excerpt},
		}
	case statusCode == 409 && strings.Contains(wireBody.Error, uploadStatusAmbiguousText):
		return &UploadStatusAmbiguousError{
			ServerMessage: wireBody.Error,
			response:      &APIError{StatusCode: statusCode, Body: excerpt},
		}
	case statusCode == 500 && wireBody.Partial:
		return &LibraryDeletePartialError{
			ServerMessage: truncateErrorBody(wireBody.Error),
			Legs:          wireBody.Legs,
			response:      &APIError{StatusCode: statusCode, Body: excerpt},
		}
	}
	return nil
}

// truncateErrorBody bounds an error body quoted in a message (500 characters, the
// same cap response.go has always used).
func truncateErrorBody(bodyText string) string {
	if len(bodyText) > 500 {
		return bodyText[:500]
	}
	return bodyText
}
