package usecases

import (
	"context"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Queries bundles the two read use cases (GET /assignments, GET /assignments/{id}).
type Queries struct{ repo ports.AssignmentRepository }

// NewQueries builds the read use cases.
func NewQueries(repo ports.AssignmentRepository) *Queries { return &Queries{repo: repo} }

// ListAssignments returns the assignments matching the filter, ordered by id
// ascending, strictly after f.After. Limit is the fetch size (0 = DefaultLimit; a page
// asks for page size + 1, hence FetchLimit). An unknown state or a limit outside
// 0..FetchLimit is shared.ErrValidation.
func (q *Queries) ListAssignments(ctx context.Context, f repository.AssignmentFilter) ([]views.Assignment, error) {
	if f.State != "" && !f.State.Valid() {
		return nil, shared.Invalid("state %q is not an assignment state", f.State)
	}
	if f.Limit < 0 || f.Limit > repository.FetchLimit {
		return nil, shared.Invalid("limit must be between 1 and %d", repository.MaxLimit)
	}
	if f.Limit == 0 {
		f.Limit = repository.DefaultLimit
	}
	list, err := q.repo.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]views.Assignment, 0, len(list))
	for _, a := range list {
		out = append(out, views.AssignmentFrom(a))
	}
	return out, nil
}

// GetAssignment returns one assignment, or ports.ErrNotFound.
func (q *Queries) GetAssignment(ctx context.Context, id assignment.ID) (views.Assignment, error) {
	a, err := q.repo.Get(ctx, id)
	if err != nil {
		return views.Assignment{}, err
	}
	return views.AssignmentFrom(a), nil
}
