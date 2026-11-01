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
			return shared.Invalid("a release line has no listingId")
		}
		if l.CatalogItemID != a.lines[i].CatalogItemID || l.Quantity != a.lines[i].Quantity || l.LineNo != a.lines[i].LineNo {
			return shared.Invalid("a release line differs from the group's line")
		}
	}
	return nil
}

// lastRelease returns the most recent release, if any.
func (a *Assignment) lastRelease() (Release, bool) {
	if len(a.releases) == 0 {
		return Release{}, false
	}
	return a.releases[len(a.releases)-1], true
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
