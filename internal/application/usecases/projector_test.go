package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

func meta(id string) usecases.Meta { return usecases.Meta{EventID: id, EventType: "x"} }

func TestProjectorNodes(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	e.must(p.NodeRegistered(ctx, meta("e1"), "n9", network.Registered{Type: "FULFILLMENT_CENTER", Subtype: "MICRO", Operator: "VENDOR", RegionID: "r1", At: h(0)}))
	e.must(p.NodeProfileDeclared(ctx, meta("e2"), "n9", 2, 90, 0))
	e.must(p.NodeProfileDeclared(ctx, meta("e3"), "n9", 1, 10, 0)) // older revision: ignored
	e.must(p.NodeStatusChanged(ctx, meta("e4"), "n9", network.StatusActive, h(1)))
	e.must(p.NodeStatusChanged(ctx, meta("e5"), "n9", network.StatusSuspended, h(0))) // older: ignored
	n, err := e.store.ReadModels().GetNode(ctx, "n9")
	e.must(err)
	if n.HandlingMinutes != 90 || n.Status != network.StatusActive || n.Subtype != "MICRO" || n.ProfileRevision != 2 || !n.Eligible() {
		t.Fatalf("node = %+v", n)
	}
	// A profile or status before the registration creates the row; the registration fills it in.
	e.must(p.NodeProfileDeclared(ctx, meta("e6"), "n10", 1, 30, 5))
	e.must(p.NodeStatusChanged(ctx, meta("e7"), "n10", network.StatusActive, h(2)))
	e.must(p.NodeRegistered(ctx, meta("e8"), "n10", network.Registered{Type: "FULFILLMENT_CENTER", Operator: "VENDOR", RegionID: "r2", At: h(0)}))
	n, _ = e.store.ReadModels().GetNode(ctx, "n10")
	if n.Status != network.StatusActive || n.RegionID != "r2" || n.HandlingMinutes != 30 {
		t.Fatalf("out of order node = %+v", n)
	}
	// A repeated event id changes nothing.
	before := e.store.Counts()
	e.must(p.NodeProfileDeclared(ctx, meta("e2"), "n9", 7, 1, 1))
	e.wantUnchanged(before)
	if n, _ = e.store.ReadModels().GetNode(ctx, "n9"); n.ProfileRevision != 2 {
		t.Fatal("redelivery applied twice")
	}
}

func TestProjectorNetworkGraph(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	rm := e.store.ReadModels()
	e.must(p.LaneClosed(ctx, meta("l0"), "lane-9")) // before it opened
	e.must(p.LaneOpened(ctx, meta("l1"), network.Lane{ID: "lane-9", Origin: "a", Destination: "b", Mode: "GROUND"}))
	if l, _ := rm.GetLane(ctx, "lane-9"); !l.Closed || l.Origin != "a" {
		t.Fatalf("a close that arrived first stays: %+v", l)
	}
	e.must(p.ServiceAreaAssigned(ctx, meta("s1"), network.ServiceArea{PostalPrefix: "111", StationID: "s1", RegionID: "r1", Version: 2}))
	e.must(p.ServiceAreaAssigned(ctx, meta("s2"), network.ServiceArea{PostalPrefix: "111", StationID: "s0", RegionID: "r0", Version: 1})) // older
	e.must(p.ServiceAreaAssigned(ctx, meta("s3"), network.ServiceArea{PostalPrefix: "111", StationID: "s3", RegionID: "r3", Version: 3}))
	if a, _ := rm.GetServiceArea(ctx, "111"); a.StationID != "s3" {
		t.Fatalf("area = %+v", a)
	}
	e.must(p.RegionDefined(ctx, meta("r1"), network.Region{ID: "r1", Code: "SE", Name: "South East"}))

	e.must(p.DepartureCapacityChanged(ctx, meta("d1"), "dep-9", 0))
	e.must(p.DepartureScheduled(ctx, meta("d2"), network.Departure{ID: "dep-9", LaneID: "lane-9", Origin: "a", Destination: "b", CutoffAt: h(5), Capacity: 10}))
	if d, _ := rm.GetDeparture(ctx, "dep-9"); d.Remaining != 0 || !d.Scheduled || d.Usable() {
		t.Fatalf("capacity before schedule: %+v", d)
	}
	e.must(p.DepartureCancelled(ctx, meta("d3"), "dep-9"))
	e.must(p.DepartureCancelled(ctx, meta("d4"), "dep-new")) // cancel for an unknown departure creates a stub
	if d, _ := rm.GetDeparture(ctx, "dep-new"); !d.Cancelled || d.Scheduled {
		t.Fatalf("stub = %+v", d)
	}
	e.must(p.WaveCapacityChanged(ctx, meta("w1"), "wave-9", 4))
	e.must(p.WaveScheduled(ctx, meta("w2"), network.Wave{ID: "wave-9", StationID: "s3", InductCutoffAt: h(5), WindowEnd: h(9), Capacity: 40}))
	if w, _ := rm.GetWave(ctx, "wave-9"); w.Remaining != 4 || !w.Usable() {
		t.Fatalf("wave = %+v", w)
	}
	e.must(p.WaveCancelled(ctx, meta("w3"), "wave-9"))
	if w, _ := rm.GetWave(ctx, "wave-9"); w.Usable() {
		t.Fatal("a cancelled wave is not usable")
	}
}

func TestProjectorDeclaredCosts(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	rm := e.store.ReadModels()

	// A lane cost before LaneOpened: stored keyed by laneId anyway, and the
	// later open completes the row without losing the cost (ADR 0003).
	e.must(p.LaneCostDeclared(ctx, meta("c1"), "lane-c", 320, h(2)))
	if l, _ := rm.GetLane(ctx, "lane-c"); l.CostPerUnitCents != 320 || !l.CostDeclared() {
		t.Fatalf("cost stub = %+v", l)
	}
	e.must(p.LaneOpened(ctx, meta("c2"), network.Lane{ID: "lane-c", Origin: "a", Destination: "b", Mode: "GROUND"}))
	if l, _ := rm.GetLane(ctx, "lane-c"); l.Origin != "a" || l.CostPerUnitCents != 320 || !l.CostDeclared() {
		t.Fatalf("open after cost = %+v", l)
	}
	// The other order: an open lane takes the declaration, and an older
	// declaration never wins.
	e.must(p.LaneOpened(ctx, meta("c3"), network.Lane{ID: "lane-o", Origin: "a", Destination: "c", Mode: "AIR"}))
	e.must(p.LaneCostDeclared(ctx, meta("c4"), "lane-o", 700, h(5)))
	e.must(p.LaneCostDeclared(ctx, meta("c5"), "lane-o", 900, h(4))) // older: ignored
	if l, _ := rm.GetLane(ctx, "lane-o"); l.CostPerUnitCents != 700 || l.Mode != "AIR" {
		t.Fatalf("cost after open = %+v", l)
	}

	// A wave cost before DeliveryWaveScheduled keeps through the schedule; a
	// newer declaration after it still wins.
	e.must(p.WaveCostDeclared(ctx, meta("c6"), "wave-c", 425, h(3)))
	e.must(p.WaveScheduled(ctx, meta("c7"), network.Wave{ID: "wave-c", StationID: "s1", InductCutoffAt: h(5), WindowEnd: h(9), Capacity: 40}))
	if w, _ := rm.GetWave(ctx, "wave-c"); w.CostPerStopCents != 425 || !w.Scheduled || !w.Usable() {
		t.Fatalf("schedule after cost = %+v", w)
	}
	e.must(p.WaveCostDeclared(ctx, meta("c8"), "wave-c", 500, h(6)))
	if w, _ := rm.GetWave(ctx, "wave-c"); w.CostPerStopCents != 500 || !w.Scheduled {
		t.Fatalf("newer cost over schedule = %+v", w)
	}
	// A redelivery changes nothing.
	before := e.store.Counts()
	e.must(p.LaneCostDeclared(ctx, meta("c4"), "lane-o", 1, h(1)))
	e.must(p.WaveCostDeclared(ctx, meta("c6"), "wave-c", 1, h(1)))
	e.wantUnchanged(before)
	if l, _ := rm.GetLane(ctx, "lane-o"); l.CostPerUnitCents != 700 {
		t.Fatal("redelivery applied twice")
	}
}

// TestProjectorDeclaredEmissions: the emission declarations merge exactly like
// the cost ones — any order of arrival, the newer declaredAt wins, and a
// redelivery changes nothing (ADR 0003, ADR 0017).
func TestProjectorDeclaredEmissions(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	rm := e.store.ReadModels()

	// An emission before LaneOpened: stored keyed by laneId anyway, the later
	// open keeps it; the cost and the emission sit on the same row.
	e.must(p.LaneEmissionDeclared(ctx, meta("e1"), "lane-c", 540, h(2)))
	e.must(p.LaneCostDeclared(ctx, meta("e2"), "lane-c", 320, h(2)))
	e.must(p.LaneOpened(ctx, meta("e3"), network.Lane{ID: "lane-c", Origin: "a", Destination: "b", Mode: "GROUND"}))
	if l, _ := rm.GetLane(ctx, "lane-c"); l.Origin != "a" || l.EmissionPerUnitG != 540 || !l.EmissionDeclared() || l.CostPerUnitCents != 320 {
		t.Fatalf("open after declarations = %+v", l)
	}
	// The other order, with an older declaration ignored.
	e.must(p.LaneOpened(ctx, meta("e4"), network.Lane{ID: "lane-o", Origin: "a", Destination: "c", Mode: "AIR"}))
	e.must(p.LaneEmissionDeclared(ctx, meta("e5"), "lane-o", 610, h(5)))
	e.must(p.LaneEmissionDeclared(ctx, meta("e6"), "lane-o", 900, h(4))) // older: ignored
	if l, _ := rm.GetLane(ctx, "lane-o"); l.EmissionPerUnitG != 610 || l.Mode != "AIR" || l.CostDeclared() {
		t.Fatalf("emission after open = %+v", l)
	}

	// The wave twin: before the schedule and a newer declaration after it.
	e.must(p.WaveEmissionDeclared(ctx, meta("e7"), "wave-c", 300, h(3)))
	e.must(p.WaveScheduled(ctx, meta("e8"), network.Wave{ID: "wave-c", StationID: "s1", InductCutoffAt: h(5), WindowEnd: h(9), Capacity: 40}))
	if w, _ := rm.GetWave(ctx, "wave-c"); w.EmissionPerStopG != 300 || !w.Scheduled || !w.Usable() {
		t.Fatalf("schedule after emission = %+v", w)
	}
	e.must(p.WaveEmissionDeclared(ctx, meta("e9"), "wave-c", 260, h(6)))
	if w, _ := rm.GetWave(ctx, "wave-c"); w.EmissionPerStopG != 260 || !w.Scheduled {
		t.Fatalf("newer emission over schedule = %+v", w)
	}
	// A redelivery changes nothing.
	before := e.store.Counts()
	e.must(p.LaneEmissionDeclared(ctx, meta("e5"), "lane-o", 1, h(1)))
	e.must(p.WaveEmissionDeclared(ctx, meta("e7"), "wave-c", 1, h(1)))
	e.wantUnchanged(before)
	if l, _ := rm.GetLane(ctx, "lane-o"); l.EmissionPerUnitG != 610 {
		t.Fatal("redelivery applied twice")
	}
}

func TestProjectorAvailabilityScorecardsAndPressure(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	rm := e.store.ReadModels()
	e.must(p.ListingAvailabilityChanged(ctx, meta("a1"), avail("lst-9", "cat-9", "n1", "", 5, h(2))))
	e.must(p.ListingAvailabilityChanged(ctx, meta("a2"), avail("lst-9", "", "", "", 99, h(1)))) // older asOf: ignored
	e.must(p.ListingCreated(ctx, meta("a3"), network.Listing{ID: "lst-9", CatalogItemID: "cat-9", NodeID: "n1"}))
	e.must(p.ListingSuppressionChanged(ctx, meta("a4"), "lst-9", true))
	l, _ := rm.GetListing(ctx, "lst-9")
	if l.AvailableQty != 5 || !l.Suppressed || l.CatalogItemID != "cat-9" || l.NodeID != "n1" {
		t.Fatalf("listing = %+v", l)
	}
	e.must(p.ListingSuppressionChanged(ctx, meta("a5"), "lst-9", false))
	e.must(p.ListingAvailabilityChanged(ctx, meta("a6"), avail("lst-9", "", "", "", 7, h(3))))
	if l, _ = rm.GetListing(ctx, "lst-9"); l.Suppressed || l.AvailableQty != 7 || l.NodeID != "n1" {
		t.Fatalf("listing = %+v", l)
	}
	e.must(p.CatalogItemDiscontinued(ctx, meta("a7"), "cat-9"))

	e.must(p.ScorecardUpdated(ctx, meta("c1"), network.Scorecard{NodeID: "n1", Window: "30d", Grade: "B", ComputedAt: h(2)}))
	e.must(p.ScorecardUpdated(ctx, meta("c2"), network.Scorecard{NodeID: "n1", Window: "30d", Grade: "D", ComputedAt: h(1)})) // older
	e.must(p.ScorecardUpdated(ctx, meta("c3"), network.Scorecard{NodeID: "n1", Window: "7d", Grade: "A", ComputedAt: h(1)}))
	if s, _ := rm.GetScorecard(ctx, "n1", "30d"); s.Grade != "B" {
		t.Fatalf("scorecard = %+v", s)
	}
	e.must(p.BookExhausted(ctx, meta("b1"), network.Pressure{BookID: "b1", NodeID: "n1", PickupDepartureID: "pk-a"}))
}

func TestProjectorFailuresRollBack(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	before := e.store.Counts()
	e.rm.get = fault{errBoom, 1}
	wantErr(t, p.NodeProfileDeclared(ctx, meta("f1"), "n1", 5, 1, 1), errBoom)
	e.rm.put = fault{errBoom, 9}
	wantErr(t, p.NodeProfileDeclared(ctx, meta("f2"), "n1", 5, 1, 1), errBoom)
	e.wantUnchanged(before)
	e.rm.put = fault{}
	e.inbox.fault = fault{errBoom, 1}
	wantErr(t, p.NodeProfileDeclared(ctx, meta("f3"), "n1", 5, 1, 1), errBoom)
	e.wantUnchanged(before)
	e.must(p.NodeProfileDeclared(ctx, meta("f2"), "n1", 5, 1, 1)) // the failed claim was rolled back: the same id works now
	if n, _ := e.store.ReadModels().GetNode(ctx, "n1"); n.ProfileRevision != 5 || n.HandlingMinutes != 1 {
		t.Fatalf("node = %+v", n)
	}
}
