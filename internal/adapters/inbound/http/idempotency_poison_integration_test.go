//go:build integration

package http

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/order-sourcing/internal/application/ports"
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

func keyRow(t *testing.T, a replayAdapter, key string) int { return a.KeyRows(t, key) }

// A FINAL refusal written from an ABORTED transaction is stored: ROLLBACK TO SAVEPOINT heals the
// transaction, discards the handler's partial writes and the key row commits.
func TestAFinalRefusalFromAnAbortedTransactionCommitsTheOutcome(t *testing.T) {
	a := newReplayAdapter(t)
	var calls atomic.Int32
	mw := RequireIdempotencyKey(a.(*repoAdapter).pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		poisonTx(t, r)
		writeError(w, r, errMalformedRequest)
	}))

	first := post(t, mw, "k-poison", `{}`)
	if first.Code != http.StatusBadRequest || keyRow(t, a, "k-poison") != 1 {
		t.Fatalf("first = %d %s rows=%d, want the truthful 400 stored", first.Code, first.Body, keyRow(t, a, "k-poison"))
	}
	if n := keyRow(t, a, "dup"); n != 0 {
		t.Fatalf("the handler's partial writes survived: %d", n)
	}
	replay := post(t, mw, "k-poison", `{}`)
	if replay.Code != http.StatusBadRequest || !replayed(replay) || replay.Body.String() != first.Body.String() || calls.Load() != 1 {
		t.Fatalf("replay = %d replayed=%v calls=%d, want the stored 400 without re-running the handler", replay.Code, replayed(replay), calls.Load())
	}
}

// A RETRYABLE refusal from an aborted transaction (the real shape of a lost race) is released:
// the whole transaction rolls back, the client sees the truthful 409 and the retry executes.
func TestARetryableRefusalFromAnAbortedTransactionReleasesTheKey(t *testing.T) {
	a := newReplayAdapter(t)
	var calls atomic.Int32
	mw := RequireIdempotencyKey(a.(*repoAdapter).pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r)
			writeError(w, r, ports.ErrConcurrentUpdate)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	first := post(t, mw, "k-lost", `{}`)
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), "concurrent-update") || keyRow(t, a, "k-lost") != 0 {
		t.Fatalf("first = %d %s rows=%d, want the truthful 409 and no key row", first.Code, first.Body, keyRow(t, a, "k-lost"))
	}
	if retry := post(t, mw, "k-lost", `{}`); retry.Code != http.StatusOK || replayed(retry) || calls.Load() != 2 || keyRow(t, a, "k-lost") != 1 {
		t.Fatalf("retry = %d replayed=%v calls=%d, want the work executed and then stored", retry.Code, replayed(retry), calls.Load())
	}
}

func TestA2xxFromAnAbortedTransactionIsNeverCached(t *testing.T) {
	a := newReplayAdapter(t)
	var calls atomic.Int32
	mw := RequireIdempotencyKey(a.(*repoAdapter).pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))

	first := post(t, mw, "k-lie", `{}`)
	if first.Code != http.StatusInternalServerError || keyRow(t, a, "k-lie") != 0 {
		t.Fatalf("first = %d %s rows=%d, want 500 and no stored row", first.Code, first.Body, keyRow(t, a, "k-lie"))
	}
	second := post(t, mw, "k-lie", `{}`)
	if second.Code != http.StatusCreated || replayed(second) || calls.Load() != 2 {
		t.Fatalf("retry = %d calls=%d, want the real work to succeed", second.Code, calls.Load())
	}
}
