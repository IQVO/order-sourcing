package sourcing

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func h(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }

func fc(id, region string) network.Node {
	return network.Node{
		ID: id, Type: network.TypeFulfillmentCenter, Subtype: "SORTABLE", Operator: network.OperatorVendor, RegionID: region,
		Status: network.StatusActive, ProfileRevision: 1, HandlingMinutes: 60,
	}
}

func dep(id, laneID, from, to string, cutoff, departs, arrives int) network.Departure {
	return network.Departure{
		ID: id, LaneID: laneID, Kind: "LINEHAUL", Origin: from, Destination: to, CutoffAt: h(cutoff), DepartsAt: h(departs),
		ArrivesAt: h(arrives), Capacity: 100, Remaining: 100, RemainingKnown: true, Scheduled: true,
	}
}

func wave(id string, inductCutoff, start, end int) network.Wave {
	return network.Wave{
		ID: id, StationID: "ds-1", InductCutoffAt: h(inductCutoff), WindowStart: h(start), WindowEnd: h(end),
		Capacity: 50, Remaining: 50, RemainingKnown: true, Scheduled: true,
	}
}

func listing(node, item string, qty int) network.Listing {
	return network.Listing{ID: "lst-" + item + "-" + node, CatalogItemID: item, NodeID: node, AvailableQty: qty}
}

// world: fc-a (region r1) and fc-b (region r2) both reach ds-1 (region r1) through sc-1.
func world() World {
	return World{
		Nodes: map[string]network.Node{
			"fc-a": fc("fc-a", "r1"), "fc-b": fc("fc-b", "r2"),
			"sc-1": {ID: "sc-1", Type: "SORTATION_CENTER", Operator: "NETWORK", Status: network.StatusActive, SortDwellMinutes: 30},
		},
		Departures: []network.Departure{
			dep("pk-a", "lane-a", "fc-a", "sc-1", 4, 5, 8),
			dep("pk-b", "lane-b", "fc-b", "sc-1", 4, 5, 8),
			dep("lh-1", "lane-s", "sc-1", "ds-1", 10, 11, 14),
		},
		Lanes:        map[string]network.Lane{},
		Waves:        []network.Wave{wave("w1", 15, 20, 26)},
		Area:         network.ServiceArea{PostalPrefix: "30301", StationID: "ds-1", RegionID: "r1", Version: 1},
		Listings:     []network.Listing{listing("fc-a", "cat-1", 10), listing("fc-b", "cat-1", 10)},
		Discontinued: map[string]bool{},
		Scorecards:   map[string]network.Scorecard{},
	}
}

func request() Request {
	return Request{
		Now: now, Tier: assignment.TierTwoDay, DeliverByLatest: h(48), WitnessNodeID: "fc-a",
		Lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
	}
}

func nodesOf(opts []Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Candidate.NodeID)
	}
	return out
}

func wantNodes(t *testing.T, opts []Option, want ...string) {
	t.Helper()
	got := nodesOf(opts)
	if len(got) != len(want) {
		t.Fatalf("nodes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nodes = %v, want %v", got, want)
		}
	}
}

func wantCost(t *testing.T, o Option, cost int64) {
	t.Helper()
	if o.Candidate.Cost != cost {
		t.Fatalf("cost of %s = %d, want %d", o.Candidate.NodeID, o.Candidate.Cost, cost)
	}
}
