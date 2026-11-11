// Package views holds the read DTOs of the application layer: plain structs
// built from the domain, never persisted.
package views

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// Assignment is the read view of a FulfillmentAssignment (GET /assignments/{id}).
type Assignment struct {
	AssignmentID            string
	OrderID                 string
	GroupNo                 int
	DestinationPostalPrefix string
	State                   string
	AssignmentVersion       int64
	Promise                 Promise
	Lines                   []Line
	Candidate               *Candidate
	ReleaseAt               time.Time
	Releases                []Release
	ExcludedNodes           []ExcludedNode
	LockedAt                time.Time
	LockReason              string
	PickupDepartureID       string
	TrackingNumbers         []string
	ShippedAt               time.Time
	DeliveredAt             time.Time
	CancelReason            string
	// ConsolidatedInto is the carrier of a follower and ConsolidatedWith the
	// followers of a carrier; both empty when the assignment is not merged (ADR 0015).
	ConsolidatedInto string
	ConsolidatedWith []string
}

// Promise is the promise constraint. DeliverByEarliest is zero when unknown.
type Promise struct {
	PromiseID         string
	Tier              string
	DeliverByEarliest time.Time
	DeliverByLatest   time.Time
	WitnessNodeID     string
	RevisionNo        int
}

// Line is one line of the group. OwnerRef is whose stock the released line
// takes ("" = operator-owned; ADR 0012).
type Line struct {
	LineNo        int
	CatalogItemID string
	ListingID     string
	OwnerRef      string
	Quantity      int
}

// Candidate is the current best node and path. Cost is the legacy ordinal
// score; EstimatedCostCents is the money-denominated estimate in
// planner-declared cents, carried only when CostComplete (ADR 0011).
// SurchargeCents and PolicyVersion are what the sourcing policy added and the
// version it was read at (ADR 0014); EffectiveCostCents is derived.
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
}

// EffectiveCostCents is the estimate plus the surcharges (ADR 0014).
func (c Candidate) EffectiveCostCents() int64 { return c.EstimatedCostCents + c.SurchargeCents }

// Release is one release to a node.
type Release struct {
	ReleaseNo      int
	NodeID         string
	ReleasedAt     time.Time
	RequiredShipBy time.Time
	Outcome        string
	ReasonCode     string
}

// ExcludedNode is a node that refused the group.
type ExcludedNode struct {
	NodeID string
	Reason string
}

// AssignmentFrom builds the view of an assignment.
func AssignmentFrom(a *assignment.Assignment) Assignment {
	p := a.Promise()
	v := Assignment{
		AssignmentID: string(a.ID()), OrderID: a.OrderID(), GroupNo: a.GroupNo(), DestinationPostalPrefix: a.DestinationPostalPrefix(),
		State: string(a.State()), AssignmentVersion: a.Version(),
		Promise: Promise{PromiseID: p.PromiseID, Tier: string(p.Tier), DeliverByEarliest: p.DeliverByEarliest, DeliverByLatest: p.DeliverByLatest,
			WitnessNodeID: p.WitnessNodeID, RevisionNo: p.RevisionNo},
		ConsolidatedInto: string(a.ConsolidatedInto()),
		Lines:            make([]Line, 0, len(a.Lines())), Releases: []Release{}, ExcludedNodes: []ExcludedNode{},
		ReleaseAt: a.ReleaseAt(), LockedAt: a.LockedAt(), LockReason: a.LockReason(), PickupDepartureID: a.PickupDepartureID(),
		TrackingNumbers: append([]string{}, a.TrackingNumbers()...), ShippedAt: a.ShippedAt(), DeliveredAt: a.DeliveredAt(),
		CancelReason: a.CancelReason(),
	}
	for _, l := range a.Lines() {
		v.Lines = append(v.Lines, Line{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, ListingID: l.ListingID, OwnerRef: l.OwnerRef, Quantity: l.Quantity})
	}
	if c, ok := a.Candidate(); ok {
		v.Candidate = &Candidate{
			NodeID: c.NodeID, PickupDepartureID: c.PickupDepartureID, PickupCutoffAt: c.PickupCutoffAt,
			Legs: append([]string{}, c.Legs...), DeliveryWaveID: c.DeliveryWaveID, ETA: c.ETA, Cost: c.Cost,
			EstimatedCostCents: c.EstimatedCostCents, CostComplete: c.CostComplete,
			SurchargeCents: c.SurchargeCents, PolicyVersion: c.PolicyVersion,
		}
	}
	for _, r := range a.Releases() {
		v.Releases = append(v.Releases, Release{
			ReleaseNo: r.No, NodeID: r.NodeID, ReleasedAt: r.ReleasedAt, RequiredShipBy: r.RequiredShipBy,
			Outcome: string(r.Outcome), ReasonCode: r.ReasonCode,
		})
	}
	for _, e := range a.ExcludedNodes() {
		v.ExcludedNodes = append(v.ExcludedNodes, ExcludedNode{NodeID: e.NodeID, Reason: e.Reason})
	}
	for _, m := range a.Members() {
		v.ConsolidatedWith = append(v.ConsolidatedWith, string(m))
	}
	return v
}
