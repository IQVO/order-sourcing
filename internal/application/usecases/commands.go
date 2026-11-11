package usecases

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// mutate loads one assignment, applies one domain command to it and persists
// the result with its events, in one unit of work with the inbox claim. A
// message already processed returns the zero view and nil.
func (w writer) mutate(
	ctx context.Context, m Meta, id assignment.ID,
	apply func(ctx context.Context, a *assignment.Assignment, now time.Time) ([]shared.Event, error),
) (views.Assignment, error) {
	return transact(ctx, w.UoW, func(ctx context.Context) (views.Assignment, error) {
		if done, err := w.claim(ctx, m); err != nil || done {
			return views.Assignment{}, err
		}
		a, err := w.Assignments.Get(ctx, id)
		if err != nil {
			return views.Assignment{}, err
		}
		before := a.Version()
		events, err := apply(ctx, a, w.now())
		if err != nil {
			return views.Assignment{}, err
		}
		if err := w.persist(ctx, a, before, events); err != nil {
			return views.Assignment{}, err
		}
		return views.AssignmentFrom(a), nil
	})
}

// ReoptimiseAssignment re-runs the candidate search of a QUEUED assignment.
type ReoptimiseAssignment struct{ writer }

// NewReoptimiseAssignment builds the use case.
func NewReoptimiseAssignment(d Deps) *ReoptimiseAssignment { return &ReoptimiseAssignment{writer{d}} }

// Execute re-optimises assignment id. expectedVersion 0 skips the caller's
// version check. Any state but QUEUED is the domain's error for that state.
func (uc *ReoptimiseAssignment) Execute(ctx context.Context, id assignment.ID, expectedVersion int64) (views.Assignment, error) {
	return uc.mutate(ctx, Meta{}, id, func(ctx context.Context, a *assignment.Assignment, now time.Time) ([]shared.Event, error) {
		if a.State() != assignment.StateQueued {
			return a.Reoptimise(nil, time.Time{}, expectedVersion, now) // the guard's error for this state
		}
		found, err := uc.search(ctx, a, now)
		if err != nil {
			return nil, err
		}
		cand, releaseAt := candidateOf(found.best)
		return a.Reoptimise(cand, releaseAt, expectedVersion, now)
	})
}

// HandleNodeRefusalCommand is the input of HandleNodeRefusal.
type HandleNodeRefusalCommand struct {
	Meta
	AssignmentID assignment.ID
	ReleaseNo    int
	Outcome      assignment.Outcome
	ReasonCode   string
}

// HandleNodeRefusal reacts to PurchaseOrderRejected and PurchaseOrderExpired.
type HandleNodeRefusal struct{ writer }

// NewHandleNodeRefusal builds the use case.
func NewHandleNodeRefusal(d Deps) *HandleNodeRefusal { return &HandleNodeRefusal{writer{d}} }

// Execute excludes the refusing node and re-queues on the next best candidate,
// or declares the group UNSOURCEABLE. The outcome of a release that is not the
// pending one is assignment.ErrReleaseNotPending. A refused carrier takes every
// follower of its merge with it: each follower refuses its own pending release,
// excludes the node and is re-searched independently (ADR 0015).
func (uc *HandleNodeRefusal) Execute(ctx context.Context, cmd HandleNodeRefusalCommand) (views.Assignment, error) {
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.Assignment, error) {
		if done, err := uc.claim(ctx, cmd.Meta); err != nil || done {
			return views.Assignment{}, err
		}
		a, err := uc.Assignments.Get(ctx, cmd.AssignmentID)
		if err != nil {
			return views.Assignment{}, err
		}
		if a.ConsolidatedInto() != "" {
			// The vendor only knows the carrier's id: an outcome on a follower's id is stale.
			return views.Assignment{}, fmt.Errorf("%w: assignment %s follows %s", assignment.ErrReleaseNotPending, a.ID(), a.ConsolidatedInto())
		}
		followers := a.Members()
		if err := uc.refuseOne(ctx, a, cmd.ReleaseNo, cmd); err != nil {
			return views.Assignment{}, err
		}
		for _, id := range followers {
			f, err := uc.Assignments.Get(ctx, id)
			if err != nil {
				return views.Assignment{}, err
			}
			rels := f.Releases()
			if err := uc.refuseOne(ctx, f, rels[len(rels)-1].No, cmd); err != nil {
				return views.Assignment{}, err
			}
		}
		return views.AssignmentFrom(a), nil
	})
}

// refuseOne applies the refusal of release no to one assignment and saves it.
func (uc *HandleNodeRefusal) refuseOne(ctx context.Context, a *assignment.Assignment, no int, cmd HandleNodeRefusalCommand) error {
	now := uc.now()
	var extra []assignment.ExcludedNode
	if rels := a.Releases(); a.State() == assignment.StateReleased && len(rels) > 0 {
		extra = append(extra, assignment.ExcludedNode{NodeID: rels[len(rels)-1].NodeID, Reason: string(cmd.Outcome)})
	}
	found, err := uc.search(ctx, a, now, extra...)
	if err != nil {
		return err
	}
	cand, releaseAt := candidateOf(found.best)
	before := a.Version()
	events, err := a.RefuseRelease(no, cmd.Outcome, cmd.ReasonCode, cand, releaseAt, now)
	if err != nil {
		return err
	}
	return uc.persist(ctx, a, before, events)
}

// LockAssignmentCommand is the input of LockAssignment.
type LockAssignmentCommand struct {
	Meta
	AssignmentID assignment.ID
	ReleaseNo    int
	AcceptedAt   time.Time
}

// LockAssignment reacts to PurchaseOrderAccepted.
type LockAssignment struct{ writer }

// NewLockAssignment builds the use case.
func NewLockAssignment(d Deps) *LockAssignment { return &LockAssignment{writer{d}} }

// Execute locks the assignment on the acceptance of its pending release; every
// follower of a merged carrier locks with it, each on its own pending release.
// An acceptance for a release that is no longer pending (a stale accept after a
// withdrawal) is counted and returned as assignment.ErrReleaseNotPending, which
// the consumer acknowledges (ADR 0015).
func (uc *LockAssignment) Execute(ctx context.Context, cmd LockAssignmentCommand) (views.Assignment, error) {
	v, err := uc.mutateMerged(ctx, cmd.Meta, cmd.AssignmentID,
		func(_ context.Context, a *assignment.Assignment, now time.Time) ([]shared.Event, error) {
			return a.Lock(cmd.ReleaseNo, cmd.AcceptedAt, now)
		},
		func(carrier, f *assignment.Assignment, now time.Time) ([]shared.Event, error) {
			return f.LockFollower(carrier.MergedAssignmentIDs(), cmd.AcceptedAt, now)
		})
	if errors.Is(err, assignment.ErrReleaseNotPending) {
		uc.consolidationMetrics().StaleAccept(ctx)
		slog.WarnContext(ctx, "acceptance for a release that is not pending", "assignmentId", cmd.AssignmentID, "releaseNo", cmd.ReleaseNo)
	}
	return v, err
}

// MarkShippedCommand is the input of MarkShipped.
type MarkShippedCommand struct {
	Meta
	AssignmentID    assignment.ID
	TrackingNumbers []string
	ShippedAt       time.Time
}

// MarkShipped reacts to VendorShipmentConfirmed.
type MarkShipped struct{ writer }

// NewMarkShipped builds the use case.
func NewMarkShipped(d Deps) *MarkShipped { return &MarkShipped{writer{d}} }

// Execute records the shipment of a LOCKED assignment; every follower of a
// merged carrier ships under the same tracking numbers (ADR 0015).
func (uc *MarkShipped) Execute(ctx context.Context, cmd MarkShippedCommand) (views.Assignment, error) {
	ship := func(a *assignment.Assignment, now time.Time) ([]shared.Event, error) {
		return a.MarkShipped(slices.Clone(cmd.TrackingNumbers), cmd.ShippedAt, now)
	}
	return uc.mutateMerged(ctx, cmd.Meta, cmd.AssignmentID,
		func(_ context.Context, a *assignment.Assignment, now time.Time) ([]shared.Event, error) {
			return ship(a, now)
		},
		func(_, f *assignment.Assignment, now time.Time) ([]shared.Event, error) { return ship(f, now) })
}

// MarkDeliveredCommand is the input of MarkDelivered.
type MarkDeliveredCommand struct {
	Meta
	AssignmentID   assignment.ID
	TrackingNumber string
	DeliveredAt    time.Time
}

// MarkDelivered reacts to PackageDelivered.
type MarkDelivered struct{ writer }

// NewMarkDelivered builds the use case.
func NewMarkDelivered(d Deps) *MarkDelivered { return &MarkDelivered{writer{d}} }

// Execute records one delivered package; the last one delivers the group. A
// merged carrier's followers record the same package (ADR 0015).
func (uc *MarkDelivered) Execute(ctx context.Context, cmd MarkDeliveredCommand) (views.Assignment, error) {
	deliver := func(a *assignment.Assignment, now time.Time) ([]shared.Event, error) {
		return a.MarkDelivered(cmd.TrackingNumber, cmd.DeliveredAt, now)
	}
	return uc.mutateMerged(ctx, cmd.Meta, cmd.AssignmentID,
		func(_ context.Context, a *assignment.Assignment, now time.Time) ([]shared.Event, error) {
			return deliver(a, now)
		},
		func(_, f *assignment.Assignment, now time.Time) ([]shared.Event, error) { return deliver(f, now) })
}

// RecordPickupMovedCommand is the input of RecordPickupMoved (BookingMoved).
type RecordPickupMovedCommand struct {
	Meta
	AssignmentID  assignment.ID
	FromDeparture string
	ToDeparture   string
}

// RecordPickupMoved follows a booking that linehaul moved (G14).
type RecordPickupMoved struct{ writer }

// NewRecordPickupMoved builds the use case.
func NewRecordPickupMoved(d Deps) *RecordPickupMoved { return &RecordPickupMoved{writer{d}} }

// Execute points a LOCKED assignment at the new pickup departure; the followers
// of a merged carrier follow the same booking (ADR 0015).
func (uc *RecordPickupMoved) Execute(ctx context.Context, cmd RecordPickupMovedCommand) (views.Assignment, error) {
	return uc.mutateMerged(ctx, cmd.Meta, cmd.AssignmentID,
		func(_ context.Context, a *assignment.Assignment, now time.Time) ([]shared.Event, error) {
			return nil, a.MovePickup(cmd.FromDeparture, cmd.ToDeparture, now)
		},
		func(_, f *assignment.Assignment, now time.Time) ([]shared.Event, error) {
			return nil, f.MovePickup(cmd.FromDeparture, cmd.ToDeparture, now)
		})
}

// RecordProcessed claims the inbox row of an event that needs no effect (for
// example BookingFailed, which is only logged by its consumer).
type RecordProcessed struct{ writer }

// NewRecordProcessed builds the use case.
func NewRecordProcessed(d Deps) *RecordProcessed { return &RecordProcessed{writer{d}} }

// Execute claims the event id; it reports false when it was seen before.
func (uc *RecordProcessed) Execute(ctx context.Context, m Meta) (bool, error) {
	return transact(ctx, uc.UoW, func(ctx context.Context) (bool, error) {
		done, err := uc.claim(ctx, m)
		return !done, err
	})
}
