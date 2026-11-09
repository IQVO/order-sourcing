package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// The sourcing-decision fact is filled from the search's world (ADR 0013).

func (e *env) lastDecision() assignment.FulfillmentSourcingDecided {
	e.t.Helper()
	events := e.store.Outbox().Events()
	for i := len(events) - 1; i >= 0; i-- {
		if d, ok := events[i].(assignment.FulfillmentSourcingDecided); ok {
			return d
		}
	}
	e.t.Fatal("no FulfillmentSourcingDecided was published")
	return assignment.FulfillmentSourcingDecided{}
}

func TestReleaseToNodeRecordsTheDecisionInRegion(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.release(id)
	d := e.lastDecision()
	// fc-a (r1) and fc-b (r2) both reach ds-1 (r1): two candidates, fc-a wins; pk-a then lh-1 is one leg.
	if d.AssignmentID != id || d.NodeID != "fc-a" || d.NodeRegionID != "r1" || d.DestinationRegionID != "r1" || d.CandidateCount != 2 ||
		d.Legs != 1 || d.Units != 2 || d.ReleaseNo != 1 || d.OrderID != "ord-1" || d.GroupNo != 1 || !d.DecidedAt.Equal(t0) || d.CostComplete() {
		t.Fatalf("decision = %+v", d)
	}
	if in, known := d.InRegion(); !in || !known {
		t.Fatalf("fc-a serves its own region: %v %v", in, known)
	}
}

func TestReleaseToNodeRecordsTheDecisionOutOfRegion(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.must(e.store.ReadModels().PutListing(context.Background(), listingZero("fc-a")))
	e.release(id)
	d := e.lastDecision()
	if d.NodeID != "fc-b" || d.NodeRegionID != "r2" || d.CandidateCount != 1 {
		t.Fatalf("decision = %+v", d)
	}
	if in, known := d.InRegion(); in || !known {
		t.Fatalf("fc-b is out of region: %v %v", in, known)
	}
}

func TestReleaseToNodeRecordsAnUnknownRegion(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.must(e.store.ReadModels().PutNode(context.Background(), network.Node{ID: "fc-a", Type: network.TypeFulfillmentCenter, Subtype: "SORTABLE",
		Operator: network.OperatorVendor, Status: network.StatusActive, ProfileRevision: 1, HandlingMinutes: 60}))
	e.release(id)
	d := e.lastDecision()
	if d.NodeID != "fc-a" || d.NodeRegionID != "" || d.DestinationRegionID != "r1" {
		t.Fatalf("decision = %+v", d)
	}
	if _, known := d.InRegion(); known {
		t.Fatal("a node without a region leaves inRegion unknown")
	}
}

func TestSameDayQueueReleasesWithTheDecision(t *testing.T) {
	e := newEnv(t)
	e.must(e.store.ReadModels().PutNode(context.Background(), network.Node{ID: "fc-a", Type: network.TypeFulfillmentCenter, Subtype: network.SubtypeSubSameDay,
		Operator: network.OperatorVendor, RegionID: "r1", Status: network.StatusActive, ProfileRevision: 1, HandlingMinutes: 60}))
	e.must(e.store.ReadModels().PutDeparture(context.Background(), directDeparture()))
	_, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), placed("evt-sd", group(1, "prm-sd", assignment.TierSameDay)))
	e.must(err)
	d := e.lastDecision()
	if d.NodeID != "fc-a" || d.CandidateCount != 1 || d.DestinationRegionID != "r1" || d.Legs != 0 {
		t.Fatalf("decision = %+v", d)
	}
}
