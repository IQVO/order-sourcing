package assignment

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Snapshot is the persisted form of an assignment. Restore validates it.
type Snapshot struct {
	ID                      ID
	OrderID                 string
	GroupNo                 int
	DestinationPostalPrefix string
	Promise                 PromiseConstraint
	Lines                   []Line
	State                   State
	Candidate               *Candidate
	ReleaseAt               time.Time
	Releases                []Release
	ExcludedNodes           []ExcludedNode
	Version                 int64
	QueuedAt                time.Time
	LockedAt                time.Time
	LockReason              string
	PickupDepartureID       string
	TrackingNumbers         []string
	ShippedAt               time.Time
	Delivered               []DeliveredPackage
	DeliveredAt             time.Time
	CancelReason            string
	UpdatedAt               time.Time
}

// Snapshot returns the persisted form (deep copies).
func (a *Assignment) Snapshot() Snapshot {
	s := Snapshot{
		ID: a.id, OrderID: a.orderID, GroupNo: a.groupNo, DestinationPostalPrefix: a.destination, Promise: a.promise,
		Lines: cloneLines(a.lines), State: a.state, ReleaseAt: a.releaseAt, Releases: slices.Clone(a.releases),
		ExcludedNodes: slices.Clone(a.excluded), Version: a.version, QueuedAt: a.queuedAt, LockedAt: a.lockedAt,
		LockReason: a.lockReason, PickupDepartureID: a.pickupDeparture, TrackingNumbers: slices.Clone(a.trackingNumbers),
		ShippedAt: a.shippedAt, Delivered: slices.Clone(a.delivered), DeliveredAt: a.deliveredAt,
		CancelReason: a.cancelReason, UpdatedAt: a.updatedAt,
	}
	if a.candidate != nil {
		c := a.candidate.clone()
		s.Candidate = &c
	}
	return s
}

// Restore rebuilds a persisted assignment, validating every invariant.
func Restore(s Snapshot) (*Assignment, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("assignment %q: %v", s.ID, err)
	}
	a := &Assignment{
		id: s.ID, orderID: s.OrderID, groupNo: s.GroupNo, destination: s.DestinationPostalPrefix, promise: s.Promise,
		lines: cloneLines(s.Lines), state: s.State, releaseAt: shared.UTC(s.ReleaseAt), releases: slices.Clone(s.Releases),
		excluded: slices.Clone(s.ExcludedNodes), version: s.Version, queuedAt: shared.UTC(s.QueuedAt),
		lockedAt: shared.UTC(s.LockedAt), lockReason: s.LockReason, pickupDeparture: s.PickupDepartureID,
		trackingNumbers: slices.Clone(s.TrackingNumbers), shippedAt: shared.UTC(s.ShippedAt),
		delivered: slices.Clone(s.Delivered), deliveredAt: shared.UTC(s.DeliveredAt), cancelReason: s.CancelReason,
		updatedAt: shared.UTC(s.UpdatedAt),
	}
	a.promise.DeliverByLatest = shared.UTC(a.promise.DeliverByLatest)
	if s.Candidate != nil {
		c := s.Candidate.clone()
		a.candidate = &c
	}
	return a, nil
}

func checkSnapshot(s Snapshot) error {
	if err := validateNew(New{ID: s.ID, OrderID: s.OrderID, GroupNo: s.GroupNo, DestinationPostalPrefix: s.DestinationPostalPrefix,
		Promise: s.Promise, Lines: s.Lines}); err != nil {
		return err
	}
	if !s.State.Valid() {
		return fmt.Errorf("state %q is not a state of the model", s.State)
	}
	if s.Version < 1 || s.QueuedAt.IsZero() || s.UpdatedAt.IsZero() {
		return errors.New("a version of at least 1, queuedAt and updatedAt are required")
	}
	if err := checkReleases(s); err != nil {
		return err
	}
	if err := checkExcluded(s.ExcludedNodes); err != nil {
		return err
	}
	return checkState(s)
}

func checkReleases(s Snapshot) error {
	for i, r := range s.Releases {
		if r.No != i+1 {
			return fmt.Errorf("release at position %d is numbered %d", i, r.No)
		}
		if !shared.ValidID(r.NodeID) || r.ReleasedAt.IsZero() || r.RequiredShipBy.IsZero() {
			return fmt.Errorf("release %d needs nodeId, releasedAt and requiredShipBy", r.No)
		}
		if !r.Outcome.Valid() {
			return fmt.Errorf("release %d has outcome %q", r.No, r.Outcome)
		}
		if r.Outcome == OutcomePending && i != len(s.Releases)-1 {
			return fmt.Errorf("release %d is PENDING but not the last", r.No)
		}
	}
	return nil
}

func checkExcluded(ex []ExcludedNode) error {
	for i, e := range ex {
		if !shared.ValidID(e.NodeID) || !shared.ValidTextN(e.Reason, MaxReason) {
			return fmt.Errorf("excluded node at position %d needs a valid nodeId and reason", i)
		}
		if slices.ContainsFunc(ex[:i], func(o ExcludedNode) bool { return o.NodeID == e.NodeID }) {
			return fmt.Errorf("node %s is excluded twice", e.NodeID)
		}
	}
	return nil
}

func lastOutcome(s Snapshot) Outcome {
	if len(s.Releases) == 0 {
		return ""
	}
	return s.Releases[len(s.Releases)-1].Outcome
}

func checkState(s Snapshot) error {
	switch s.State {
	case StateQueued:
		return checkQueued(s)
	case StateReleased:
		return checkReleased(s)
	case StateLocked, StateShipped, StateDelivered:
		return checkCommitted(s)
	case StateUnsourceable:
		if s.Candidate != nil || !s.ReleaseAt.IsZero() {
			return errors.New("an UNSOURCEABLE assignment has no candidate and no releaseAt")
		}
		return nil
	default: // StateCancelled, the only state left
		if s.Candidate != nil || !s.ReleaseAt.IsZero() || !shared.ValidTextN(s.CancelReason, MaxReason) {
			return errors.New("a CANCELLED assignment has a cancel reason, no candidate and no releaseAt")
		}
		return nil
	}
}

func checkQueued(s Snapshot) error {
	if s.Candidate == nil || s.ReleaseAt.IsZero() || lastOutcome(s) == OutcomePending {
		return errors.New("a QUEUED assignment has a candidate and a releaseAt and no pending release")
	}
	if s.Candidate.ETA.After(s.Promise.DeliverByLatest) {
		return fmt.Errorf("%w: eta is after deliverByLatest", ErrCandidateBreaksPromise)
	}
	if slices.ContainsFunc(s.ExcludedNodes, func(e ExcludedNode) bool { return e.NodeID == s.Candidate.NodeID }) {
		return fmt.Errorf("%w: %s", ErrNodeExcluded, s.Candidate.NodeID)
	}
	return nil
}

func checkReleased(s Snapshot) error {
	if s.Candidate == nil || !s.ReleaseAt.IsZero() || lastOutcome(s) != OutcomePending {
		return errors.New("a RELEASED assignment has a candidate, no releaseAt and a pending last release")
	}
	if s.Releases[len(s.Releases)-1].NodeID != s.Candidate.NodeID {
		return errors.New("the pending release is not to the candidate node")
	}
	return nil
}

func checkCommitted(s Snapshot) error {
	if lastOutcome(s) != OutcomeAccepted || s.Candidate == nil || s.LockedAt.IsZero() || s.LockReason == "" ||
		!shared.ValidID(s.PickupDepartureID) || !s.ReleaseAt.IsZero() {
		return errors.New("a committed assignment has an accepted last release, a candidate, lockedAt, lockReason and a pickup departure")
	}
	if s.State == StateLocked {
		if len(s.TrackingNumbers) != 0 || !s.ShippedAt.IsZero() || len(s.Delivered) != 0 || !s.DeliveredAt.IsZero() {
			return errors.New("a LOCKED assignment has no shipment")
		}
		return nil
	}
	if len(s.TrackingNumbers) < 1 || s.ShippedAt.IsZero() {
		return errors.New("a SHIPPED or DELIVERED assignment has tracking numbers and shippedAt")
	}
	return checkDelivery(s)
}

func checkDelivery(s Snapshot) error {
	for _, p := range s.Delivered {
		if !slices.Contains(s.TrackingNumbers, p.TrackingNumber) || p.DeliveredAt.IsZero() {
			return fmt.Errorf("delivered package %q is not on the shipment or has no instant", p.TrackingNumber)
		}
	}
	all := len(s.Delivered) == len(s.TrackingNumbers)
	if s.State == StateShipped && (all || !s.DeliveredAt.IsZero()) {
		return errors.New("a SHIPPED assignment has packages left to deliver")
	}
	if s.State == StateDelivered && (!all || s.DeliveredAt.IsZero()) {
		return errors.New("a DELIVERED assignment delivered every package and has deliveredAt")
	}
	return nil
}
