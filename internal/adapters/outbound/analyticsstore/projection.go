package analyticsstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// Projection is the Postgres WRITER (report.Projection). Apply claims the
// event id and projects the fact in ONE transaction: the id is recorded if and
// only if its effect is, so a failure anywhere leaves nothing behind and the
// redelivered event is applied afresh, while a replay of an applied id is a
// no-op.
type Projection struct {
	pool *pgxpool.Pool
}

// NewProjection constructs a Projection over the writer pool.
func NewProjection(pool *pgxpool.Pool) *Projection { return &Projection{pool: pool} }

var _ report.Projection = (*Projection)(nil)

const claimSQL = `
	INSERT INTO analytics_processed_events (event_id, event_type, occurred_at)
	VALUES ($1, $2, $3)
	ON CONFLICT (event_id) DO NOTHING`

const (
	// A group is queued once; a republish of the same assignment keeps the first row.
	insertQueuedSQL = `
	INSERT INTO queued_groups (assignment_id, order_id, group_no, queued_at, event_id)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (assignment_id) DO NOTHING`

	// One decision per (assignment, release): a republish carries the same facts.
	upsertDecisionSQL = `
	INSERT INTO sourcing_decisions (assignment_id, release_no, order_id, group_no, node_id, node_region_id,
	    destination_region_id, in_region, legs, estimated_cost_cents, cost_complete, units, candidate_count, decided_at, event_id)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	ON CONFLICT (assignment_id, release_no) DO UPDATE
	SET order_id = EXCLUDED.order_id, group_no = EXCLUDED.group_no, node_id = EXCLUDED.node_id,
	    node_region_id = EXCLUDED.node_region_id, destination_region_id = EXCLUDED.destination_region_id,
	    in_region = EXCLUDED.in_region, legs = EXCLUDED.legs, estimated_cost_cents = EXCLUDED.estimated_cost_cents,
	    cost_complete = EXCLUDED.cost_complete, units = EXCLUDED.units, candidate_count = EXCLUDED.candidate_count,
	    decided_at = EXCLUDED.decided_at, event_id = EXCLUDED.event_id`

	insertUnsourceableSQL = `
	INSERT INTO unsourceable_declarations (event_id, assignment_id, order_id, declared_at)
	VALUES ($1, $2, $3, $4)`
)

// Apply implements report.Projection.
func (p *Projection) Apply(ctx context.Context, e report.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("analyticsstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	tag, err := tx.Exec(ctx, claimSQL, e.EventID, string(e.Kind), e.At)
	if err != nil {
		return false, classify(fmt.Errorf("analyticsstore: claim event %s: %w", e.EventID, err))
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already applied: the deferred rollback ends the empty tx
	}
	if err := project(ctx, tx, e); err != nil {
		return false, classify(fmt.Errorf("analyticsstore: project %s %s: %w", e.Kind, e.EventID, err))
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("analyticsstore: commit: %w", err)
	}
	return true, nil
}

// errUnknownKind is deterministic: no retry can make the store accept it.
var errUnknownKind = errors.New("unknown event kind")

func project(ctx context.Context, tx pgx.Tx, e report.Event) error {
	if e.Kind == report.KindFulfillmentQueued {
		_, err := tx.Exec(ctx, insertQueuedSQL, e.AssignmentID, e.OrderID, e.GroupNo, e.OccurredAt, e.EventID)
		return err
	}
	if e.Kind == report.KindFulfillmentDecided {
		d := e.Decision
		_, err := tx.Exec(ctx, upsertDecisionSQL, e.AssignmentID, d.ReleaseNo, e.OrderID, nullInt(e.GroupNo), d.NodeID,
			nullText(d.NodeRegionID), nullText(d.DestinationRegionID), d.InRegion, d.Legs, d.EstimatedCostCents, d.CostComplete,
			d.Units, d.CandidateCount, e.OccurredAt, e.EventID)
		return err
	}
	if e.Kind == report.KindFulfillmentUnsourceable {
		_, err := tx.Exec(ctx, insertUnsourceableSQL, e.EventID, e.AssignmentID, e.OrderID, e.OccurredAt)
		return err
	}
	return fmt.Errorf("%w %q", errUnknownKind, e.Kind)
}

// nullText stores "" (unknown) as NULL.
func nullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullInt stores 0 (omitted) as NULL.
func nullInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// classify wraps a Postgres data-exception (SQLSTATE class 22) or
// integrity-violation (class 23) error, and an unknown event kind, in
// report.ErrRejected: the same event can never succeed, so the consumer
// dead-letters it instead of retrying forever. Everything else (connection
// loss, timeouts, locks, failovers, a raised exception) stays transient.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.Is(err, errUnknownKind) || (errors.As(err, &pgErr) && deterministicClass(pgErr.Code)) {
		return fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	return err
}

// deterministicClass reports whether an SQLSTATE is in class 22 or 23.
func deterministicClass(code string) bool {
	return len(code) == 5 && (code[:2] == "22" || code[:2] == "23")
}
