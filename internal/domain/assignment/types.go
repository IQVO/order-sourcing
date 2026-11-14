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

// The service tiers: four classic ones and CONSOLIDATED, the customer-chosen
// consolidated delivery day (ADR 0015, ADR 0016).
const (
	TierSameDay      Tier = "SAME_DAY"
	TierNextDay      Tier = "NEXT_DAY"
	TierTwoDay       Tier = "TWO_DAY"
	TierStandard     Tier = "STANDARD"
	TierConsolidated Tier = "CONSOLIDATED"
)

// Valid reports whether t is one of the five tiers.
func (t Tier) Valid() bool {
	return t == TierSameDay || t == TierNextDay || t == TierTwoDay || t == TierStandard || t == TierConsolidated
}

// FulfilmentClass is the tier the release carries on the wire: vendor
// connectivity and shipping know only the four classic tiers, so CONSOLIDATED
// is released as STANDARD (ADR 0015).
func (t Tier) FulfilmentClass() Tier {
	if t == TierConsolidated {
		return TierStandard
	}
	return t
}

// Outcome is the outcome of a release.
type Outcome string

// The five release outcomes. WITHDRAWN is a release that order-sourcing took
// back because the merge it belonged to was dissolved (ADR 0015).
const (
	OutcomePending   Outcome = "PENDING"
	OutcomeAccepted  Outcome = "ACCEPTED"
	OutcomeRejected  Outcome = "REJECTED"
	OutcomeExpired   Outcome = "EXPIRED"
	OutcomeWithdrawn Outcome = "WITHDRAWN"
)

// Valid reports whether o is one of the five outcomes.
func (o Outcome) Valid() bool {
	return o == OutcomePending || o == OutcomeAccepted || o == OutcomeRejected || o == OutcomeExpired || o == OutcomeWithdrawn
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
	// ReasonCancelWhileMerged refuses a cancel of a member of a live merge that
	// is not yet LOCKED; ReasonConsolidationWithdrawn is the reassignment reason
	// of a member re-queued after its merge was withdrawn (ADR 0015).
	ReasonCancelWhileMerged      = "CANCEL_WHILE_MERGED"
	ReasonConsolidationWithdrawn = "CONSOLIDATION_WITHDRAWN"
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

// Line is one line of the group. ListingID and OwnerRef are set from the
// release on: the listing of the released node and whose stock it is, as the
// OwnerChoice rule picked it ("" = operator-owned; ADR 0012).
type Line struct {
	LineNo        int
	CatalogItemID string
	ListingID     string
	OwnerRef      string
	Quantity      int
}

// PromiseConstraint is what the assignment must keep (model value object plus
// revisionNo, G13). DeliverByEarliest is the start of the promised window, zero
// when unknown (a group queued before ADR 0015); when known it is not after
// DeliverByLatest.
type PromiseConstraint struct {
	PromiseID         string
	Tier              Tier
	DeliverByEarliest time.Time
	DeliverByLatest   time.Time
	WitnessNodeID     string
	RevisionNo        int
}

// Candidate is the current best node and path (model value object plus the
// pickup cutoff, which is the requiredShipBy of a release). Cost is the legacy
// ordinal score (ADR 0002, A12); EstimatedCostCents is the money-denominated
// estimate in planner-declared cents, complete only when every lane and wave
// component of the path had a declared cost (ADR 0011). SurchargeCents is what
// the sourcing policy added to the estimate (ADR 0014; 0 when none) and
// PolicyVersion the policy version the search read (0 when none was published).
type Candidate struct {
	NodeID             string
	PickupDepartureID  string
	PickupCutoffAt     time.Time
	Legs               []string
	DeliveryWaveID     string
	ETA                time.Time
	Cost               int64
	EstimatedCostCents int64
	CostComplete       bool
	SurchargeCents     int64
	PolicyVersion      int
	// EstimatedCo2eGrams is the carbon estimate of the same path in
	// planner-declared grams of CO2e, carried only when EmissionComplete
	// (ADR 0017).
	EstimatedCo2eGrams int64
	EmissionComplete   bool
}

// EffectiveCostCents is the estimate plus the surcharges (ADR 0014); the sum
// of the estimate's cents even when the estimate is incomplete.
func (c Candidate) EffectiveCostCents() int64 { return c.EstimatedCostCents + c.SurchargeCents }

// EffectiveCostOnWire is the effective cost where it is published: set only
// when a surcharge changed the cost, nil otherwise (ADR 0014).
func (c Candidate) EffectiveCostOnWire() *int64 {
	if c.SurchargeCents == 0 {
		return nil
	}
	cents := c.EffectiveCostCents()
	return &cents
}

func (c Candidate) clone() Candidate {
	c.Legs = slices.Clone(c.Legs)
	return c
}

// Same reports whether two candidates are the same node, path, wave, eta,
// costs and estimate.
func (c Candidate) Same(o Candidate) bool {
	return c.NodeID == o.NodeID && c.PickupDepartureID == o.PickupDepartureID && c.PickupCutoffAt.Equal(o.PickupCutoffAt) &&
		slices.Equal(c.Legs, o.Legs) && c.DeliveryWaveID == o.DeliveryWaveID && c.ETA.Equal(o.ETA) && c.Cost == o.Cost &&
		c.EstimatedCostCents == o.EstimatedCostCents && c.CostComplete == o.CostComplete &&
		c.SurchargeCents == o.SurchargeCents && c.PolicyVersion == o.PolicyVersion &&
		c.EstimatedCo2eGrams == o.EstimatedCo2eGrams && c.EmissionComplete == o.EmissionComplete
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

// SourcingFacts is what the candidate search knew when it chose the released
// candidate (ADR 0013): the regionId of the node and of the destination's
// service area ("" when the read model does not know it) and the size of the
// ranked candidate set the release chose from.
type SourcingFacts struct {
	NodeRegionID        string
	DestinationRegionID string
	CandidateCount      int
}

// DeliveredPackage is a delivered package of a shipped group.
type DeliveredPackage struct {
	TrackingNumber string
	DeliveredAt    time.Time
}
