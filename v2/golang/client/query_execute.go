package client

// query_execute.go — executing the structured AST query (POST /api/v1/query)
// and decoding its envelope.
//
// Domain: ONE endpoint, end to end. The grammar itself lives next door —
// query_builder.go builds the QueryRequest, query_validation.go enforces the
// column/operator/pagination whitelists locally. This file only spends the
// request and turns the reply into records.
//
// Split out of parity.go on 2026-09-14 (house 300-line rule): parity.go was
// already 532 lines, and the Query contract needed to GROW to document what the
// server does and does not accept on this route. Growing a file already past
// the cut is forbidden, so the domain moved out first.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v3/models"
)

// Query executes a structured AST query via POST /api/v1/query and returns the
// matching records as a flat slice. This is the MCP execute_ast contract: a
// whitelisted filter/sort/pagination grammar over the record columns, evaluated
// server-side as SQL.
//
// The request is the QueryRequest struct (filters/sort/pagination/select). Build
// it directly, or with the small fluent helpers (NewQuery().Where(...).
// OrderBy(...).Limit(...)).
//
//	req := client.NewQuery().
//	    Where("type", client.QueryOp{Eq: "fact"}).
//	    Where("score", client.QueryOp{Gte: 7}).
//	    OrderBy("created_at", "desc").
//	    Limit(20)
//	records, _ := mem.Query(ctx, req)
//
// Junior Tip [why this method takes NO ReadOption, 2026-09-14]: it used to end
// with `opts ...ReadOption` and a bare `_ = opts` in the body. WithAsOf,
// WithSince, WithUntil and WithKeyword therefore COMPILED, were accepted, and
// were thrown away before the request was built — a caller who scoped a query
// to a point in time silently got the unscoped query instead. The variadic is
// gone so the compiler refuses what the endpoint cannot do.
//
// This route has no temporal or keyword surface to honour, and that is not an
// assumption — it was probed against the production router on 2026-09-14
// (read-only, owner key):
//
//   - `?as_of=`, `?since=`, `?until=` and `?q=` on the POST URL changed nothing:
//     the same filter returned the identical count with and without each of
//     them, including a `since` in the year 2030 that would have emptied the
//     page had it been read. The handler reads the BODY and nothing else
//     (AnhurDB/server/handler/record_query.go).
//   - `as_of` as a top-level body key is silently ignored: the grammar has four
//     top-level keys (select, filters, sort, pagination) and drops the rest.
//
// Nor is as-of expressible as filters. The server's real point-in-time
// predicate (AnhurDB/server/database/list_record.go, GetRecordAsOf) is
// `created_at <= T AND (valid_from IS NULL OR valid_from <= T) AND
// (valid_until IS NULL OR valid_until > T)` plus an unwind of the supersede
// chain. The AST grammar has no OR, no IS NULL and no subquery, and `$eq: null`
// compiles to `col = ?` bound to NULL — never true. Live proof from the same
// probe: against a tenant where the unfiltered page returns 1000 rows,
// `valid_until $gt "2020-01-01T00:00:00Z"` returned 0, and
// `superseded_by $eq null` also returned 0 even though EVERY row the server
// returns already satisfies `superseded_by IS NULL`. A "best effort"
// translation would therefore not under-scope the answer, it would ANNIHILATE
// it, with HTTP 200 and no warning — the exact silent failure the variadic was
// removed to stop.
//
// since/until alone WOULD translate (`created_at $gte` / `$lte`, confirmed
// working live), but they are deliberately not wired either: callers already
// express that as a first-class filter, and a hidden second channel writing the
// same column would silently AND against an explicit created_at filter and
// return an empty page for a query that looks correct. One way to say one
// thing.
//
// Callers who need a point-in-time read must use a route that has one —
// ManifestGlobal / ManifestSession accept WithAsOf/WithSince/WithUntil and the
// server enforces the as_of-XOR-since/until rule there.
func (m *Memory) Query(ctx context.Context, request *QueryRequest) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if request == nil {
		// Typed like every other client-side rejection (B2, 2026-09-14): the
		// message is unchanged, but errors.As now yields *APIError with
		// Kind()==KindInvalidRequest and Retryable()==false.
		return nil, newValidationError("Query: request is required")
	}
	// Junior Tip [validar ANTES de gastar a requisicao — paridade com Python/TS]:
	// campo fora da whitelist, operador ausente, $in vazio, limit/offset fora de
	// faixa: os tres SDKs agora recusam no cliente. Sem isto, o Go pagava um
	// round-trip para receber um 400 que ele ja tinha informacao para prever, e
	// ate 2026-07-28 nem 400 recebia — o servidor descartava o predicado e
	// devolvia uma listagem SEM FILTRO, que e a resposta errada mais cara que
	// existe: parece certa.
	if validationErr := request.Validate(); validationErr != nil {
		return nil, validationErr
	}

	// The AST is a read behind POST.
	respBytes, postErr := m.conn.PostRead(ctx, "/api/v1/query", request)
	if postErr != nil {
		return nil, postErr
	}

	var wrapped queryResponse
	if decodeErr := json.Unmarshal(respBytes, &wrapped); decodeErr != nil {
		return nil, fmt.Errorf("parsing query response: %w", decodeErr)
	}
	// records:null (empty result set) decodes to a nil slice; normalise to an
	// empty slice so callers can range without a nil guard.
	if wrapped.Records == nil {
		return []models.Record{}, nil
	}
	return wrapped.Records, nil
}
