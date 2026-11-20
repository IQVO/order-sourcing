package http

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// IdempotencyPurgeBatch is how many expired key rows one purge statement deletes at most: the
// purge never holds a long lock nor writes a long transaction, however far behind it is.
const IdempotencyPurgeBatch = 500

// Execer is the part of a pgx pool the retention purge needs.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// purgeExpiredKeysSQL deletes one batch of rows the read check (replay) already ignores:
// created_at <= now() - ttl is the exact complement of replay's created_at > now() - ttl, and the
// ttl is the same IdempotencyTTL argument. The rows are picked with FOR UPDATE SKIP LOCKED, so a
// row a request holds is skipped, never waited for: a request that holds a key takes the advisory
// lock first and refreshes the row (store's ON CONFLICT DO UPDATE sets created_at = now()) under
// a row lock, and the predicate is checked again once the lock is taken. A purge that deletes an
// expired row just before store's upsert only makes the upsert an INSERT.
const purgeExpiredKeysSQL = `DELETE FROM idempotency_keys WHERE key IN (
	SELECT key FROM idempotency_keys WHERE created_at <= now() - $1::interval
	ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED
) AND created_at <= now() - $1::interval`

// PurgeExpiredIdempotencyKeys deletes up to limit expired idempotency_keys rows (older than
// IdempotencyTTL) and returns how many it deleted. A result below limit means nothing expired is
// left. Expiry is still enforced at read; the purge only stops the table from growing (ADR 0019,
// addendum 2026-11-20).
func PurgeExpiredIdempotencyKeys(ctx context.Context, db Execer, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tag, err := db.Exec(ctx, purgeExpiredKeysSQL, IdempotencyTTL.String(), limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
