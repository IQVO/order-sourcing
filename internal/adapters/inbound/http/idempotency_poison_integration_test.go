//go:build integration

package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	inhttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// The tcpostgres reference keeps the testcontainers postgres module import
// reachable in this package's directory (required by internal/architecture's
// TestPostgresIntegrationTestsUseTestcontainers for any integration test
// package that talks to pgx).
var _ = tcpostgres.Run

// poisonTx simulates what a mapped domain conflict does to the middleware's
// transaction: a statement fails on the server (a unique violation, exactly
// like a racing write translated to a typed conflict), the handler answers a
// truthful 4xx -- but the transaction is ABORTED from then on. The middleware
// must still record and return that 4xx, never a 500.
func poisonTx(t *testing.T, r *http.Request) {
	t.Helper()
	tx, ok := pgtx.From(r.Context())
	if !ok {
		t.Fatal("the middleware did not bind a transaction into the request context")
	}
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(r.Context(), `INSERT INTO idempotency_keys (key, fingerprint, status, body) VALUES ('dup', 'f', 200, '')`); err != nil {
			return // the second insert aborts the transaction (23505)
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

func conflictHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poisonTx(t, r)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"https://errors.order-sourcing.warehouse-systems.dev/conflict","title":"Conflict","status":409}`))
	})
}

func keyedRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/assignments/a/reoptimisations", strings.NewReader(`{}`))
	req.Header.Set(inhttp.IdempotencyKeyHeader, key)
	return req
}

// TestIdempotency_A409FromAnAbortedTransactionCommitsTheOutcome: a conflict
// that loses on a unique index answers 409, not 500, and that 409 is stored
// and replayed -- a retry with the same key must NEVER re-run the handler.
func TestIdempotency_A409FromAnAbortedTransactionCommitsTheOutcome(t *testing.T) {
	pool := pgtest.NewPool(t)
	mw := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(conflictHandler(t))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyedRequest("k-poison"))
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the truthful 409", first.Code, first.Body.String())
	}

	var statusCode int
	if err := pool.QueryRow(t.Context(), `SELECT status FROM idempotency_keys WHERE key = 'k-poison'`).Scan(&statusCode); err != nil {
		t.Fatalf("the 409 outcome was not committed: %v", err)
	}
	if statusCode != http.StatusConflict {
		t.Fatalf("stored status = %d", statusCode)
	}

	replay := httptest.NewRecorder()
	mw.ServeHTTP(replay, keyedRequest("k-poison"))
	if replay.Code != http.StatusConflict || replay.Header().Get(inhttp.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay = %d replayed=%q, want the stored 409", replay.Code, replay.Header().Get(inhttp.IdempotencyReplayedHeader))
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
}

// TestIdempotency_A2xxFromAnAbortedTransactionIsNeverStored: a success answer
// whose writes died with the transaction is a LIE if returned or stored.
// Roll everything back and fail the request, so the same key retries the
// real work.
func TestIdempotency_A2xxFromAnAbortedTransactionIsNeverStored(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r) // only the first attempt dies inside the transaction
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"reoptimisationId":"r1"}`))
	})
	mw := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(handler)

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyedRequest("k-lie"))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d %s, want 500 (the writes never happened)", first.Code, first.Body.String())
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM idempotency_keys WHERE key = 'k-lie'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a failed success left %d idempotency rows: the key could not retry", n)
	}

	second := httptest.NewRecorder()
	mw.ServeHTTP(second, keyedRequest("k-lie"))
	if second.Code != http.StatusCreated {
		t.Fatalf("retry = %d %s, want the real work to succeed", second.Code, second.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2 (the first attempt was discarded)", calls.Load())
	}
}
