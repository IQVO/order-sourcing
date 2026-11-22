package http

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// spyExec records the purge statement and answers with a scripted row count.
type spyExec struct {
	sql      string
	args     []any
	affected int64
	err      error
	calls    int
}

func (s *spyExec) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	s.calls++
	s.sql, s.args = sql, args
	return pgconn.NewCommandTag("DELETE " + strconv.FormatInt(s.affected, 10)), s.err
}

// The purge uses the SAME window as the read check (replay): one constant, one interval argument.
func TestThePurgeUsesTheTTLOfTheReadCheck(t *testing.T) {
	db := &spyExec{affected: 3}
	n, err := PurgeExpiredIdempotencyKeys(context.Background(), db, 50)
	if n != 3 || err != nil {
		t.Fatalf("purge = %d, %v, want the rows the server deleted", n, err)
	}
	if len(db.args) != 2 || db.args[0] != IdempotencyTTL.String() || db.args[1] != 50 {
		t.Fatalf("args = %v, want the TTL %q and the batch limit 50", db.args, IdempotencyTTL.String())
	}
	for _, want := range []string{"DELETE FROM idempotency_keys", "created_at <= now() - $1::interval", "LIMIT $2", "FOR UPDATE SKIP LOCKED"} {
		if !strings.Contains(db.sql, want) {
			t.Errorf("purge SQL lacks %q:\n%s", want, db.sql)
		}
	}
}

// What the purge keeps is exactly what replay answers: replay reads "created_at > now() - ttl",
// the purge deletes "created_at <= now() - ttl", nothing in between.
func TestThePurgeDeletesOnlyWhatReplayIgnores(t *testing.T) {
	tx := &fakeTx{scanErr: pgx.ErrNoRows}
	_, _, _ = replay(context.Background(), tx, nil, "k", "fp")
	db := &spyExec{}
	_, _ = PurgeExpiredIdempotencyKeys(context.Background(), db, 1)
	if !strings.Contains(tx.querySQL, "created_at > now() - $2::interval") || strings.Count(db.sql, "created_at <= now() - $1::interval") != 2 {
		t.Fatalf("read: %s\npurge: %s", tx.querySQL, db.sql)
	}
}

func TestThePurgeWithNoBatchDoesNothing(t *testing.T) {
	for _, limit := range []int{0, -1} {
		db := &spyExec{}
		if n, err := PurgeExpiredIdempotencyKeys(context.Background(), db, limit); n != 0 || err != nil || db.calls != 0 {
			t.Errorf("limit %d: %d, %v, %d statements; want nothing run", limit, n, err, db.calls)
		}
	}
}

func TestAFailedPurgeReportsTheErrorAndNoRows(t *testing.T) {
	boom := errors.New("boom")
	if n, err := PurgeExpiredIdempotencyKeys(context.Background(), &spyExec{affected: 9, err: boom}, 10); n != 0 || !errors.Is(err, boom) {
		t.Fatalf("purge = %d, %v, want 0 and the error", n, err)
	}
}
