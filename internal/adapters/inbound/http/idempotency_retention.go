package http

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// Execer is the part of a pgx pool the retention purge needs.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// PurgeExpiredIdempotencyKeys is not implemented yet: the red step of the retention fix.
func PurgeExpiredIdempotencyKeys(context.Context, Execer, int) (int, error) { return 0, nil }
