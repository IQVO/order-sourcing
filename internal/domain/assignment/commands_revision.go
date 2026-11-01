package assignment

import (
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Revision is a revised promise (PromiseRevised). WitnessNodeID empty keeps the witness.
type Revision struct {
	RevisionNo      int
	DeliverByLatest time.Time
	WitnessNodeID   string
}

// ApplyRevisedPromise applies a revised promise in every non-terminal state
// (G13). best is the search result under the new constraint with no exclusions;
// it is used in QUEUED and UNSOURCEABLE only.
func (a *Assignment) ApplyRevisedPromise(rev Revision, best *Candidate, releaseAt, now time.Time) ([]shared.Event, error) {
	if a.state.Terminal() {
		return nil, fmt.Errorf("%w: assignment %s is %s", shared.ErrIllegalTransition, a.id, a.state)
	}
	if rev.RevisionNo <= a.promise.RevisionNo {
		return nil, fmt.Errorf("%w: %d is not above %d", ErrStaleRevision, rev.RevisionNo, a.promise.RevisionNo)
	}
	next := a.promise
	next.RevisionNo, next.DeliverByLatest = rev.RevisionNo, shared.UTC(rev.DeliverByLatest)
	if rev.WitnessNodeID != "" {
		next.WitnessNodeID = rev.WitnessNodeID
	}
	if err := validatePromise(next); err != nil {
		return nil, err
	}
	prev, prevExcluded := a.promise, a.excluded
	a.promise, a.excluded = next, nil
	if best != nil && (a.state == StateQueued || a.state == StateUnsourceable) {
		if err := a.checkCandidate(best); err != nil {
			a.promise, a.excluded = prev, prevExcluded
			return nil, err
		}
	}
	a.touch(now)
	events := []shared.Event{FulfillmentConstraintRevised{Base: a.base(), RevisionNo: next.RevisionNo, DeliverByLatest: next.DeliverByLatest}}
	return append(events, a.reapply(best, releaseAt)...), nil
}

// reapply updates the candidate after a revision (already touched).
func (a *Assignment) reapply(best *Candidate, releaseAt time.Time) []shared.Event {
	switch a.state {
	case StateUnsourceable:
		if best == nil {
			return nil
		}
		a.state = StateQueued
		a.setCandidate(best, releaseAt, "", "")
	case StateQueued:
		if best == nil {
			a.markUnsourceable()
			return []shared.Event{a.unsourceable(ReasonNoCandidate)}
		}
		return a.setCandidate(best, releaseAt, a.candidate.NodeID, ReasonPromiseRevised)
	}
	return nil
}

// MovePickup points a LOCKED assignment at the departure its booking moved to
// (BookingMoved, G14). The node never changes. A move from a departure that
// is not the current one, or to the current one, returns no events.
func (a *Assignment) MovePickup(from, to string, now time.Time) error {
	if a.state != StateLocked {
		return fmt.Errorf("%w: assignment %s is %s, not LOCKED", shared.ErrIllegalTransition, a.id, a.state)
	}
	if !shared.ValidID(to) || (from != "" && !shared.ValidID(from)) {
		return shared.Invalid("departure ids %q and %q must be valid", from, to)
	}
	if to == a.pickupDeparture || (from != "" && from != a.pickupDeparture) {
		return nil
	}
	a.pickupDeparture = to
	a.candidate.PickupDepartureID = to
	a.touch(now)
	return nil
}
