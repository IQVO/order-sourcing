package analyticsstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// Reader is the Postgres READER (report.Reader). Every query filters the
// business instant with `>= from AND < to`, and days are UTC calendar days.
// SQL only counts, sums and takes percentile_cont; the rule constants, the
// ranks and every derived figure (shares, means, rates) come from
// internal/analytics/report.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader constructs a Reader over the (read-only) reports pool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

var _ report.Reader = (*Reader)(nil)

// mixSQL: per UTC day of decidedAt the releases, their regions, legs and
// re-releases, joined with the unsourceable declarations of the same day.
//
//	$1 from, $2 to, $3 the first release number
const mixSQL = `
	WITH decisions AS (
		SELECT (decided_at AT TIME ZONE 'UTC')::date AS day,
		       count(*) AS releases,
		       count(*) FILTER (WHERE in_region) AS in_region,
		       count(*) FILTER (WHERE NOT in_region) AS out_region,
		       count(*) FILTER (WHERE in_region IS NULL) AS unknown_region,
		       sum(legs) AS legs,
		       count(*) FILTER (WHERE release_no > $3) AS reassignments
		FROM sourcing_decisions
		WHERE decided_at >= $1 AND decided_at < $2
		GROUP BY 1
	), unsourceable AS (
		SELECT (declared_at AT TIME ZONE 'UTC')::date AS day, count(*) AS n
		FROM unsourceable_declarations
		WHERE declared_at >= $1 AND declared_at < $2
		GROUP BY 1
	)
	SELECT COALESCE(d.day, u.day), COALESCE(d.releases, 0), COALESCE(d.in_region, 0), COALESCE(d.out_region, 0),
	       COALESCE(d.unknown_region, 0), COALESCE(d.legs, 0)::bigint, COALESCE(d.reassignments, 0), COALESCE(u.n, 0)
	FROM decisions d FULL OUTER JOIN unsourceable u ON u.day = d.day
	ORDER BY 1`

// SourcingMix implements report.Reader.
func (r *Reader) SourcingMix(ctx context.Context, rg report.Range) ([]report.MixDay, error) {
	return collect(ctx, r.pool, mixSQL, func(rows pgx.Rows) (report.MixDay, error) {
		var d report.MixDay
		err := rows.Scan(&d.Day, &d.Releases, &d.InRegion, &d.OutOfRegion, &d.UnknownRegion, &d.Legs, &d.Reassignments, &d.Unsourceable)
		d.Day = d.Day.UTC()
		return d, err
	}, rg.From, rg.To, report.FirstReleaseNo())
}

// splitsSQL: every order on the instant its first group was queued, with its
// distinct groups, then per day the orders, groups and split orders.
//
//	$1 from, $2 to, $3 the group count of an unsplit order
const splitsSQL = `
	WITH per_order AS (
		SELECT order_id, min(queued_at) AS first_at, count(DISTINCT group_no) AS groups
		FROM queued_groups
		GROUP BY order_id
	)
	SELECT (first_at AT TIME ZONE 'UTC')::date AS day, count(*), sum(groups)::bigint, count(*) FILTER (WHERE groups > $3)
	FROM per_order
	WHERE first_at >= $1 AND first_at < $2
	GROUP BY 1
	ORDER BY 1`

// OrderSplits implements report.Reader.
func (r *Reader) OrderSplits(ctx context.Context, rg report.Range) ([]report.SplitDay, error) {
	return collect(ctx, r.pool, splitsSQL, func(rows pgx.Rows) (report.SplitDay, error) {
		var d report.SplitDay
		err := rows.Scan(&d.Day, &d.Orders, &d.Groups, &d.SplitOrders)
		d.Day = d.Day.UTC()
		return d, err
	}, rg.From, rg.To, report.SingleGroup())
}

// costSQL: the LAST decision of every assignment (highest releaseNo), then per
// UTC day of its decidedAt the decisions, the complete ones, their cents and
// units, and the two cost percentiles over the complete ones.
//
//	$1 from, $2 to, $3 p50 rank, $4 p90 rank
const costSQL = `
	WITH last AS (
		SELECT DISTINCT ON (assignment_id) assignment_id, decided_at, cost_complete, estimated_cost_cents, units
		FROM sourcing_decisions
		ORDER BY assignment_id, release_no DESC
	)
	SELECT (decided_at AT TIME ZONE 'UTC')::date AS day, count(*), count(*) FILTER (WHERE cost_complete),
	       COALESCE(sum(estimated_cost_cents) FILTER (WHERE cost_complete), 0)::bigint,
	       COALESCE(sum(units) FILTER (WHERE cost_complete), 0)::bigint,
	       percentile_cont($3::float8) WITHIN GROUP (ORDER BY estimated_cost_cents::float8) FILTER (WHERE cost_complete),
	       percentile_cont($4::float8) WITHIN GROUP (ORDER BY estimated_cost_cents::float8) FILTER (WHERE cost_complete)
	FROM last
	WHERE decided_at >= $1 AND decided_at < $2
	GROUP BY 1
	ORDER BY 1`

// CostToServe implements report.Reader.
func (r *Reader) CostToServe(ctx context.Context, rg report.Range) ([]report.CostDay, error) {
	return collect(ctx, r.pool, costSQL, func(rows pgx.Rows) (report.CostDay, error) {
		var d report.CostDay
		err := rows.Scan(&d.Day, &d.Decisions, &d.Complete, &d.CostCents, &d.Units, &d.P50, &d.P90)
		d.Day = d.Day.UTC()
		return d, err
	}, rg.From, rg.To, report.CostP50(), report.CostP90())
}

// LastEventAt implements report.Reader: the newest CloudEvents time among the
// applied events, nil while none was applied.
func (r *Reader) LastEventAt(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	if err := r.pool.QueryRow(ctx, `SELECT max(occurred_at) FROM analytics_processed_events`).Scan(&at); err != nil {
		return nil, fmt.Errorf("analyticsstore: last event: %w", err)
	}
	if at != nil {
		utc := at.UTC()
		at = &utc
	}
	return at, nil
}

// Ping checks the analytical database answers (the reports' /readyz).
func (r *Reader) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }

// collect runs query with args and scans every row; the result is a non-nil
// slice even when empty.
func collect[T any](ctx context.Context, pool *pgxpool.Pool, query string, scan func(pgx.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: query: %w", err)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("analyticsstore: scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analyticsstore: rows: %w", err)
	}
	return out, nil
}
