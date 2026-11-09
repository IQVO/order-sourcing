package usecases_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

func avail(id, item, node, owner string, qty int, asOf time.Time) network.Listing {
	return network.Listing{ID: id, CatalogItemID: item, NodeID: node, OwnerRef: owner, AvailableQty: qty, AsOf: asOf}
}

// TestProjectorKeysAvailabilityByOwner: two owners' listings of one item at one
// node are two rows of AvailabilityView, each with its own quantity and
// suppression state; an absent ownerRef is operator-owned (ADR 0012).
func TestProjectorKeysAvailabilityByOwner(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	e.must(p.ListingCreated(ctx, meta("o1"), network.Listing{ID: "lst-op", CatalogItemID: "cat-7", NodeID: "n1"}))
	e.must(p.ListingCreated(ctx, meta("o2"), network.Listing{ID: "lst-s1", CatalogItemID: "cat-7", NodeID: "n1", OwnerRef: "sel-1"}))
	e.must(p.ListingAvailabilityChanged(ctx, meta("o3"), avail("lst-op", "cat-7", "n1", "", 3, h(1))))
	e.must(p.ListingAvailabilityChanged(ctx, meta("o4"), avail("lst-s1", "cat-7", "n1", "sel-1", 6, h(1))))
	e.must(p.ListingSuppressionChanged(ctx, meta("o5"), "lst-s1", true))
	w, err := e.store.World().Load(ctx, repository.WorldQuery{DestinationPostalPrefix: "30301", CatalogItemIDs: []string{"cat-7"}, Now: h(0)})
	if err != nil {
		t.Fatal(err)
	}
	want := []network.Listing{
		{ID: "lst-op", CatalogItemID: "cat-7", NodeID: "n1", AvailableQty: 3, AsOf: h(1)},
		{ID: "lst-s1", CatalogItemID: "cat-7", NodeID: "n1", OwnerRef: "sel-1", AvailableQty: 6, AsOf: h(1), Suppressed: true},
	}
	if !reflect.DeepEqual(w.Listings, want) {
		t.Fatalf("listings = %+v, want %+v", w.Listings, want)
	}
	if w.Listings[0].Key() == w.Listings[1].Key() {
		t.Fatal("the two owners must be two keys")
	}
}

// TestProjectorTakesTheOwnerFromTheAvailabilityEvent: ListingAvailabilityChanged
// carries ownerRef too, and its absence means operator-owned.
func TestProjectorTakesTheOwnerFromTheAvailabilityEvent(t *testing.T) {
	e := newEnv(t)
	p := usecases.NewProjector(e.deps)
	ctx := context.Background()
	rm := e.store.ReadModels()
	e.must(p.ListingAvailabilityChanged(ctx, meta("q1"), avail("lst-x", "cat-7", "n1", "sel-2", 4, h(1))))
	if l, _ := rm.GetListing(ctx, "lst-x"); l.OwnerRef != "sel-2" || l.AvailableQty != 4 {
		t.Fatalf("listing = %+v", l)
	}
	e.must(p.ListingAvailabilityChanged(ctx, meta("q2"), avail("lst-y", "cat-7", "n1", "", 2, h(1))))
	if l, _ := rm.GetListing(ctx, "lst-y"); l.OwnerRef != network.OperatorOwned || l.Key().OwnerRef != "" {
		t.Fatalf("absent ownerRef must be operator-owned: %+v", l)
	}
}
