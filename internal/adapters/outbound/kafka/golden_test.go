package kafka_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var update = flag.Bool("update", false, "rewrite the golden CloudEvents under testdata/golden")

// TestGoldenCloudEvents pins the exact JSON of every published event, byte for byte (indented for review),
// so that a renamed field, a changed type or a dropped attribute fails here before it reaches a consumer.
func TestGoldenCloudEvents(t *testing.T) {
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	for _, ev := range allEvents() {
		t.Run(ev.EventName(), func(t *testing.T) {
			got := encodeIndented(t, enc, ev)
			path := filepath.Join("testdata", "golden", ev.EventName()+".json")
			if *update {
				writeGolden(t, path, got)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("%s differs from its golden file\n got:\n%s\nwant:\n%s", ev.EventName(), got, want)
			}
		})
	}
}

func encodeIndented(t *testing.T, enc kafka.Encoder, ev shared.Event) []byte {
	t.Helper()
	m, err := enc.Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, m.Value, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	return pretty.Bytes()
}

func writeGolden(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestGoldenSourcingDecidedWithUnknownFacts pins the omissions of the decision
// fact (ADR 0013): an unknown region drops the region and inRegion, an
// incomplete estimate drops estimatedCostCents and sets costComplete false.
// The fully known form is FulfillmentSourcingDecided.json above.
func TestGoldenSourcingDecidedWithUnknownFacts(t *testing.T) {
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	ev := assignment.FulfillmentSourcingDecided{Base: base(), OrderID: "ord-1", GroupNo: 2, ReleaseNo: 2, NodeID: "n2",
		DestinationRegionID: "reg-se", Legs: 0, Units: 1, CandidateCount: 1, DecidedAt: at}
	got := encodeIndented(t, enc, ev)
	path := filepath.Join("testdata", "golden", "FulfillmentSourcingDecided.unknown-facts.json")
	if *update {
		writeGolden(t, path, got)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("unknown-facts decision differs from its golden file\n got:\n%s\nwant:\n%s", got, want)
	}
	for _, absent := range []string{"nodeRegionId", "inRegion", "estimatedCostCents", "estimatedCo2eGrams", "emissionComplete"} {
		if bytes.Contains(got, []byte(`"`+absent+`"`)) {
			t.Errorf("%s must be omitted when unknown:\n%s", absent, got)
		}
	}
	if !bytes.Contains(got, []byte(`"costComplete": false`)) || !bytes.Contains(got, []byte(`"legs": 0`)) {
		t.Errorf("costComplete false and legs 0 are always present:\n%s", got)
	}
}

// TestGoldenReleasedToNodeWithASellerOwnedLine pins lines[].ownerRef: present
// on the seller-owned line, omitted on the operator-owned one (ADR 0012). The
// operator-only form is FulfillmentReleasedToNode.json above. FulfillmentQueued
// never carries an owner, even when its lines have one.
func TestGoldenReleasedToNodeWithASellerOwnedLine(t *testing.T) {
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	mixed := []assignment.Line{
		{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2},
		{LineNo: 2, CatalogItemID: "cat-2", ListingID: "lst-2", OwnerRef: "sel-1", Quantity: 1},
	}
	ev := assignment.FulfillmentReleasedToNode{Base: base(), ReleaseNo: 1, OrderID: "ord-1", GroupNo: 1, NodeID: "n1", PickupDepartureID: "dep-1",
		RequiredShipBy: at, PromisedDeliverByLatest: at, Tier: assignment.TierTwoDay, Lines: mixed, EstimatedCostCents: &complete}
	got := encodeIndented(t, enc, ev)
	path := filepath.Join("testdata", "golden", "FulfillmentReleasedToNode.seller-owned.json")
	if *update {
		writeGolden(t, path, got)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("seller-owned release differs from its golden file\n got:\n%s\nwant:\n%s", got, want)
	}
	queued := encodeIndented(t, enc, assignment.FulfillmentQueued{Base: base(), OrderID: "ord-1", GroupNo: 1, PromiseID: "prm-1", DeliverByLatest: at, Lines: mixed})
	if bytes.Contains(queued, []byte("ownerRef")) {
		t.Fatalf("FulfillmentQueued must not carry ownerRef:\n%s", queued)
	}
}
