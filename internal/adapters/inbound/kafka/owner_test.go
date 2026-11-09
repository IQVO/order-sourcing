package kafka_test

import (
	"context"
	"testing"

	consumer "github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
)

// TestListingEventsCarryTheOwner decodes the optional ownerRef of ListingCreated
// and ListingAvailabilityChanged into the AvailabilityView key, an absent one
// meaning operator-owned (ADR 0012).
func TestListingEventsCarryTheOwner(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	c := f.consumerFor(consumer.OfferAvailabilityTopic)
	const p = "com.retailnetwork.commerce.offer-availability.listing."
	msgs := [][]byte{
		event(t, p+"ListingCreated", "evt-o1", "lst-s", map[string]any{"listingId": "lst-s", "catalogItemId": "cat-1", "nodeId": "n1", "ownerRef": "sel-1"}),
		event(t, p+"ListingCreated", "evt-o2", "lst-op", map[string]any{"listingId": "lst-op", "catalogItemId": "cat-1", "nodeId": "n1"}),
		event(t, p+"ListingAvailabilityChanged", "evt-o3", "lst-s", map[string]any{"listingId": "lst-s", "catalogItemId": "cat-1", "nodeId": "n1",
			"ownerRef": "sel-1", "availableQty": 6, "basis": "PHYSICAL", "asOf": "2026-10-22T12:00:00Z", "feedId": "feed-1"}),
	}
	for _, m := range msgs {
		if err := c.HandleMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	rm := f.store.ReadModels()
	if l, err := rm.GetListing(ctx, "lst-s"); err != nil || l.OwnerRef != "sel-1" || l.AvailableQty != 6 {
		t.Fatalf("seller listing = %+v %v", l, err)
	}
	if l, err := rm.GetListing(ctx, "lst-op"); err != nil || l.OwnerRef != "" || l.CatalogItemID != "cat-1" {
		t.Fatalf("operator listing = %+v %v", l, err)
	}
}
