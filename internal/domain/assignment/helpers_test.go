package assignment

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var (
	t0       = time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	deadline = time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)
)

func at(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }

func newInput() New {
	return New{
		ID: "asg-1", OrderID: "ord-1", GroupNo: 1, DestinationPostalPrefix: "30301",
		Promise: PromiseConstraint{PromiseID: "prm-1", Tier: TierTwoDay, DeliverByLatest: deadline, WitnessNodeID: "node-a"},
		Lines:   []Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}, {LineNo: 2, CatalogItemID: "cat-2", Quantity: 1}},
	}
}

func cand(node string) *Candidate {
	return &Candidate{
		NodeID: node, PickupDepartureID: "dep-" + node, PickupCutoffAt: at(5), Legs: []string{"leg-1"},
		DeliveryWaveID: "wave-1", ETA: at(30), Cost: 130,
	}
}

func releaseLines() []Line {
	return []Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2}, {LineNo: 2, CatalogItemID: "cat-2", ListingID: "lst-2", Quantity: 1}}
}

func mustQueue(t *testing.T) *Assignment {
	t.Helper()
	a, _, err := Queue(newInput(), cand("node-a"), at(2), t0)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	return a
}

func mustRelease(t *testing.T) *Assignment {
	t.Helper()
	a := mustQueue(t)
	if _, err := a.Release(cand("node-a"), releaseLines(), at(2)); err != nil {
		t.Fatalf("Release: %v", err)
	}
	return a
}

func mustLock(t *testing.T) *Assignment {
	t.Helper()
	a := mustRelease(t)
	if _, err := a.Lock(1, at(3), at(3)); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	return a
}

func mustShip(t *testing.T, tracking ...string) *Assignment {
	t.Helper()
	a := mustLock(t)
	if _, err := a.MarkShipped(tracking, at(4), at(4)); err != nil {
		t.Fatalf("MarkShipped: %v", err)
	}
	return a
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func wantNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func eventNames(events []shared.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.EventName())
	}
	return out
}

func wantEvents(t *testing.T, events []shared.Event, names ...string) {
	t.Helper()
	got := eventNames(events)
	if len(got) == 0 && len(names) == 0 {
		return
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("events = %v, want %v", got, names)
	}
}

func wantState(t *testing.T, a *Assignment, s State, version int64) {
	t.Helper()
	if a.State() != s || a.Version() != version {
		t.Fatalf("state/version = %s/%d, want %s/%d", a.State(), a.Version(), s, version)
	}
}

// wantEventVersions checks that every event carries the aggregate's version after the change.
func wantEventVersions(t *testing.T, a *Assignment, events []shared.Event) {
	t.Helper()
	for _, e := range events {
		if e.AggregateVersion() != a.Version() || e.SubjectID() != string(a.ID()) || e.Entity() != "assignment" || e.OccurredAt().IsZero() {
			t.Fatalf("event %s: version %d subject %q entity %q at %v", e.EventName(), e.AggregateVersion(), e.SubjectID(), e.Entity(), e.OccurredAt())
		}
	}
}
