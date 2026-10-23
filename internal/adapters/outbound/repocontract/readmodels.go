package repocontract

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

func runReadModels(t *testing.T, f Factory) {
	t.Run("a miss is ErrNotFound and a put replaces", func(t *testing.T) { putGet(t, f(t)) })
	t.Run("the inbox claims once", func(t *testing.T) { inboxClaims(t, f(t)) })
}

func putGet(t *testing.T, fx Fixture) {
	rm := fx.ReadModels
	node := network.Node{ID: "n1", Type: "FULFILLMENT_CENTER", Subtype: "SORTABLE", Operator: "VENDOR", RegionID: "r1", Status: "ACTIVE",
		StatusAt: hours(1), ProfileRevision: 2, HandlingMinutes: 95, SortDwellMinutes: 30}
	lane := network.Lane{ID: "l1", Origin: "n1", Destination: "n2", Mode: "GROUND", Closed: true}
	dep := network.Departure{ID: "d1", LaneID: "l1", Kind: "PICKUP", Origin: "n1", Destination: "n2", CutoffAt: hours(4), DepartsAt: hours(5),
		ArrivesAt: hours(8), Capacity: 50, Remaining: 7, RemainingKnown: true, Scheduled: true, Cancelled: true}
	wave := network.Wave{ID: "w1", StationID: "s1", InductCutoffAt: hours(10), WindowStart: hours(12), WindowEnd: hours(18), Capacity: 90,
		Remaining: 3, RemainingKnown: true, Scheduled: true, Cancelled: true}
	area := network.ServiceArea{PostalPrefix: "30301", StationID: "s1", RegionID: "r1", Version: 3}
	listing := network.Listing{ID: "lst1", CatalogItemID: "cat-1", NodeID: "n1", AvailableQty: 8, AsOf: hours(2), Suppressed: true}
	card := network.Scorecard{NodeID: "n1", Window: "30d", Grade: "B", ComputedAt: hours(3)}

	if _, err := rm.GetNode(ctx(), "n1"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("GetNode miss = %v", err)
	}
	checks := []struct {
		name string
		put  func() error
		get  func() (any, error)
		want any
	}{
		{"node", func() error { return rm.PutNode(ctx(), node) }, func() (any, error) { return rm.GetNode(ctx(), "n1") }, node},
		{"lane", func() error { return rm.PutLane(ctx(), lane) }, func() (any, error) { return rm.GetLane(ctx(), "l1") }, lane},
		{"departure", func() error { return rm.PutDeparture(ctx(), dep) }, func() (any, error) { return rm.GetDeparture(ctx(), "d1") }, dep},
		{"wave", func() error { return rm.PutWave(ctx(), wave) }, func() (any, error) { return rm.GetWave(ctx(), "w1") }, wave},
		{"service area", func() error { return rm.PutServiceArea(ctx(), area) }, func() (any, error) { return rm.GetServiceArea(ctx(), "30301") }, area},
		{"listing", func() error { return rm.PutListing(ctx(), listing) }, func() (any, error) { return rm.GetListing(ctx(), "lst1") }, listing},
		{"scorecard", func() error { return rm.PutScorecard(ctx(), card) }, func() (any, error) { return rm.GetScorecard(ctx(), "n1", "30d") }, card},
	}
	for _, c := range checks {
		if err := c.put(); err != nil {
			t.Fatalf("put %s: %v", c.name, err)
		}
		got, err := c.get()
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s round trip = %+v %v, want %+v", c.name, got, err, c.want)
		}
	}
	node.HandlingMinutes = 10
	if err := rm.PutNode(ctx(), node); err != nil {
		t.Fatal(err)
	}
	if got, _ := rm.GetNode(ctx(), "n1"); got.HandlingMinutes != 10 {
		t.Fatal("a put replaces the row")
	}
	missesAndSingles(t, rm)
}

func missesAndSingles(t *testing.T, rm ports.ReadModels) {
	t.Helper()
	for _, miss := range []func() error{
		func() error { _, err := rm.GetLane(ctx(), "none"); return err },
		func() error { _, err := rm.GetDeparture(ctx(), "none"); return err },
		func() error { _, err := rm.GetWave(ctx(), "none"); return err },
		func() error { _, err := rm.GetServiceArea(ctx(), "none"); return err },
		func() error { _, err := rm.GetListing(ctx(), "none"); return err },
		func() error { _, err := rm.GetScorecard(ctx(), "n1", "7d"); return err },
	} {
		if err := miss(); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("a miss = %v, want ErrNotFound", err)
		}
	}
	if err := rm.PutRegion(ctx(), network.Region{ID: "r1", Code: "SE", Name: "South East"}); err != nil {
		t.Fatal(err)
	}
	if err := rm.PutRegion(ctx(), network.Region{ID: "r1", Code: "SE", Name: "South East 2"}); err != nil {
		t.Fatalf("a region is replaceable: %v", err)
	}
	if err := rm.PutDiscontinued(ctx(), "cat-9"); err != nil {
		t.Fatal(err)
	}
	if err := rm.PutDiscontinued(ctx(), "cat-9"); err != nil {
		t.Fatalf("discontinuing twice is fine: %v", err)
	}
	if err := rm.PutPressure(ctx(), network.Pressure{BookID: "b1", NodeID: "n1", PickupDepartureID: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := rm.PutPressure(ctx(), network.Pressure{BookID: "b1", NodeID: "n1", PickupDepartureID: "d1"}); err != nil {
		t.Fatalf("a pressure row is replaceable: %v", err)
	}
}

func inboxClaims(t *testing.T, fx Fixture) {
	first, err := fx.Inbox.Claim(ctx(), "evt-1", "com.retailnetwork.x")
	if err != nil || !first {
		t.Fatalf("first claim = %v %v", first, err)
	}
	again, err := fx.Inbox.Claim(ctx(), "evt-1", "com.retailnetwork.x")
	if err != nil || again {
		t.Fatalf("second claim = %v %v", again, err)
	}
	other, err := fx.Inbox.Claim(ctx(), "evt-2", "com.retailnetwork.x")
	if err != nil || !other {
		t.Fatalf("another id = %v %v", other, err)
	}
}

func runWorld(t *testing.T, f Factory) {
	fx := f(t)
	now := hours(1)
	seedWorld(t, fx.ReadModels)
	w, err := fx.World.Load(ctx(), repository.WorldQuery{DestinationPostalPrefix: "30301", CatalogItemIDs: []string{"cat-1", "cat-2"}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	wantWorld(t, w)
	unknown, err := fx.World.Load(ctx(), repository.WorldQuery{DestinationPostalPrefix: "00000", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Area.StationID != "" || len(unknown.Waves) != 0 || len(unknown.Listings) != 0 {
		t.Fatalf("an unknown prefix has no area, waves or listings: %+v", unknown)
	}
}

func seedWorld(t *testing.T, rm ports.ReadModels) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := hours(1)
	must(rm.PutNode(ctx(), network.Node{ID: "n1", Type: "FULFILLMENT_CENTER", Status: "ACTIVE"}))
	must(rm.PutLane(ctx(), network.Lane{ID: "l1", Origin: "n1", Destination: "s1"}))
	must(rm.PutServiceArea(ctx(), network.ServiceArea{PostalPrefix: "30301", StationID: "s1", RegionID: "r1", Version: 1}))
	must(rm.PutServiceArea(ctx(), network.ServiceArea{PostalPrefix: "99999", StationID: "s9", RegionID: "r9", Version: 1}))
	dep := func(id string, cutoff time.Time, scheduled bool) network.Departure {
		return network.Departure{ID: id, LaneID: "l1", Origin: "n1", Destination: "s1", CutoffAt: cutoff, Remaining: 5, RemainingKnown: true, Scheduled: scheduled}
	}
	must(rm.PutDeparture(ctx(), dep("d-future", hours(3), true)))
	must(rm.PutDeparture(ctx(), dep("d-edge", now, true)))
	must(rm.PutDeparture(ctx(), dep("d-past", hours(0), true)))
	must(rm.PutDeparture(ctx(), network.Departure{ID: "d-stub", Remaining: 2, RemainingKnown: true}))
	wv := func(id, station string, induct time.Time) network.Wave {
		return network.Wave{ID: id, StationID: station, InductCutoffAt: induct, WindowStart: hours(10), WindowEnd: hours(12), Remaining: 4, RemainingKnown: true, Scheduled: true}
	}
	must(rm.PutWave(ctx(), wv("w-ok", "s1", hours(5))))
	must(rm.PutWave(ctx(), wv("w-edge", "s1", now)))
	must(rm.PutWave(ctx(), wv("w-past", "s1", hours(0))))
	must(rm.PutWave(ctx(), wv("w-other", "s9", hours(5))))
	must(rm.PutListing(ctx(), network.Listing{ID: "lst-1", CatalogItemID: "cat-1", NodeID: "n1", AvailableQty: 5, AsOf: hours(0)}))
	must(rm.PutListing(ctx(), network.Listing{ID: "lst-2", CatalogItemID: "cat-2", NodeID: "n1", AvailableQty: 5, AsOf: hours(0)}))
	must(rm.PutDiscontinued(ctx(), "cat-1"))
	must(rm.PutDiscontinued(ctx(), "cat-3"))
	must(rm.PutScorecard(ctx(), network.Scorecard{NodeID: "n1", Window: "30d", Grade: "C", ComputedAt: hours(0)}))
	must(rm.PutScorecard(ctx(), network.Scorecard{NodeID: "n1", Window: "7d", Grade: "A", ComputedAt: hours(0)}))
	must(rm.PutPressure(ctx(), network.Pressure{BookID: "b1", NodeID: "n1", PickupDepartureID: "d-future"}))

}

func wantWorld(t *testing.T, w sourcing.World) {
	t.Helper()
	if w.Area.StationID != "s1" || w.Area.RegionID != "r1" {
		t.Fatalf("area = %+v", w.Area)
	}
	if got := departureIDs(w.Departures); !reflect.DeepEqual(got, []string{"d-edge", "d-future"}) {
		t.Fatalf("departures = %v: scheduled ones whose cutoff is not before now, by id", got)
	}
	if got := waveIDs(w.Waves); !reflect.DeepEqual(got, []string{"w-edge", "w-ok"}) {
		t.Fatalf("waves = %v: the station's, induct cutoff not before now, by id", got)
	}
	wantWorldRest(t, w)
}

func wantWorldRest(t *testing.T, w sourcing.World) {
	t.Helper()
	if len(w.Listings) != 2 || w.Listings[0].ID != "lst-1" || w.Listings[1].ID != "lst-2" {
		t.Fatalf("listings = %+v: only the asked items, by id", w.Listings)
	}
	if !w.Discontinued["cat-1"] || w.Discontinued["cat-3"] || w.Discontinued["cat-2"] {
		t.Fatalf("discontinued = %v: only asked items", w.Discontinued)
	}
	if sc := w.Scorecards["n1"]; sc.Grade != "C" || len(w.Scorecards) != 1 {
		t.Fatalf("scorecards = %+v: the 30d window only", w.Scorecards)
	}
	if len(w.Pressure) != 1 || w.Pressure[0].BookID != "b1" || len(w.Nodes) != 1 || len(w.Lanes) != 1 {
		t.Fatalf("pressure %v nodes %v lanes %v", w.Pressure, w.Nodes, w.Lanes)
	}
}

func departureIDs(ds []network.Departure) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

func waveIDs(ws []network.Wave) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}
