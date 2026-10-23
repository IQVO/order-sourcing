// Package assignment is the FulfillmentAssignment aggregate: one shipment group
// of one customer order, its promise constraint, the current candidate node
// and path, and the history of releases (ADR 0002).
//
// States: QUEUED, RELEASED, LOCKED, SHIPPED, DELIVERED, UNSOURCEABLE, CANCELLED.
// Invariants (model names): a released candidate's eta <= deliverByLatest
// (ErrCandidateBreaksPromise); no change of node after LOCKED
// (ErrAssignmentLocked); a refusing node is not released to again for the same
// promise revision (ErrNodeExcluded); at most one release PENDING
// (ErrReleaseInFlight); assignmentVersion strictly increases
// (ErrStaleAssignmentVersion); cancel only before LOCKED (ErrCancelAfterLock).
package assignment

import (
	"slices"
	"time"
)

// ID identifies an assignment (assignmentId).
type ID string

// State is the lifecycle state of an assignment.
type State string

// The seven states of the model.
const (
	StateQueued       State = "QUEUED"
	StateReleased     State = "RELEASED"
	StateLocked       State = "LOCKED"
	StateShipped      State = "SHIPPED"
	StateDelivered    State = "DELIVERED"
	StateUnsourceable State = "UNSOURCEABLE"
	StateCancelled    State = "CANCELLED"
)

// Valid reports whether s is one of the seven states.
func (s State) Valid() bool {
	switch s {
	case StateQueued, StateReleased, StateLocked, StateShipped, StateDelivered, StateUnsourceable, StateCancelled:
		return true
	}
	return false
}

// Terminal reports whether no command leaves the state.
func (s State) Terminal() bool { return s == StateDelivered || s == StateCancelled }

// Committed reports whether the node can no longer change (LOCKED and later).
func (s State) Committed() bool { return s == StateLocked || s == StateShipped || s == StateDelivered }

// Tier is a service tier of the model.
type Tier string

// The four service tiers.
const (
	TierSameDay  Tier = "SAME_DAY"
	TierNextDay  Tier = "NEXT_DAY"
	TierTwoDay   Tier = "TWO_DAY"
	TierStandard Tier = "STANDARD"
)

// Valid reports whether t is one of the four tiers.
func (t Tier) Valid() bool {
	return t == TierSameDay || t == TierNextDay || t == TierTwoDay || t == TierStandard
}

// Outcome is the outcome of a release.
type Outcome string

// The four release outcomes.
const (
	OutcomePending  Outcome = "PENDING"
	OutcomeAccepted Outcome = "ACCEPTED"
	OutcomeRejected Outcome = "REJECTED"
	OutcomeExpired  Outcome = "EXPIRED"
)

// Valid reports whether o is one of the four outcomes.
func (o Outcome) Valid() bool {
	return o == OutcomePending || o == OutcomeAccepted || o == OutcomeRejected || o == OutcomeExpired
}

// Reasons of the events (ADR 0004; the model leaves the vocabularies open).
const (
	ReasonReoptimised           = "REOPTIMISED"
	ReasonReleaseRecheck        = "RELEASE_RECHECK"
	ReasonPurchaseOrderRejected = "PURCHASE_ORDER_REJECTED"
	ReasonPurchaseOrderExpired  = "PURCHASE_ORDER_EXPIRED"
	ReasonPromiseRevised        = "PROMISE_REVISED"
	ReasonNoCandidate           = "NO_CANDIDATE"
	ReasonNodeRefused           = "NODE_REFUSED"
	LockReasonAccepted          = "PURCHASE_ORDER_ACCEPTED"
	ReasonCancelAfterLock       = "CANCEL_AFTER_LOCK"
)

// Bounds, plain literals because Postgres limits them (ADR 0002, A21).
const (
	MaxInt32       = 2147483647
	MaxLines       = 200
	MaxLegs        = 10
	MaxTrackingNos = 100
	MaxPrefix      = 16
	MaxReason      = 200
	MaxTrackingLen = 64
)

// Line is one line of the group. ListingID is set from the release on (the
// listing of the released node).
type Line struct {
	LineNo        int
	CatalogItemID string
	ListingID     string
	Quantity      int
}

// PromiseConstraint is what the assignment must keep (model value object plus
// revisionNo, G13).
type PromiseConstraint struct {
	PromiseID       string
	Tier            Tier
	DeliverByLatest time.Time
	WitnessNodeID   string
	RevisionNo      int
}

// Candidate is the current best node and path (model value object plus the
// pickup cutoff, which is the requiredShipBy of a release).
type Candidate struct {
	NodeID            string
	PickupDepartureID string
	PickupCutoffAt    time.Time
	Legs              []string
	DeliveryWaveID    string
	ETA               time.Time
	Cost              int64
}

func (c Candidate) clone() Candidate {
	c.Legs = slices.Clone(c.Legs)
	return c
}

// Same reports whether two candidates are the same node, path, wave, eta and cost.
func (c Candidate) Same(o Candidate) bool {
	return c.NodeID == o.NodeID && c.PickupDepartureID == o.PickupDepartureID && c.PickupCutoffAt.Equal(o.PickupCutoffAt) &&
		slices.Equal(c.Legs, o.Legs) && c.DeliveryWaveID == o.DeliveryWaveID && c.ETA.Equal(o.ETA) && c.Cost == o.Cost
}

// Release is one release of the group to a node.
type Release struct {
	No             int
	NodeID         string
	ReleasedAt     time.Time
	RequiredShipBy time.Time
	Outcome        Outcome
	ReasonCode     string
}

// ExcludedNode is a node that refused the group for the current promise revision.
type ExcludedNode struct {
	NodeID string
	Reason string
}

// DeliveredPackage is a delivered package of a shipped group.
type DeliveredPackage struct {
	TrackingNumber string
	DeliveredAt    time.Time
}
