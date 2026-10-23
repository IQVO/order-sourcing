package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// SweepResult counts what one pass of the queue sweep did.
type SweepResult struct {
	Released    int
	Reoptimised int
	Skipped     int
	// Next is the cursor of the next re-optimisation page ("" after the last).
	Next assignment.ID
}

// SweepQueue is the in-process timer of the virtual queue: it releases the
// QUEUED assignments whose releaseAt has passed, and re-optimises a page of the
// others (VirtualQueue, ADR 0002 A18).
type SweepQueue struct {
	repo    ports.AssignmentRepository
	clock   ports.Clock
	release *ReleaseToNode
	reopt   *ReoptimiseAssignment
}

// NewSweepQueue builds the sweep.
func NewSweepQueue(d Deps, release *ReleaseToNode, reopt *ReoptimiseAssignment) *SweepQueue {
	return &SweepQueue{repo: d.Assignments, clock: d.Clock, release: release, reopt: reopt}
}

// Execute runs one pass: up to limit due releases, then up to limit
// re-optimisations after the cursor. A row another writer changed meanwhile
// (ports.ErrConcurrentUpdate) or that no longer exists is skipped; any other
// error is collected and returned with the result.
func (s *SweepQueue) Execute(ctx context.Context, after assignment.ID, limit int) (SweepResult, error) {
	var res SweepResult
	var errs []error
	due, err := s.repo.Due(ctx, s.clock.Now().UTC(), limit)
	if err != nil {
		return res, fmt.Errorf("list due assignments: %w", err)
	}
	for _, id := range due {
		s.count(&res, &res.Released, &errs, id, func() error { _, e := s.release.Execute(ctx, id); return e })
	}
	page, err := s.repo.Queued(ctx, after, limit)
	if err != nil {
		return res, errors.Join(append(errs, fmt.Errorf("list queued assignments: %w", err))...)
	}
	if len(page) == limit {
		res.Next = page[len(page)-1]
	}
	for _, id := range page {
		s.count(&res, &res.Reoptimised, &errs, id, func() error { _, e := s.reopt.Execute(ctx, id, 0); return e })
	}
	return res, errors.Join(errs...)
}

func (s *SweepQueue) count(res *SweepResult, counter *int, errs *[]error, id assignment.ID, run func() error) {
	err := run()
	switch {
	case err == nil:
		*counter++
	case errors.Is(err, ports.ErrConcurrentUpdate), errors.Is(err, ports.ErrNotFound), isStateRefusal(err):
		res.Skipped++
	default:
		*errs = append(*errs, fmt.Errorf("assignment %s: %w", id, err))
	}
}

// isStateRefusal reports the domain refusals that only say the row moved on.
func isStateRefusal(err error) bool {
	return errors.Is(err, assignment.ErrAssignmentLocked) || errors.Is(err, assignment.ErrReleaseInFlight)
}
