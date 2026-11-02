package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The unit tests in this file cover the pieces of the idempotency middleware
// that need no Postgres: the response capture, flush, and the store/replay SQL
// against a scripted pgx.Tx. The full transactional behaviour (advisory lock,
// commit-together-with-the-handler, 24h replay window) is covered by the
// integration suite (idempotency_integration_test.go).

// fakeTx is a pgx.Tx that records Exec calls and answers QueryRow from a
// scripted row. Every other method is out of scope for these tests.
type fakeTx struct {
	execSQL  []string
	execArgs [][]any

	querySQL  string
	queryArgs []any
	scanFrom  []any // values Scan copies into the destinations
	scanErr   error // error Scan returns (pgx.ErrNoRows for a miss)
}

func (t *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.execSQL = append(t.execSQL, sql)
	t.execArgs = append(t.execArgs, args)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (t *fakeTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	t.querySQL, t.queryArgs = sql, args
	return fakeRow{from: t.scanFrom, err: t.scanErr}
}

type fakeRow struct {
	from []any
	err  error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		if i >= len(r.from) {
			break
		}
		switch p := dest[i].(type) {
		case *string:
			*p = r.from[i].(string)
		case *[]byte:
			*p = []byte(r.from[i].(string))
		case *int:
			*p = r.from[i].(int)
		}
	}
	return nil
}

func (t *fakeTx) Begin(context.Context) (pgx.Tx, error) { panic("not needed") }
func (t *fakeTx) Commit(context.Context) error          { panic("not needed") }
func (t *fakeTx) Rollback(context.Context) error        { panic("not needed") }
func (t *fakeTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("not needed")
}
func (t *fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { panic("not needed") }
func (t *fakeTx) LargeObjects() pgx.LargeObjects                         { panic("not needed") }
func (t *fakeTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("not needed")
}
func (t *fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) { panic("not needed") }
func (t *fakeTx) Conn() *pgx.Conn                                         { panic("not needed") }

var _ pgx.Tx = (*fakeTx)(nil)

// TestCaptureRecordsFirstStatusAndBody pins the capture contract the
// transactional replay relies on: the FIRST status code wins (a handler that
// writes twice must not rewrite history) and a body-only write counts as 200.
func TestCaptureRecordsFirstStatusAndBody(t *testing.T) {
	c := &capture{header: http.Header{}}
	c.WriteHeader(409)
	c.WriteHeader(200)
	if c.code != 409 {
		t.Fatalf("code = %d, want the first status 409", c.code)
	}
	if _, err := c.Write([]byte("no")); err != nil {
		t.Fatal(err)
	}
	c2 := &capture{header: http.Header{}}
	if _, err := c2.Write([]byte("body only")); err != nil {
		t.Fatal(err)
	}
	if c2.code != http.StatusOK {
		t.Fatalf("code = %d, a body without WriteHeader is a 200", c2.code)
	}
	if c2.body.String() != "body only" {
		t.Fatalf("body = %q", c2.body.String())
	}
}

// TestFlushCopiesHeadersStatusAndBody pins flush: everything the handler
// recorded reaches the client, header values included.
func TestFlushCopiesHeadersStatusAndBody(t *testing.T) {
	rec := &capture{header: http.Header{}}
	rec.header.Set("Content-Type", "application/json")
	rec.header.Add("X-Trace", "a")
	rec.WriteHeader(201)
	_, _ = rec.body.WriteString(`{"ok":true}`)

	w := httptest.NewRecorder()
	flush(w, rec)
	if w.Code != 201 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("flushed = %d %q", w.Code, w.Body)
	}
	if w.Header().Get("Content-Type") != "application/json" || len(w.Header().Values("X-Trace")) != 1 {
		t.Fatalf("headers = %v", w.Header())
	}
}

// TestStoreWritesUpsertWithResponse pins store's SQL contract: one
// idempotency_keys upsert keyed by key alone carrying status, body and
// headers, so a retried key refreshes the same row instead of failing.
func TestStoreWritesUpsertWithResponse(t *testing.T) {
	tx := &fakeTx{}
	rec := &capture{header: http.Header{}}
	rec.header.Set("Content-Type", "application/json")
	rec.WriteHeader(200)
	_, _ = rec.body.WriteString(`{"v":1}`)

	if err := store(context.Background(), tx, "key-1", "POST /assignments/a/reoptimisations abc", rec); err != nil {
		t.Fatalf("store = %v", err)
	}
	if len(tx.execSQL) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(tx.execSQL))
	}
	sql := tx.execSQL[0]
	for _, want := range []string{"INSERT INTO idempotency_keys", "ON CONFLICT (key) DO UPDATE"} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql = %q, want it to contain %q", sql, want)
		}
	}
	args := tx.execArgs[0]
	if len(args) != 5 || args[0] != "key-1" || args[1] != "POST /assignments/a/reoptimisations abc" || args[2] != 200 {
		t.Fatalf("args = %v, want key, fingerprint, status...", args)
	}
	if string(args[3].([]byte)) != `{"v":1}` {
		t.Errorf("stored body = %v", args[3])
	}
	var headers http.Header
	if err := json.Unmarshal(args[4].([]byte), &headers); err != nil {
		t.Fatalf("stored headers are not JSON: %v", err)
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Errorf("stored headers = %v", headers)
	}
}

// TestReplayAnswersFromTheStoredRowAndMarksTheHeader pins replay: a miss (no
// row yet) reports false with no error and writes nothing; a hit replays the
// stored status, body and headers and marks Idempotency-Replayed; a storage
// error is returned.
func TestReplayAnswersFromTheStoredRowAndMarksTheHeader(t *testing.T) {
	ctx := context.Background()

	// Miss: nothing stored yet.
	tx := &fakeTx{scanErr: pgx.ErrNoRows}
	w := httptest.NewRecorder()
	replayed, reused, err := replay(ctx, tx, w, "k", "fp")
	if replayed || reused || err != nil || w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("miss: replayed=%v err=%v code=%d body=%q", replayed, err, w.Code, w.Body)
	}

	// Hit: the stored outcome is replayed verbatim.
	tx = &fakeTx{scanFrom: []any{"fp", 409, `{"type":"x"}`, `{"Content-Type":["application/problem+json"]}`}}
	w = httptest.NewRecorder()
	replayed, reused, err = replay(ctx, tx, w, "k", "fp")
	if !replayed || reused || err != nil {
		t.Fatalf("hit: replayed=%v err=%v", replayed, err)
	}
	if w.Code != 409 || w.Body.String() != `{"type":"x"}` {
		t.Fatalf("replayed = %d %q", w.Code, w.Body)
	}
	if w.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("stored headers were not restored: %v", w.Header())
	}
	if w.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("the replay must be marked with Idempotency-Replayed")
	}

	// Storage failure.
	tx = &fakeTx{scanErr: errors.New("connection refused")}
	if _, _, err := replay(ctx, tx, httptest.NewRecorder(), "k", "fp"); err == nil {
		t.Fatal("a lookup error must surface, not count as a miss")
	}
}

// TestReplayQueryFiltersByKeyAndTTL pins the SELECT: it scopes to the key
// alone (global key) and applies the 24h window, so an old key is a miss again.
func TestReplayQueryFiltersByKeyAndTTL(t *testing.T) {
	tx := &fakeTx{scanErr: pgx.ErrNoRows}
	_, _, _ = replay(context.Background(), tx, httptest.NewRecorder(), "k2", "fp")
	if !strings.Contains(tx.querySQL, "FROM idempotency_keys") || !strings.Contains(tx.querySQL, "now() - $2::interval") {
		t.Fatalf("sql = %q", tx.querySQL)
	}
	if len(tx.queryArgs) != 2 || tx.queryArgs[0] != "k2" {
		t.Fatalf("args = %v", tx.queryArgs)
	}
	if d, ok := tx.queryArgs[1].(string); !ok || d != idempotencyTTL.String() {
		t.Fatalf("ttl arg = %v, want the 24h idempotency window", tx.queryArgs[1])
	}
}

// TestReplayFlagsReuseOfAKeyForAnotherRequest: the same key with another fingerprint is reuse, nothing is written.
func TestReplayFlagsReuseOfAKeyForAnotherRequest(t *testing.T) {
	tx := &fakeTx{scanFrom: []any{"other", 200, `{}`, `{}`}}
	w := httptest.NewRecorder()
	replayed, reused, err := replay(context.Background(), tx, w, "k", "fp")
	if replayed || !reused || err != nil || w.Body.Len() != 0 {
		t.Fatalf("reuse: replayed=%v reused=%v err=%v", replayed, reused, err)
	}
}
