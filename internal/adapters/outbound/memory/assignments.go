package memory

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// AssignmentRepository is the in-memory ports.AssignmentRepository.
type AssignmentRepository struct{ s *Store }

func restore(snap assignment.Snapshot) (*assignment.Assignment, error) {
	return assignment.Restore(snap)
}

// Get implements ports.AssignmentRepository.
func (r *AssignmentRepository) Get(ctx context.Context, id assignment.ID) (*assignment.Assignment, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.assignments[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return restore(snap)
}

func (r *AssignmentRepository) find(match func(assignment.Snapshot) bool) (*assignment.Assignment, error) {
	for _, snap := range r.s.assignments {
		if match(snap) {
			return restore(snap)
		}
	}
	return nil, ports.ErrNotFound
}

// FindByGroup implements ports.AssignmentRepository.
func (r *AssignmentRepository) FindByGroup(ctx context.Context, orderID string, groupNo int) (*assignment.Assignment, error) {
	defer r.s.enter(ctx)()
	return r.find(func(s assignment.Snapshot) bool { return s.OrderID == orderID && s.GroupNo == groupNo })
}

// FindByPromise implements ports.AssignmentRepository.
func (r *AssignmentRepository) FindByPromise(ctx context.Context, promiseID string) (*assignment.Assignment, error) {
	defer r.s.enter(ctx)()
	return r.find(func(s assignment.Snapshot) bool { return s.Promise.PromiseID == promiseID })
}

func (r *AssignmentRepository) sorted(keep func(assignment.Snapshot) bool, less func(a, b assignment.Snapshot) int) []assignment.Snapshot {
	var out []assignment.Snapshot
	for _, snap := range r.s.assignments {
		if keep(snap) {
			out = append(out, snap)
		}
	}
	slices.SortFunc(out, less)
	return out
}

func byID(a, b assignment.Snapshot) int { return cmp.Compare(a.ID, b.ID) }

// List implements ports.AssignmentRepository.
func (r *AssignmentRepository) List(ctx context.Context, f repository.AssignmentFilter) ([]*assignment.Assignment, error) {
	defer r.s.enter(ctx)()
	limit := f.Limit
	if limit <= 0 || limit > repository.FetchLimit {
		limit = repository.FetchLimit
	}
	var out []*assignment.Assignment
	for _, snap := range r.sorted(func(s assignment.Snapshot) bool { return string(s.ID) > f.After && matches(s, f) }, byID) {
		if len(out) == limit {
			break
		}
		a, err := restore(snap)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func matches(snap assignment.Snapshot, f repository.AssignmentFilter) bool {
	if f.State != "" && snap.State != f.State {
		return false
	}
	if f.OrderID != "" && snap.OrderID != f.OrderID {
		return false
	}
	if f.NodeID == "" {
		return true
	}
	a, err := restore(snap)
	return err == nil && a.CurrentNodeID() == f.NodeID
}

// Due implements ports.AssignmentRepository.
func (r *AssignmentRepository) Due(ctx context.Context, now time.Time, limit int) ([]assignment.ID, error) {
	defer r.s.enter(ctx)()
	due := r.sorted(func(s assignment.Snapshot) bool { return s.State == assignment.StateQueued && !s.ReleaseAt.After(now) },
		func(a, b assignment.Snapshot) int { return cmp.Or(a.ReleaseAt.Compare(b.ReleaseAt), byID(a, b)) })
	return firstIDs(due, limit), nil
}

// Queued implements ports.AssignmentRepository.
func (r *AssignmentRepository) Queued(ctx context.Context, after assignment.ID, limit int) ([]assignment.ID, error) {
	defer r.s.enter(ctx)()
	queued := r.sorted(func(s assignment.Snapshot) bool { return s.State == assignment.StateQueued && s.ID > after }, byID)
	return firstIDs(queued, limit), nil
}

func firstIDs(snaps []assignment.Snapshot, limit int) []assignment.ID {
	out := make([]assignment.ID, 0, min(limit, len(snaps)))
	for _, s := range snaps {
		if len(out) == limit {
			break
		}
		out = append(out, s.ID)
	}
	return out
}

// Save implements ports.AssignmentRepository.
func (r *AssignmentRepository) Save(ctx context.Context, a *assignment.Assignment) error {
	defer r.s.enter(ctx)()
	snap := a.Snapshot()
	stored, exists := r.s.assignments[snap.ID]
	switch {
	case !exists && snap.Version != 1, exists && stored.Version != snap.Version-1:
		return ports.ErrConcurrentUpdate
	case !exists:
		for _, o := range r.s.assignments {
			if o.OrderID == snap.OrderID && o.GroupNo == snap.GroupNo {
				return ports.ErrDuplicateGroup
			}
		}
	}
	r.s.assignments[snap.ID] = snap
	return nil
}
