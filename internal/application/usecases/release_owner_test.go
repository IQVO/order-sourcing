package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// releasedLines returns the lines of the last FulfillmentReleasedToNode in the outbox.
func (e *env) releasedLines() []assignment.Line {
	e.t.Helper()
	evs := e.store.Outbox().Events()
	for i := len(evs) - 1; i >= 0; i-- {
		if r, ok := evs[i].(assignment.FulfillmentReleasedToNode); ok {
			return r.Lines
		}
	}
	e.t.Fatal("no FulfillmentReleasedToNode in the outbox")
	return nil
}

// TestReleaseToNodePublishesTheChosenOwner: the operator's stock at fc-a is
// short, a seller's covers the line, so the release takes the seller's listing
// and publishes its ownerRef; the persisted line carries it too (ADR 0012).
func TestReleaseToNodePublishesTheChosenOwner(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	ctx := context.Background()
	rm := e.store.ReadModels()
	e.must(rm.PutListing(ctx, network.Listing{ID: "lst-1-fc-a", CatalogItemID: "cat-1", NodeID: "fc-a", AvailableQty: 1, AsOf: h(1)}))
	e.must(rm.PutListing(ctx, network.Listing{ID: "lst-s-fc-a", CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: "sel-1", AvailableQty: 4, AsOf: h(1)}))
	got, err := usecases.NewReleaseToNode(e.deps).Execute(ctx, id)
	e.must(err)
	if got.Releases[0].NodeID != "fc-a" || got.Lines[0].ListingID != "lst-s-fc-a" || got.Lines[0].OwnerRef != "sel-1" {
		t.Fatalf("view = %+v", got)
	}
	if l := e.releasedLines(); len(l) != 1 || l[0].OwnerRef != "sel-1" || l[0].ListingID != "lst-s-fc-a" {
		t.Fatalf("published lines = %+v", l)
	}
	if l := e.get(id).Lines(); l[0].OwnerRef != "sel-1" {
		t.Fatalf("persisted lines = %+v", l)
	}
}

// TestReleaseToNodeKeepsOperatorStockFirst: with operator stock covering the
// line, the release is operator-owned and the published line has no owner.
func TestReleaseToNodeKeepsOperatorStockFirst(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	ctx := context.Background()
	e.must(e.store.ReadModels().PutListing(ctx, network.Listing{ID: "lst-s-fc-a", CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: "sel-1", AvailableQty: 50, AsOf: h(1)}))
	e.release(id)
	if l := e.releasedLines(); len(l) != 1 || l[0].OwnerRef != "" || l[0].ListingID != "lst-1-fc-a" {
		t.Fatalf("published lines = %+v", l)
	}
}
