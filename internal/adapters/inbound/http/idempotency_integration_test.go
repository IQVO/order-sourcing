//go:build integration

package http_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	inhttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

func post(h http.Handler, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	if key != "" {
		req.Header.Set(inhttp.IdempotencyKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIdempotencyKeyOnPostgres(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	status := atomic.Int32{}
	status.Store(200)
	h := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"n":` + string(rune('0'+calls.Load())) + `}`))
	}))

	first := post(h, "/assignments/a/reoptimisations", "k1")
	again := post(h, "/assignments/a/reoptimisations", "k1")
	if first.Code != 200 || again.Code != 200 || first.Body.String() != again.Body.String() || calls.Load() != 1 {
		t.Fatalf("a repeated key replays the first answer: %d %q / %d %q calls=%d", first.Code, first.Body, again.Code, again.Body, calls.Load())
	}
	if again.Header().Get("Idempotency-Replayed") != "true" || again.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay headers = %v", again.Header())
	}
	other := post(h, "/assignments/b/reoptimisations", "k1")
	if other.Code != 409 || !strings.Contains(other.Body.String(), "idempotency-key-reused") || calls.Load() != 1 || other.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("the same key on another request is 409 idempotency-key-reused: %d %s calls=%d", other.Code, other.Body, calls.Load())
	}

	// A refusal that carries no replay class (this handler writes the status itself, not through
	// the catalogue) is released like a RETRYABLE one: the retry runs the handler again (ADR 0019).
	status.Store(409)
	c1, c2 := post(h, "/assignments/a/reoptimisations", "k409"), post(h, "/assignments/a/reoptimisations", "k409")
	if c1.Code != 409 || c2.Code != 409 || calls.Load() != 3 || c2.Header().Get("Idempotency-Replayed") != "" || pgtest.CountRows(t, pool, "idempotency_keys") != 1 {
		t.Fatalf("an unclassified 4xx is not stored: %d %d calls=%d rows=%d", c1.Code, c2.Code, calls.Load(), pgtest.CountRows(t, pool, "idempotency_keys"))
	}

	status.Store(500)
	post(h, "/assignments/a/reoptimisations", "k500")
	status.Store(200)
	if r := post(h, "/assignments/a/reoptimisations", "k500"); r.Code != 200 || r.Header().Get("Idempotency-Replayed") != "" || calls.Load() != 5 {
		t.Fatalf("a 5xx is never stored, the key retries the real work: %d calls=%d", r.Code, calls.Load())
	}
}

func TestConcurrentSameKeyRunsTheHandlerOnce(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	h := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); post(h, "/assignments/a/reoptimisations", "race") }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times", calls.Load())
	}
}

func TestPanicRollsBackAndRepanics(t *testing.T) {
	pool := pgtest.NewPool(t)
	h := inhttp.RequireIdempotencyKey(pool, postgres.BindTx)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic must propagate")
			}
		}()
		post(h, "/assignments/a/reoptimisations", "kp")
	}()
	if n := pgtest.CountRows(t, pool, "idempotency_keys"); n != 0 {
		t.Fatalf("a panic leaves no stored key, found %d", n)
	}
}
