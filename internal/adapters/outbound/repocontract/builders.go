package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// Shared fixture identifiers reused across the contract suites (goconst).
const (
	fixtureCatItem = "cat-1"
	fixtureListing = "lst-1"
	fixtureAsgTwo  = "asg-2"
)

func hours(n int) time.Time { return T0.Add(time.Duration(n) * time.Hour) }

func candidate(node string) *assignment.Candidate {
	return &assignment.Candidate{
		NodeID: node, PickupDepartureID: "dep-" + node, PickupCutoffAt: hours(5), Legs: []string{"leg-1", "leg-2"},
		DeliveryWaveID: "wave-1", ETA: hours(30), Cost: 130,
		// The money and sourcing-policy fields of the candidate (ADR 0011, ADR 0014) round-trip too.
		EstimatedCostCents: 950, CostComplete: true, SurchargeCents: 300, PolicyVersion: 2,
	}
}

func input(id, order string, group int) assignment.New {
	return assignment.New{
		ID: assignment.ID(id), OrderID: order, GroupNo: group, DestinationPostalPrefix: "30301",
		Promise: assignment.PromiseConstraint{PromiseID: "prm-" + id, Tier: assignment.TierTwoDay, DeliverByLatest: hours(60), WitnessNodeID: "node-a"},
		Lines:   []assignment.Line{{LineNo: 1, CatalogItemID: fixtureCatItem, Quantity: 2}},
	}
}

// queued builds a QUEUED assignment on node-a with the given release point.
func queued(t *testing.T, id, order string, group int, releaseAt time.Time) *assignment.Assignment {
	t.Helper()
	a, _, err := assignment.Queue(input(id, order, group), candidate("node-a"), releaseAt, T0)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	return a
}

// fixtureOwner is the seller of the released line (ADR 0012).
const fixtureOwner = "sel-1"

func releaseLines() []assignment.Line {
	return []assignment.Line{{LineNo: 1, CatalogItemID: fixtureCatItem, ListingID: fixtureListing, OwnerRef: fixtureOwner, Quantity: 2}}
}

// released builds a RELEASED assignment of group 1 (version 2).
func released(t *testing.T, id, order string) *assignment.Assignment {
	t.Helper()
	a := queued(t, id, order, 1, hours(2))
	if _, err := a.Release(candidate("node-a"), releaseLines(), assignment.SourcingFacts{NodeRegionID: "r1", DestinationRegionID: "r1", CandidateCount: 1}, hours(2)); err != nil {
		t.Fatalf("Release: %v", err)
	}
	return a
}
