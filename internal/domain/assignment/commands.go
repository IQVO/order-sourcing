package assignment

import (
	"fmt"
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// touch bumps the version and the update instant. Every change calls it once.
func (a *Assignment) touch(now time.Time) {
	a.version++
	a.updatedAt = shared.UTC(now)
}

func (a *Assignment) base() Base { return newBase(a.id, a.version, a.updatedAt) }

func (a *Assignment) unsourceable(reason string) FulfillmentUnsourceable {
	return FulfillmentUnsourceable{
		Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, PromiseID: a.promise.PromiseID,
		ExcludedNodes: slices.Clone(a.excluded), Reason: reason,
	}
}

func (a *Assignment) reassigned(from, to, reason string) FulfillmentReassigned {
	return FulfillmentReassigned{Base: a.base(), FromNodeID: from, ToNodeID: to, Reason: reason}
}

// guardMovable returns nil in QUEUED and the model's error for every other state.
func (a *Assignment) guardMovable() error {
	switch a.state {
	case StateQueued:
		return nil
	case StateReleased:
		return fmt.Errorf("%w: assignment %s awaits release %d", ErrReleaseInFlight, a.id, len(a.releases))
	case StateLocked, StateShipped, StateDelivered:
		return fmt.Errorf("%w: assignment %s is %s", ErrAssignmentLocked, a.id, a.state)
	default:
		return fmt.Errorf("%w: assignment %s is %s", shared.ErrIllegalTransition, a.id, a.state)
	}
}

// toUnsourceable moves a QUEUED or RELEASED assignment to UNSOURCEABLE.
func (a *Assignment) toUnsourceable(reason string, now time.Time) shared.Event {
	a.markUnsourceable()
	a.touch(now)
	return a.unsourceable(reason)
}

func (a *Assignment) markUnsourceable() {
	a.state = StateUnsourceable
	a.candidate = nil
	a.releaseAt = time.Time{}
}

// setCandidate replaces the candidate and returns FulfillmentReassigned when the node changed.
func (a *Assignment) setCandidate(best *Candidate, releaseAt time.Time, from, reason string) []shared.Event {
	c := best.clone()
	a.candidate = &c
	a.releaseAt = shared.UTC(releaseAt)
	if from != "" && from != best.NodeID {
		return []shared.Event{a.reassigned(from, best.NodeID, reason)}
	}
	return nil
}

func sameReleaseAt(old, next, now time.Time) bool {
	return old.Equal(next) || (!old.After(now) && !next.After(now))
}

// Reoptimise applies the result of a new candidate search to a QUEUED
// assignment (ReoptimiseAssignment). best nil means the candidate set is empty
// (UNSOURCEABLE, ADR 0002 A4). expectedVersion 0 skips the caller's version
// check, otherwise a mismatch is ErrStaleAssignmentVersion. When nothing
// changed it returns no events and does not bump the version.
func (a *Assignment) Reoptimise(best *Candidate, releaseAt time.Time, expectedVersion int64, now time.Time) ([]shared.Event, error) {
	if err := a.guardMovable(); err != nil {
		return nil, err
	}
	if expectedVersion != 0 && expectedVersion != a.version {
		return nil, fmt.Errorf("%w: caller has %d, assignment is at %d", ErrStaleAssignmentVersion, expectedVersion, a.version)
	}
	if best == nil {
		return []shared.Event{a.toUnsourceable(ReasonNoCandidate, now)}, nil
	}
	if err := a.checkCandidate(best); err != nil {
		return nil, err
	}
	if a.candidate != nil && a.candidate.Same(*best) && sameReleaseAt(a.releaseAt, releaseAt, now) {
		return nil, nil
	}
	from := ""
	if a.candidate != nil {
		from = a.candidate.NodeID
	}
	a.touch(now)
	return a.setCandidate(best, releaseAt, from, ReasonReoptimised), nil
}

// Release sends the group to the node of best, the candidate re-checked
// against fresh read models (ReleaseToNode). lines carry the listingId of that
// node for every line. best nil means no candidate is left: UNSOURCEABLE.
func (a *Assignment) Release(best *Candidate, lines []Line, now time.Time) ([]shared.Event, error) {
	if err := a.guardMovable(); err != nil {
		return nil, err
	}
	if best == nil {
		return []shared.Event{a.toUnsourceable(ReasonNoCandidate, now)}, nil
	}
	if err := a.checkCandidate(best); err != nil {
		return nil, err
	}
	if err := a.checkReleaseLines(lines); err != nil {
		return nil, err
	}
	from := ""
	if a.candidate != nil {
		from = a.candidate.NodeID
	}
	a.touch(now)
	events := a.setCandidate(best, time.Time{}, from, ReasonReleaseRecheck)
	a.lines = cloneLines(lines)
	a.releases = append(a.releases, Release{
		No: len(a.releases) + 1, NodeID: best.NodeID, ReleasedAt: shared.UTC(now),
		RequiredShipBy: shared.UTC(best.PickupCutoffAt), Outcome: OutcomePending,
	})
	a.state = StateReleased
	rel := a.releases[len(a.releases)-1]
	return append(events, FulfillmentReleasedToNode{
		Base: a.base(), ReleaseNo: rel.No, OrderID: a.orderID, GroupNo: a.groupNo, NodeID: rel.NodeID,
		PickupDepartureID: best.PickupDepartureID, RequiredShipBy: rel.RequiredShipBy,
		PromisedDeliverByLatest: a.promise.DeliverByLatest, Tier: a.promise.Tier, Lines: cloneLines(a.lines),
	}), nil
}

// checkReleaseLines requires the same lines as the group, each with a listing.
func (a *Assignment) checkReleaseLines(lines []Line) error {
	if err := validateLines(lines); err != nil {
		return err
	}
	if len(lines) != len(a.lines) {
		return shared.Invalid("a release carries the whole group: %d lines, got %d", len(a.lines), len(lines))
	}
	for i, l := range lines {
		if l.ListingID == "" {
			return shared.Invalid("line %d has no listingId", i+1)
		}
		if l.CatalogItemID != a.lines[i].CatalogItemID || l.Quantity != a.lines[i].Quantity || l.LineNo != a.lines[i].LineNo {
			return shared.Invalid("line %d differs from the group's line", i+1)
		}
	}
	return nil
}

// pendingRelease returns the last release when it is PENDING with number no.
func (a *Assignment) pendingRelease(no int) (*Release, error) {
	if a.state != StateReleased || len(a.releases) == 0 {
		return nil, fmt.Errorf("%w: assignment %s is %s", ErrReleaseNotPending, a.id, a.state)
	}
	last := &a.releases[len(a.releases)-1]
	if last.No != no || last.Outcome != OutcomePending {
		return nil, fmt.Errorf("%w: release %d of assignment %s", ErrReleaseNotPending, no, a.id)
	}
	return last, nil
}

// RefuseRelease handles the node's refusal of release no (HandleNodeRefusal):
// outcome is REJECTED (with the vendor's reasonCode) or EXPIRED. The node is
// excluded; best is the next candidate or nil when none is left.
func (a *Assignment) RefuseRelease(no int, outcome Outcome, reasonCode string, best *Candidate, releaseAt, now time.Time) ([]shared.Event, error) {
	if a.state.Committed() {
		return nil, fmt.Errorf("%w: assignment %s is %s", ErrAssignmentLocked, a.id, a.state)
	}
	if outcome != OutcomeRejected && outcome != OutcomeExpired {
		return nil, shared.Invalid("outcome %q is not REJECTED or EXPIRED", outcome)
	}
	if reasonCode != "" && !shared.ValidTextN(reasonCode, MaxReason) {
		return nil, shared.Invalid("reasonCode must be 1 to %d printable characters", MaxReason)
	}
	rel, err := a.pendingRelease(no)
	if err != nil {
		return nil, err
	}
	reason, why := ReasonPurchaseOrderExpired, "EXPIRED"
	if outcome == OutcomeRejected {
		reason, why = ReasonPurchaseOrderRejected, "REJECTED"
		if reasonCode != "" {
			why += ":" + reasonCode
		}
	}
	rel.Outcome, rel.ReasonCode = outcome, reasonCode
	a.excluded = append(a.excluded, ExcludedNode{NodeID: rel.NodeID, Reason: why})
	if best == nil {
		return []shared.Event{a.toUnsourceable(ReasonNodeRefused, now)}, nil
	}
	if err := a.checkCandidate(best); err != nil {
		rel.Outcome, rel.ReasonCode = OutcomePending, ""
		a.excluded = a.excluded[:len(a.excluded)-1]
		return nil, err
	}
	a.state = StateQueued
	a.touch(now)
	return a.setCandidate(best, releaseAt, rel.NodeID, reason), nil
}

// Lock applies the node's acceptance of release no (LockAssignment). A repeat
// on an assignment already locked by that release returns no events.
func (a *Assignment) Lock(no int, acceptedAt, now time.Time) ([]shared.Event, error) {
	if a.state.Committed() {
		if n := len(a.releases); n > 0 && a.releases[n-1].No == no && a.releases[n-1].Outcome == OutcomeAccepted {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: assignment %s is %s", ErrAssignmentLocked, a.id, a.state)
	}
	if a.state == StateCancelled {
		return nil, fmt.Errorf("%w: assignment %s is CANCELLED", shared.ErrIllegalTransition, a.id)
	}
	rel, err := a.pendingRelease(no)
	if err != nil {
		return nil, err
	}
	rel.Outcome = OutcomeAccepted
	a.state = StateLocked
	a.lockedAt = shared.UTC(acceptedAt)
	if a.lockedAt.IsZero() {
		a.lockedAt = shared.UTC(now)
	}
	a.lockReason = LockReasonAccepted
	a.pickupDeparture = a.candidate.PickupDepartureID
	a.touch(now)
	return []shared.Event{FulfillmentLocked{
		Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, PromiseID: a.promise.PromiseID, NodeID: rel.NodeID,
		PickupDepartureID: a.pickupDeparture, LockedAt: a.lockedAt, LockReason: a.lockReason,
	}}, nil
}

// MarkShipped records the vendor's shipment (LOCKED to SHIPPED). A repeat on a
// SHIPPED or DELIVERED assignment returns no events.
func (a *Assignment) MarkShipped(trackingNumbers []string, shippedAt, now time.Time) ([]shared.Event, error) {
	switch a.state {
	case StateShipped, StateDelivered:
		return nil, nil
	case StateLocked:
	default:
		return nil, fmt.Errorf("%w: assignment %s is %s, not LOCKED", shared.ErrIllegalTransition, a.id, a.state)
	}
	if len(trackingNumbers) < 1 || len(trackingNumbers) > MaxTrackingNos {
		return nil, shared.Invalid("a shipment has 1 to %d tracking numbers", MaxTrackingNos)
	}
	for _, t := range trackingNumbers {
		if !shared.ValidTextN(t, MaxTrackingLen) {
			return nil, shared.Invalid("tracking number %q is not valid", t)
		}
	}
	a.state = StateShipped
	a.trackingNumbers = slices.Clone(trackingNumbers)
	a.shippedAt = shared.UTC(shippedAt)
	if a.shippedAt.IsZero() {
		a.shippedAt = shared.UTC(now)
	}
	a.touch(now)
	return []shared.Event{FulfillmentShipped{
		Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, NodeID: a.releases[len(a.releases)-1].NodeID,
		TrackingNumbers: slices.Clone(a.trackingNumbers), ShippedAt: a.shippedAt,
	}}, nil
}

func (a *Assignment) deliveredTracking(tn string) bool {
	return slices.ContainsFunc(a.delivered, func(p DeliveredPackage) bool { return p.TrackingNumber == tn })
}

// MarkDelivered records the delivery of one package of a SHIPPED group. The
// group is DELIVERED when its last package is delivered (ADR 0002 A8); before
// that only the progress is saved. A repeat returns no events.
func (a *Assignment) MarkDelivered(trackingNumber string, deliveredAt, now time.Time) ([]shared.Event, error) {
	switch a.state {
	case StateDelivered:
		return nil, nil
	case StateShipped:
	default:
		return nil, fmt.Errorf("%w: assignment %s is %s, not SHIPPED", shared.ErrIllegalTransition, a.id, a.state)
	}
	if !slices.Contains(a.trackingNumbers, trackingNumber) {
		return nil, shared.Invalid("tracking number %q is not on the shipment of %s", trackingNumber, a.id)
	}
	if a.deliveredTracking(trackingNumber) {
		return nil, nil
	}
	at := shared.UTC(deliveredAt)
	if at.IsZero() {
		at = shared.UTC(now)
	}
	a.delivered = append(a.delivered, DeliveredPackage{TrackingNumber: trackingNumber, DeliveredAt: at})
	a.touch(now)
	if len(a.delivered) < len(a.trackingNumbers) {
		return nil, nil
	}
	a.state = StateDelivered
	a.deliveredAt = at
	return []shared.Event{FulfillmentDelivered{Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, DeliveredAt: at}}, nil
}

// Cancel cancels a QUEUED, RELEASED or UNSOURCEABLE assignment
// (CancelAssignment; UNSOURCEABLE since G10). LOCKED and later is
// ErrCancelAfterLock; a repeat on CANCELLED returns no events. A PENDING
// release stays as it is: vendor-connectivity cancels the purchase order.
func (a *Assignment) Cancel(reason string, now time.Time) ([]shared.Event, error) {
	switch a.state {
	case StateCancelled:
		return nil, nil
	case StateLocked, StateShipped, StateDelivered:
		return nil, fmt.Errorf("%w: assignment %s is %s", ErrCancelAfterLock, a.id, a.state)
	}
	if !shared.ValidTextN(reason, MaxReason) {
		return nil, shared.Invalid("reason must be 1 to %d printable characters", MaxReason)
	}
	a.state = StateCancelled
	a.cancelReason = reason
	a.candidate = nil
	a.releaseAt = time.Time{}
	a.touch(now)
	return []shared.Event{FulfillmentCancelled{Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, Reason: reason}}, nil
}

// RefuseCancel answers a cancel request that came after the lock
// (contracts-delta E11): the state does not change, the version does.
func (a *Assignment) RefuseCancel(now time.Time) ([]shared.Event, error) {
	if !a.state.Committed() {
		return nil, fmt.Errorf("%w: assignment %s is %s, a cancel is not refused", shared.ErrIllegalTransition, a.id, a.state)
	}
	a.touch(now)
	return []shared.Event{FulfillmentCancelRefused{Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, Reason: ReasonCancelAfterLock}}, nil
}

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
