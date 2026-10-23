package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

func hours(n int) time.Time { return T0.Add(time.Duration(n) * time.Hour) }

func candidate(node string) *assignment.Candidate {
	return &assignment.Candidate{
		NodeID: node, PickupDepartureID: "dep-" + node, PickupCutoffAt: hours(5), Legs: []string{"leg-1", "leg-2"},
		DeliveryWaveID: "wave-1", ETA: hours(30), Cost: 130,
	}
}

func input(id, order string, group int) assignment.New {
	return assignment.New{
		ID: assignment.ID(id), OrderID: order, GroupNo: group, DestinationPostalPrefix: "30301",
		Promise: assignment.PromiseConstraint{PromiseID: "prm-" + id, Tier: assignment.TierTwoDay, DeliverByLatest: hours(60), WitnessNodeID: "node-a"},
		Lines:   []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
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

func releaseLines() []assignment.Line {
	return []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2}}
}

// released builds a RELEASED assignment (version 2).
func released(t *testing.T, id, order string, group int) *assignment.Assignment {
	t.Helper()
	a := queued(t, id, order, group, hours(2))
	if _, err := a.Release(candidate("node-a"), releaseLines(), hours(2)); err != nil {
		t.Fatalf("Release: %v", err)
	}
	return a
}
