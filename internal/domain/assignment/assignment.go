package assignment

import (
	"fmt"
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Assignment is the aggregate root.
type Assignment struct {
	id              ID
	orderID         string
	groupNo         int
	destination     string
	promise         PromiseConstraint
	lines           []Line
	state           State
	candidate       *Candidate
	releaseAt       time.Time
	releases        []Release
	excluded        []ExcludedNode
	version         int64
	queuedAt        time.Time
	lockedAt        time.Time
	lockReason      string
	pickupDeparture string
	trackingNumbers []string
	shippedAt       time.Time
	delivered       []DeliveredPackage
	deliveredAt     time.Time
	cancelReason    string
	updatedAt       time.Time
	// The merge fields (ADR 0015): the optional customer-ordering key, the
	// carrier this assignment follows ("" unless a follower) and, on a carrier,
	// the follower ids in ascending order.
	consolidationKey string
	consolidatedInto ID
	members          []ID
}

// New is the input of Queue. ConsolidationKey is optional: "" never merges.
type New struct {
	ID                      ID
	OrderID                 string
	GroupNo                 int
	DestinationPostalPrefix string
	Promise                 PromiseConstraint
	Lines                   []Line
	ConsolidationKey        string
}

// Queue creates an assignment at version 1 and raises FulfillmentQueued. best
// is the best candidate of the search (nil when the set is empty: the
// assignment is created UNSOURCEABLE at once and FulfillmentUnsourceable
// follows, ADR 0002 A4); releaseAt is the VirtualQueue release point.
func Queue(in New, best *Candidate, releaseAt, now time.Time) (*Assignment, []shared.Event, error) {
	if err := validateNew(in); err != nil {
		return nil, nil, err
	}
	if best != nil && releaseAt.IsZero() {
		return nil, nil, shared.Invalid("releaseAt is required with a candidate")
	}
	a := &Assignment{
		id: in.ID, orderID: in.OrderID, groupNo: in.GroupNo, destination: in.DestinationPostalPrefix,
		promise: in.Promise, lines: cloneLines(in.Lines), state: StateQueued, version: 1,
		queuedAt: shared.UTC(now), updatedAt: shared.UTC(now), consolidationKey: in.ConsolidationKey,
	}
	a.promise.DeliverByLatest = shared.UTC(a.promise.DeliverByLatest)
	a.promise.DeliverByEarliest = shared.UTC(a.promise.DeliverByEarliest)
	queued := FulfillmentQueued{
		Base: newBase(a.id, 1, now), OrderID: a.orderID, GroupNo: a.groupNo, PromiseID: a.promise.PromiseID,
		DeliverByLatest: a.promise.DeliverByLatest, Lines: cloneLines(a.lines),
	}
	events := []shared.Event{queued}
	if best == nil {
		a.state = StateUnsourceable
		return a, append(events, a.unsourceable(ReasonNoCandidate)), nil
	}
	if err := a.checkCandidate(best); err != nil {
		return nil, nil, err
	}
	c := best.clone()
	a.candidate = &c
	a.releaseAt = shared.UTC(releaseAt)
	return a, events, nil
}

func validateNew(in New) error {
	if !shared.ValidID(string(in.ID)) {
		return shared.Invalid("assignmentId %q is not a valid id", in.ID)
	}
	if !shared.ValidID(in.OrderID) {
		return shared.Invalid("orderId %q is not a valid id", in.OrderID)
	}
	if in.GroupNo < 1 || in.GroupNo > MaxInt32 {
		return shared.Invalid("groupNo must be between 1 and %d", MaxInt32)
	}
	if !shared.ValidTextN(in.DestinationPostalPrefix, MaxPrefix) {
		return shared.Invalid("destinationPostalPrefix must be 1 to %d printable characters", MaxPrefix)
	}
	if err := validatePromise(in.Promise); err != nil {
		return err
	}
	if !validKey(in.ConsolidationKey) {
		return shared.Invalid("consolidationKey must be empty or 64 lower-case hexadecimal characters")
	}
	return validateLines(in.Lines)
}

func validatePromise(p PromiseConstraint) error {
	if !shared.ValidID(p.PromiseID) {
		return shared.Invalid("promiseId %q is not a valid id", p.PromiseID)
	}
	if !p.Tier.Valid() {
		return shared.Invalid("tier %q is not a service tier", p.Tier)
	}
	if p.DeliverByLatest.IsZero() {
		return shared.Invalid("deliverByLatest is required")
	}
	if !p.DeliverByEarliest.IsZero() && p.DeliverByEarliest.After(p.DeliverByLatest) {
		return shared.Invalid("deliverByEarliest must not be after deliverByLatest")
	}
	if p.WitnessNodeID != "" && !shared.ValidID(p.WitnessNodeID) {
		return shared.Invalid("witnessNodeId %q is not a valid id", p.WitnessNodeID)
	}
	if p.RevisionNo < 0 || p.RevisionNo > MaxInt32 {
		return shared.Invalid("revisionNo must be between 0 and %d", MaxInt32)
	}
	return nil
}

func validateLines(lines []Line) error {
	if len(lines) < 1 || len(lines) > MaxLines {
		return shared.Invalid("a group has 1 to %d lines", MaxLines)
	}
	for _, l := range lines {
		if l.LineNo < 0 || l.LineNo > MaxInt32 {
			return shared.Invalid("lineNo must be between 0 and %d", MaxInt32)
		}
		if !shared.ValidID(l.CatalogItemID) {
			return shared.Invalid("catalogItemId %q is not a valid id", l.CatalogItemID)
		}
		if l.ListingID != "" && !shared.ValidID(l.ListingID) {
			return shared.Invalid("listingId %q is not a valid id", l.ListingID)
		}
		if l.OwnerRef != "" && !shared.ValidID(l.OwnerRef) {
			return shared.Invalid("ownerRef %q is not a valid id", l.OwnerRef)
		}
		if l.Quantity < 1 || l.Quantity > MaxInt32 {
			return shared.Invalid("quantity must be between 1 and %d", MaxInt32)
		}
	}
	return nil
}

// checkEstimates refuses negative estimates: the cents (ADR 0011) and the
// grams of CO2e (ADR 0017) follow the same rule.
func (c Candidate) checkEstimates() error {
	if c.EstimatedCostCents < 0 {
		return shared.Invalid("estimatedCostCents must not be negative")
	}
	if c.EstimatedCo2eGrams < 0 {
		return shared.Invalid("estimatedCo2eGrams must not be negative")
	}
	return nil
}

// checkCandidate enforces the candidate invariants: a well-formed candidate,
// not an excluded node (ErrNodeExcluded), eta <= deliverByLatest
// (ErrCandidateBreaksPromise).
func (a *Assignment) checkCandidate(c *Candidate) error {
	if !shared.ValidID(c.NodeID) || !shared.ValidID(c.PickupDepartureID) || !shared.ValidID(c.DeliveryWaveID) {
		return shared.Invalid("a candidate needs valid nodeId, pickupDepartureId and deliveryWaveId")
	}
	if c.ETA.IsZero() || c.PickupCutoffAt.IsZero() {
		return shared.Invalid("a candidate needs eta and pickupCutoffAt")
	}
	if len(c.Legs) > MaxLegs || slices.ContainsFunc(c.Legs, func(l string) bool { return !shared.ValidID(l) }) {
		return shared.Invalid("a candidate has at most %d valid leg ids", MaxLegs)
	}
	if c.Cost < 0 {
		return shared.Invalid("cost must not be negative")
	}
	if err := c.checkEstimates(); err != nil {
		return err
	}
	if c.SurchargeCents < 0 || c.PolicyVersion < 0 || c.PolicyVersion > MaxInt32 {
		return shared.Invalid("surchargeCents must not be negative and policyVersion must be between 0 and %d", MaxInt32)
	}
	if a.isExcluded(c.NodeID) {
		return fmt.Errorf("%w: %s", ErrNodeExcluded, c.NodeID)
	}
	if c.ETA.After(a.promise.DeliverByLatest) {
		return fmt.Errorf("%w: eta %s is after deliverByLatest %s", ErrCandidateBreaksPromise,
			c.ETA.UTC().Format(time.RFC3339), a.promise.DeliverByLatest.UTC().Format(time.RFC3339))
	}
	return nil
}

func (a *Assignment) isExcluded(node string) bool {
	return slices.ContainsFunc(a.excluded, func(e ExcludedNode) bool { return e.NodeID == node })
}

func cloneLines(in []Line) []Line { return slices.Clone(in) }

// ID returns the assignment id.
func (a *Assignment) ID() ID { return a.id }

// OrderID returns the customer order id.
func (a *Assignment) OrderID() string { return a.orderID }

// GroupNo returns the shipment group number.
func (a *Assignment) GroupNo() int { return a.groupNo }

// DestinationPostalPrefix returns the destination's postal prefix.
func (a *Assignment) DestinationPostalPrefix() string { return a.destination }

// Promise returns the promise constraint.
func (a *Assignment) Promise() PromiseConstraint { return a.promise }

// Lines returns a copy of the lines.
func (a *Assignment) Lines() []Line { return cloneLines(a.lines) }

// State returns the lifecycle state.
func (a *Assignment) State() State { return a.state }

// Candidate returns a copy of the current candidate, if any.
func (a *Assignment) Candidate() (Candidate, bool) {
	if a.candidate == nil {
		return Candidate{}, false
	}
	return a.candidate.clone(), true
}

// ReleaseAt returns the VirtualQueue release point (zero unless QUEUED).
func (a *Assignment) ReleaseAt() time.Time { return a.releaseAt }

// Releases returns a copy of the release history.
func (a *Assignment) Releases() []Release { return slices.Clone(a.releases) }

// ExcludedNodes returns a copy of the excluded nodes.
func (a *Assignment) ExcludedNodes() []ExcludedNode { return slices.Clone(a.excluded) }

// Version returns the assignmentVersion.
func (a *Assignment) Version() int64 { return a.version }

// QueuedAt returns when the assignment was created.
func (a *Assignment) QueuedAt() time.Time { return a.queuedAt }

// LockedAt returns when the assignment was locked (zero before).
func (a *Assignment) LockedAt() time.Time { return a.lockedAt }

// LockReason returns why it locked.
func (a *Assignment) LockReason() string { return a.lockReason }

// PickupDepartureID returns the pickup departure of the locked path.
func (a *Assignment) PickupDepartureID() string { return a.pickupDeparture }

// TrackingNumbers returns a copy of the shipped tracking numbers.
func (a *Assignment) TrackingNumbers() []string { return slices.Clone(a.trackingNumbers) }

// ShippedAt returns when the vendor shipped (zero before).
func (a *Assignment) ShippedAt() time.Time { return a.shippedAt }

// DeliveredPackages returns a copy of the packages delivered so far.
func (a *Assignment) DeliveredPackages() []DeliveredPackage { return slices.Clone(a.delivered) }

// DeliveredAt returns when the last package was delivered (zero before).
func (a *Assignment) DeliveredAt() time.Time { return a.deliveredAt }

// CancelReason returns why the assignment was cancelled.
func (a *Assignment) CancelReason() string { return a.cancelReason }

// UpdatedAt returns the instant of the last change.
func (a *Assignment) UpdatedAt() time.Time { return a.updatedAt }

// CurrentNodeID is the node the assignment is on: the candidate node while it
// has a candidate, otherwise the node of the latest release, otherwise "".
func (a *Assignment) CurrentNodeID() string {
	if a.candidate != nil {
		return a.candidate.NodeID
	}
	if last, ok := a.lastRelease(); ok {
		return last.NodeID
	}
	return ""
}
