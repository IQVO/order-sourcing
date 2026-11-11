package kafka_test

import (
	"context"
	"strings"
	"testing"
	"time"

	consumer "github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// placeWith seeds the world from the consumed examples and places the example
// order as one CONSOLIDATED group with the given key (absent when "") and a
// window start one day before its latest delivery.
func placeWith(t *testing.T, key string) (*assignment.Assignment, time.Time) {
	t.Helper()
	f := newFixture()
	ctx := context.Background()
	var placed example
	for _, ex := range consumedExamples(t) {
		switch {
		case ex.Name == "CustomerOrderPlaced":
			placed = ex
		case ex.Topic != consumer.CustomerOrderingTopic:
			if err := f.consumerFor(ex.Topic).HandleMessage(ctx, bytesOf(t, ex.Event)); err != nil {
				t.Fatalf("%s: %v", ex.Name, err)
			}
		}
	}
	data := placed.Event["data"].(map[string]any)
	group := data["groups"].([]any)[0].(map[string]any)
	latest, err := time.Parse(time.RFC3339, group["deliverByLatest"].(string))
	if err != nil {
		t.Fatal(err)
	}
	earliest := latest.Add(-24 * time.Hour)
	group["tier"], group["deliverByEarliest"] = "CONSOLIDATED", earliest.Format(time.RFC3339)
	if key != "" {
		data["consolidationKey"] = key
	}
	if err := f.consumerFor(consumer.CustomerOrderingTopic).HandleMessage(ctx, bytesOf(t, placed.Event)); err != nil {
		t.Fatal(err)
	}
	a, err := f.store.Assignments().FindByGroup(ctx, "ord-51a0c9", 1)
	if err != nil {
		t.Fatal(err)
	}
	return a, earliest
}

// TestCustomerOrderPlacedCarriesTheConsolidationKeyAndTheWindowStart: the key and
// the window start are optional and tolerated when absent or malformed (ADR 0015).
func TestCustomerOrderPlacedCarriesTheConsolidationKeyAndTheWindowStart(t *testing.T) {
	good := strings.Repeat("0f", 32)
	for name, tc := range map[string]struct{ key, want string }{
		"a valid key is kept":          {good, good},
		"an absent key means no merge": {"", ""},
		"a malformed key is dropped":   {"ABC", ""},
	} {
		t.Run(name, func(t *testing.T) {
			a, earliest := placeWith(t, tc.key)
			if a.ConsolidationKey() != tc.want || !a.Promise().DeliverByEarliest.Equal(earliest) {
				t.Fatalf("key %q earliest %v", a.ConsolidationKey(), a.Promise().DeliverByEarliest)
			}
		})
	}
}
