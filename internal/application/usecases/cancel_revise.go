package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// DefaultCancelReason is used when CustomerOrderCancelled carries no reason.
const DefaultCancelReason = "UNSPECIFIED"

// CancelAssignmentCommand is the input of CancelAssignment (CustomerOrderCancelled).
type CancelAssignmentCommand struct {
	Meta
	OrderID  string
	GroupNos []int
	Reason   string
}

// CancelAssignment answers a cancel request (contracts-delta E11).
type CancelAssignment struct{ writer }

// NewCancelAssignment builds the use case.
func NewCancelAssignment(d Deps) *CancelAssignment { return &CancelAssignment{writer{d}} }

// Execute cancels every listed group of the order: FulfillmentCancelled before
// the lock, FulfillmentCancelRefused after it (the state does not change). A
// group this service never queued is skipped.
func (uc *CancelAssignment) Execute(ctx context.Context, cmd CancelAssignmentCommand) ([]views.Assignment, error) {
	reason := cmd.Reason
	if reason == "" {
		reason = DefaultCancelReason
	}
	return transact(ctx, uc.UoW, func(ctx context.Context) ([]views.Assignment, error) {
		if done, err := uc.claim(ctx, cmd.Meta); err != nil || done {
			return nil, err
		}
		out := make([]views.Assignment, 0, len(cmd.GroupNos))
		for _, g := range cmd.GroupNos {
			a, err := uc.Assignments.FindByGroup(ctx, cmd.OrderID, g)
			if ok, err := found(err); err != nil {
				return nil, err
			} else if !ok {
				continue
			}
			if err := uc.cancelOne(ctx, a, reason); err != nil {
				return nil, err
			}
			out = append(out, views.AssignmentFrom(a))
		}
		return out, nil
	})
}

func (uc *CancelAssignment) cancelOne(ctx context.Context, a *assignment.Assignment, reason string) error {
	before, now := a.Version(), uc.now()
	events, err := a.Cancel(reason, now)
	if errors.Is(err, assignment.ErrCancelAfterLock) {
		events, err = a.RefuseCancel(now)
	}
	if err != nil {
		return err
	}
	return uc.persist(ctx, a, before, events)
}

// ApplyRevisedPromiseCommand is the input of ApplyRevisedPromise (PromiseRevised).
type ApplyRevisedPromiseCommand struct {
	Meta
	PromiseID       string
	RevisionNo      int
	DeliverByLatest time.Time
	WitnessNodeID   string
}

// ApplyRevisedPromise reacts to PromiseRevised (gap G13).
type ApplyRevisedPromise struct{ writer }

// NewApplyRevisedPromise builds the use case.
func NewApplyRevisedPromise(d Deps) *ApplyRevisedPromise { return &ApplyRevisedPromise{writer{d}} }

// Execute applies the revision to the assignment of the promise in every
// non-terminal state. A revision not above the applied one is ignored.
func (uc *ApplyRevisedPromise) Execute(ctx context.Context, cmd ApplyRevisedPromiseCommand) (views.Assignment, error) {
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.Assignment, error) {
		if done, err := uc.claim(ctx, cmd.Meta); err != nil || done {
			return views.Assignment{}, err
		}
		a, err := uc.Assignments.FindByPromise(ctx, cmd.PromiseID)
		if err != nil {
			return views.Assignment{}, err
		}
		before := a.Version()
		events, err := uc.apply(ctx, a, cmd)
		if errors.Is(err, assignment.ErrStaleRevision) {
			return views.AssignmentFrom(a), nil
		}
		if err != nil {
			return views.Assignment{}, err
		}
		if err := uc.persist(ctx, a, before, events); err != nil {
			return views.Assignment{}, err
		}
		return views.AssignmentFrom(a), nil
	})
}

func (uc *ApplyRevisedPromise) apply(ctx context.Context, a *assignment.Assignment, cmd ApplyRevisedPromiseCommand) ([]shared.Event, error) {
	if err := validateRevision(cmd); err != nil {
		return nil, err
	}
	now := uc.now()
	rev := assignment.Revision{RevisionNo: cmd.RevisionNo, DeliverByLatest: cmd.DeliverByLatest, WitnessNodeID: cmd.WitnessNodeID}
	var cand *assignment.Candidate
	var releaseAt time.Time
	if st := a.State(); st == assignment.StateQueued || st == assignment.StateUnsourceable {
		// The search runs under the NEW constraint and with no exclusions (they reset with the revision).
		next := a.Promise()
		next.DeliverByLatest, next.RevisionNo = cmd.DeliverByLatest, cmd.RevisionNo
		if cmd.WitnessNodeID != "" {
			next.WitnessNodeID = cmd.WitnessNodeID
		}
		if cmd.RevisionNo > a.Promise().RevisionNo {
			best, _, err := uc.bestOption(ctx, now, a.DestinationPostalPrefix(), next, a.Lines(), nil)
			if err != nil {
				return nil, err
			}
			cand, releaseAt = candidateOf(best)
		}
	}
	return a.ApplyRevisedPromise(rev, cand, releaseAt, now)
}

func validateRevision(cmd ApplyRevisedPromiseCommand) error {
	if cmd.DeliverByLatest.IsZero() {
		return shared.Invalid("deliverByLatest is required")
	}
	return nil
}
