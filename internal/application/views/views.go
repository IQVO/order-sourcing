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
}

// Promise is the promise constraint.
type Promise struct {
	PromiseID       string
	Tier            string
	DeliverByLatest time.Time
	WitnessNodeID   string
	RevisionNo      int
}

// Line is one line of the group.
type Line struct {
	LineNo        int
	CatalogItemID string
	ListingID     string
	Quantity      int
}

// Candidate is the current best node and path.
type Candidate struct {
	NodeID            string
	PickupDepartureID string
	PickupCutoffAt    time.Time
	Legs              []string
	DeliveryWaveID    string
	ETA               time.Time
	Cost              int64
}

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
		Promise: Promise{PromiseID: p.PromiseID, Tier: string(p.Tier), DeliverByLatest: p.DeliverByLatest, WitnessNodeID: p.WitnessNodeID, RevisionNo: p.RevisionNo},
		Lines:   make([]Line, 0, len(a.Lines())), Releases: []Release{}, ExcludedNodes: []ExcludedNode{},
		ReleaseAt: a.ReleaseAt(), LockedAt: a.LockedAt(), LockReason: a.LockReason(), PickupDepartureID: a.PickupDepartureID(),
		TrackingNumbers: append([]string{}, a.TrackingNumbers()...), ShippedAt: a.ShippedAt(), DeliveredAt: a.DeliveredAt(),
		CancelReason: a.CancelReason(),
	}
	for _, l := range a.Lines() {
		v.Lines = append(v.Lines, Line{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, ListingID: l.ListingID, Quantity: l.Quantity})
	}
	if c, ok := a.Candidate(); ok {
		v.Candidate = &Candidate{
			NodeID: c.NodeID, PickupDepartureID: c.PickupDepartureID, PickupCutoffAt: c.PickupCutoffAt,
			Legs: append([]string{}, c.Legs...), DeliveryWaveID: c.DeliveryWaveID, ETA: c.ETA, Cost: c.Cost,
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
	return v
}
