//go:build integration

package http_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	inhttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// poisonTx simulates a mapped domain conflict: a statement fails on the server
// (a unique violation, like a lost race on a unique index) inside the
// middleware's transaction, leaving it ABORTED.
func poisonTx(t *testing.T, r *http.Request) {
	t.Helper()
	tx, ok := pgtx.From(r.Context())
	if !ok {
		t.Fatal("the middleware did not bind a transaction into the request context")
	}
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(r.Context(), `INSERT INTO idempotency_keys (key, fingerprint, status, body) VALUES ('dup', 'h', 200, ''::bytea)`); err != nil {
			return // the second insert aborts the transaction (23505)
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

func TestIdempotencyA409FromAnAbortedTransactionCommitsTheOutcome(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	h := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		poisonTx(t, r)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"concurrent-update","status":409}`))
	}))

	first := post(h, "/assignments/a/reoptimisations", "k-poison")
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the truthful 409", first.Code, first.Body)
	}
	var status int
	if err := pool.QueryRow(t.Context(), `SELECT status FROM idempotency_keys WHERE key = 'k-poison'`).Scan(&status); err != nil || status != http.StatusConflict {
		t.Fatalf("the 409 outcome was not committed: status=%d err=%v", status, err)
	}
	var dup int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM idempotency_keys WHERE key = 'dup'`).Scan(&dup); err != nil || dup != 0 {
		t.Fatalf("the handler's partial writes survived: %d err=%v", dup, err)
	}
	replay := post(h, "/assignments/a/reoptimisations", "k-poison")
	if replay.Code != http.StatusConflict || replay.Header().Get(inhttp.IdempotencyReplayedHeader) != "true" ||
		replay.Body.String() != first.Body.String() || calls.Load() != 1 {
		t.Fatalf("replay = %d %q replayed=%q calls=%d, want the cached 409 without re-running the handler",
			replay.Code, replay.Body, replay.Header().Get(inhttp.IdempotencyReplayedHeader), calls.Load())
	}
}

func TestIdempotencyA2xxFromAnAbortedTransactionIsNeverCached(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	h := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))

	first := post(h, "/assignments/a/reoptimisations", "k-lie")
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d %s, want 500", first.Code, first.Body)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM idempotency_keys WHERE key = 'k-lie'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a failed success left %d rows err=%v", n, err)
	}
	second := post(h, "/assignments/a/reoptimisations", "k-lie")
	if second.Code != http.StatusCreated || strings.Contains(second.Header().Get(inhttp.IdempotencyReplayedHeader), "true") || calls.Load() != 2 {
		t.Fatalf("retry = %d calls=%d, want the real work to succeed", second.Code, calls.Load())
	}
}
