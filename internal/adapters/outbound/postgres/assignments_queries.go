package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// List implements ports.AssignmentRepository.
func (r *AssignmentRepository) List(ctx context.Context, f repository.AssignmentFilter) ([]*assignment.Assignment, error) {
	limit := f.Limit
	if limit <= 0 || limit > repository.FetchLimit {
		limit = repository.FetchLimit
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, `SELECT `+assignmentColumns+` FROM assignments
		WHERE ($1 = '' OR state = $1) AND ($2 = '' OR node_id = $2) AND ($3 = '' OR order_id = $3)
		AND assignment_id > $4
		ORDER BY assignment_id LIMIT $5`, string(f.State), f.NodeID, f.OrderID, f.After, limit)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	defer rows.Close()
	out := []*assignment.Assignment{}
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *AssignmentRepository) ids(ctx context.Context, sql string, args ...any) ([]assignment.ID, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list assignment ids: %w", err)
	}
	defer rows.Close()
	out := []assignment.ID{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, assignment.ID(id))
	}
	return out, rows.Err()
}

// Due implements ports.AssignmentRepository.
func (r *AssignmentRepository) Due(ctx context.Context, now time.Time, limit int) ([]assignment.ID, error) {
	return r.ids(ctx, `SELECT assignment_id FROM assignments WHERE state = 'QUEUED' AND release_at <= $1
		ORDER BY release_at, assignment_id LIMIT $2`, now.UTC(), limit)
}

// Queued implements ports.AssignmentRepository.
func (r *AssignmentRepository) Queued(ctx context.Context, after assignment.ID, limit int) ([]assignment.ID, error) {
	return r.ids(ctx, `SELECT assignment_id FROM assignments WHERE state = 'QUEUED' AND assignment_id > $1
		ORDER BY assignment_id LIMIT $2`, string(after), limit)
}

// QueuedByKey implements ports.AssignmentRepository: the merge candidates of a
// carrier, served by assignments_consolidation_idx (ADR 0015).
func (r *AssignmentRepository) QueuedByKey(ctx context.Context, key string, excludeID assignment.ID, limit int) ([]*assignment.Assignment, error) {
	if key == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, `SELECT `+assignmentColumns+` FROM assignments
		WHERE state = 'QUEUED' AND consolidation_key = $1 AND consolidation_key <> '' AND assignment_id <> $2
		ORDER BY deliver_by_latest, assignment_id LIMIT $3`, key, string(excludeID), limit)
	if err != nil {
		return nil, fmt.Errorf("list queued assignments by key: %w", err)
	}
	defer rows.Close()
	var out []*assignment.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
