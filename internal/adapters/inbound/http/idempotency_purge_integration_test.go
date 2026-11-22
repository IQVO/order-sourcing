//go:build integration

package http_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	inhttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// ADR 0019 addendum (retention): nothing used to delete an expired idempotency_keys row. These
// tests run the purge against a real Postgres, next to the real middleware.

const purgeRoute = "/assignments/asg-1/reoptimisations"

// seedKey stores a key row that was created age ago.
func seedKey(t *testing.T, db inhttp.Execer, key string, age time.Duration) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO idempotency_keys (key, fingerprint, status, body, created_at)
		VALUES ($1, 'fp', 200, '\x7b7d', now() - $2::interval)`, key, age.String()); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func TestPurgeDeletesExpiredKeysAndKeepsLiveOnes(t *testing.T) {
	pool := pgtest.NewPool(t)
	seedKey(t, pool, "expired-a", inhttp.IdempotencyTTL+time.Hour)
	seedKey(t, pool, "expired-b", 48*time.Hour)
	seedKey(t, pool, "live-a", time.Hour)
	seedKey(t, pool, "live-b", inhttp.IdempotencyTTL-time.Hour)

	n, err := inhttp.PurgeExpiredIdempotencyKeys(context.Background(), pool, 100)
	if err != nil || n != 2 {
		t.Fatalf("purge = %d, %v, want 2 expired rows deleted", n, err)
	}
	if got := pgtest.IdempotencyKeys(t, pool); !slices.Equal(got, []string{"live-a", "live-b"}) {
		t.Fatalf("rows left = %v, want only the live ones", got)
	}
	if n, err := inhttp.PurgeExpiredIdempotencyKeys(context.Background(), pool, 100); err != nil || n != 0 {
		t.Fatalf("a second purge = %d, %v, want 0: nothing live is ever deleted", n, err)
	}
}

func TestPurgeDeletesAtMostOneBatchPerCall(t *testing.T) {
	pool := pgtest.NewPool(t)
	for i := range 5 {
		seedKey(t, pool, fmt.Sprintf("old-%d", i), 30*time.Hour)
	}
	seedKey(t, pool, "live", time.Minute)

	if n, err := inhttp.PurgeExpiredIdempotencyKeys(context.Background(), pool, 2); err != nil || n != 2 {
		t.Fatalf("first batch = %d, %v, want 2", n, err)
	}
	if left := pgtest.CountRows(t, pool, "idempotency_keys"); left != 4 {
		t.Fatalf("rows after one batch of 2 = %d, want 4 (3 expired + 1 live)", left)
	}
	total := 2
	for {
		n, err := inhttp.PurgeExpiredIdempotencyKeys(context.Background(), pool, 2)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		total += n
	}
	if got := pgtest.IdempotencyKeys(t, pool); total != 5 || !slices.Equal(got, []string{"live"}) {
		t.Fatalf("purged %d rows, left %v, want 5 and only the live key", total, got)
	}
}

// A request holds the key (advisory lock) and, last, upserts its row. The purge must neither
// delete the row the request is about to refresh nor wait for the request: it skips a locked row.
func TestPurgeNeverDeletesARowARequestHoldsAndNeverWaitsForIt(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	seedKey(t, pool, "held", 30*time.Hour)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "held"); err != nil {
		t.Fatal(err)
	}
	// What store does: refresh the (expired) row; the row lock lives until the commit.
	if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (key, fingerprint, status, body, headers) VALUES ($1, 'fp2', 200, '\x7b7d', '{}')
		ON CONFLICT (key) DO UPDATE SET fingerprint = 'fp2', created_at = now()`, "held"); err != nil {
		t.Fatal(err)
	}

	purgeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if n, err := inhttp.PurgeExpiredIdempotencyKeys(purgeCtx, pool, 100); err != nil || n != 0 {
		t.Fatalf("purge while the request holds the row = %d, %v, want 0 and no wait", n, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := inhttp.PurgeExpiredIdempotencyKeys(ctx, pool, 100); err != nil || n != 0 {
		t.Fatalf("purge after the request committed = %d, %v: the row was refreshed, it is live", n, err)
	}
	if got := pgtest.IdempotencyKeys(t, pool); !slices.Equal(got, []string{"held"}) {
		t.Fatalf("rows = %v, want the refreshed key", got)
	}
}

// The real middleware on a key whose row has expired, while the purge runs at the same time: every
// request is a FRESH one (an expired row never replays), answers 200, and leaves exactly one live row.
func TestPurgeRacingARequestOnAnExpiredKeyKeepsTheRequestCorrect(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	h := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	const rounds = 25
	for i := range rounds {
		key := fmt.Sprintf("race-%d", i)
		seedKey(t, pool, key, 30*time.Hour)
		var wg sync.WaitGroup
		codes := make([]int, 3)
		for j := range codes {
			wg.Add(1)
			go func() { defer wg.Done(); codes[j] = post(h, purgeRoute, key).Code }()
		}
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = inhttp.PurgeExpiredIdempotencyKeys(context.Background(), pool, 100) }()
		wg.Wait()
		for _, c := range codes {
			if c != http.StatusOK {
				t.Fatalf("round %d: a request racing the purge answered %d, want 200", i, c)
			}
		}
	}
	if calls.Load() != rounds {
		t.Fatalf("the handler ran %d times for %d keys: an expired row must be a fresh request, once, and its duplicates replay", calls.Load(), rounds)
	}
	if left := pgtest.CountRows(t, pool, "idempotency_keys"); left != rounds {
		t.Fatalf("%d key rows left, want one live row per key (%d)", left, rounds)
	}
	if again := post(h, purgeRoute, "race-0"); again.Header().Get(inhttp.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("a repeat of a stored key replays: %v", again.Header())
	}
}
