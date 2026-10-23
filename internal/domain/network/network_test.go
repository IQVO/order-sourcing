package network

import (
	"testing"
	"time"
)

var (
	t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
)

func TestNodeRegistrationAndStatus(t *testing.T) {
	var n Node
	n.ApplyRegistered(Registered{Type: TypeFulfillmentCenter, Subtype: "SORTABLE", Operator: OperatorVendor, RegionID: "r1", At: t0})
	if n.Status != StatusProposed || !n.StatusAt.Equal(t0) || n.Type != TypeFulfillmentCenter || n.Subtype != "SORTABLE" || n.RegionID != "r1" {
		t.Fatalf("node = %+v", n)
	}
	if !n.ApplyStatus(StatusActive, t1) || n.Status != StatusActive {
		t.Fatal("a newer status applies")
	}
	n.ApplyRegistered(Registered{Type: TypeFulfillmentCenter, Operator: OperatorVendor, RegionID: "r2", At: t0})
	if n.Status != StatusActive || n.RegionID != "r2" {
		t.Fatal("a registration never moves a known status")
	}
	wantStatusRules(t, n)
}

func wantStatusRules(t *testing.T, n Node) {
	t.Helper()
	if n.ApplyStatus(StatusSuspended, t0) {
		t.Fatal("an older status is ignored")
	}
	if n.ApplyStatus(StatusActive, t1) {
		t.Fatal("the same status at the same instant is not a change")
	}
	if !n.ApplyStatus(StatusSuspended, t1) || n.Status != StatusSuspended {
		t.Fatal("a status at the same instant that differs applies (equal instants are not older)")
	}
	if !n.ApplyStatus(StatusRetired, t1.Add(time.Hour)) || n.ApplyStatus(StatusActive, t1.Add(2*time.Hour)) {
		t.Fatal("RETIRED is final")
	}
}

func TestNodeProfileRevision(t *testing.T) {
	var n Node
	if !n.ApplyProfile(2, 95, 30) || n.HandlingMinutes != 95 || n.SortDwellMinutes != 30 || n.ProfileRevision != 2 {
		t.Fatalf("node = %+v", n)
	}
	if n.ApplyProfile(2, 10, 10) || n.ApplyProfile(1, 10, 10) || n.HandlingMinutes != 95 {
		t.Fatal("an equal or lower revision is ignored")
	}
	if !n.ApplyProfile(3, 60, 0) || n.HandlingMinutes != 60 {
		t.Fatal("a higher revision applies")
	}
}

func TestNodeEligible(t *testing.T) {
	ok := Node{Type: TypeFulfillmentCenter, Operator: OperatorVendor, Status: StatusActive, ProfileRevision: 1}
	if !ok.Eligible() {
		t.Fatal("setup")
	}
	for name, mut := range map[string]func(*Node){
		"type": func(n *Node) { n.Type = "SORTATION_CENTER" }, "operator": func(n *Node) { n.Operator = "NETWORK" },
		"status": func(n *Node) { n.Status = StatusSuspended }, "profile": func(n *Node) { n.ProfileRevision = 0 },
	} {
		n := ok
		mut(&n)
		if n.Eligible() {
			t.Errorf("%s: node must not be eligible", name)
		}
	}
}

func TestDepartureAndWaveMerge(t *testing.T) {
	var d Departure
	d.ApplyCapacity(0)
	d.ApplySchedule(Departure{ID: "d1", Capacity: 50, CutoffAt: t0})
	if d.Remaining != 0 || !d.Scheduled || d.Usable() || d.CutoffAt != t0 {
		t.Fatalf("capacity before schedule must survive: %+v", d)
	}
	d.ApplyCapacity(7)
	if !d.Usable() {
		t.Fatal("remaining capacity makes it usable")
	}
	d.Cancelled = true
	d.ApplySchedule(Departure{ID: "d1", Capacity: 60})
	if !d.Cancelled || d.Usable() || d.Remaining != 7 {
		t.Fatalf("a cancelled departure stays cancelled: %+v", d)
	}
	var fresh Departure
	fresh.ApplySchedule(Departure{ID: "d2", Capacity: 9})
	if fresh.Remaining != 9 || !fresh.Usable() {
		t.Fatalf("remaining defaults to capacity: %+v", fresh)
	}
	if (Departure{Remaining: 3}).Usable() {
		t.Fatal("an unscheduled stub is not usable")
	}

	wantWaveMerge(t)
}

func wantWaveMerge(t *testing.T) {
	t.Helper()
	var w Wave
	w.ApplyCapacity(0)
	w.ApplySchedule(Wave{ID: "w1", Capacity: 100})
	if w.Remaining != 0 || w.Usable() {
		t.Fatalf("wave stub: %+v", w)
	}
	w.ApplyCapacity(4)
	w.Cancelled = true
	w.ApplySchedule(Wave{ID: "w1", Capacity: 100})
	if w.Usable() || !w.Cancelled || w.Remaining != 4 {
		t.Fatalf("cancelled wave: %+v", w)
	}
	var fw Wave
	fw.ApplySchedule(Wave{ID: "w2", Capacity: 5})
	if fw.Remaining != 5 || !fw.Usable() || (Wave{Remaining: 2}).Usable() {
		t.Fatalf("fresh wave: %+v", fw)
	}
}

func TestServiceAreaScorecardListing(t *testing.T) {
	if !(ServiceArea{Version: 2}).Newer(ServiceArea{Version: 1}) || (ServiceArea{Version: 2}).Newer(ServiceArea{Version: 2}) {
		t.Fatal("only a higher assignment version replaces")
	}
	if !(Scorecard{ComputedAt: t1}).Newer(Scorecard{ComputedAt: t0}) || !(Scorecard{ComputedAt: t0}).Newer(Scorecard{ComputedAt: t0}) ||
		(Scorecard{ComputedAt: t0}).Newer(Scorecard{ComputedAt: t1}) {
		t.Fatal("a scorecard that is not older replaces")
	}
	l := Listing{AvailableQty: 5, AsOf: t1}
	if l.ApplyAvailability(9, t0) || l.AvailableQty != 5 {
		t.Fatal("an older asOf is ignored")
	}
	if !l.ApplyAvailability(9, t1) || l.AvailableQty != 9 {
		t.Fatal("the same asOf applies")
	}
	if !l.CanShip(9) || l.CanShip(10) {
		t.Fatal("quantity must be covered in full")
	}
	l.Suppressed = true
	if l.CanShip(1) {
		t.Fatal("a suppressed listing cannot ship")
	}
}
