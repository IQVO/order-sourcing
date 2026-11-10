package kafka_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// mergedMembers is the carrier and one follower of a merged release (ADR 0015).
func mergedMembers() []assignment.MergedMember {
	return []assignment.MergedMember{
		{ID: "asg-7f3c2d10", OrderID: "ord-1", GroupNo: 1, PromiseID: "prm-1", DeliverByLatest: at,
			Lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2}}},
		{ID: "asg-2c9d41aa", OrderID: "ord-2", GroupNo: 1, PromiseID: "prm-2", DeliverByLatest: at,
			Lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 3}}},
	}
}

// mergedGoldens are the merged and refused forms of the changed events, pinned
// byte for byte next to the plain form of allEvents. Absent members, absent
// carrierAssignmentId and so on are the unmerged form, pinned by the plain files.
func mergedGoldens() map[string]shared.Event {
	merged := []assignment.Line{
		{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2},
		{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 3},
	}
	ids := []assignment.ID{"asg-2c9d41aa", "asg-7f3c2d10"}
	return map[string]shared.Event{
		"FulfillmentReleasedToNode.merged": assignment.FulfillmentReleasedToNode{Base: base(), ReleaseNo: 1, OrderID: "ord-1", GroupNo: 1, NodeID: "n1",
			PickupDepartureID: "dep-1", RequiredShipBy: at, PromisedDeliverByLatest: at, Tier: assignment.TierStandard, Lines: merged,
			EstimatedCostCents: &complete, Members: mergedMembers(), LineMembers: []assignment.ID{"asg-7f3c2d10", "asg-2c9d41aa"}},
		"FulfillmentLocked.carrier": assignment.FulfillmentLocked{Base: base(), OrderID: "ord-1", GroupNo: 1, PromiseID: "prm-1", NodeID: "n1",
			PickupDepartureID: "dep-1", LockedAt: at, LockReason: "PURCHASE_ORDER_ACCEPTED", MergedAssignmentIDs: ids},
		"FulfillmentLocked.follower": assignment.FulfillmentLocked{Base: base(), OrderID: "ord-2", GroupNo: 1, PromiseID: "prm-2", NodeID: "n1",
			PickupDepartureID: "dep-1", LockedAt: at, LockReason: "PURCHASE_ORDER_ACCEPTED", ConsolidatedInto: "asg-7f3c2d10", MergedAssignmentIDs: ids},
		"FulfillmentSourcingDecided.follower": assignment.FulfillmentSourcingDecided{Base: base(), OrderID: "ord-2", GroupNo: 1, ReleaseNo: 1, NodeID: "n1",
			NodeRegionID: "reg-se", DestinationRegionID: "reg-se", Legs: 1, EstimatedCostCents: &complete, Units: 3, CandidateCount: 3, DecidedAt: at,
			CarrierAssignmentID: "asg-7f3c2d10", ConsolidatedCount: 2},
		"FulfillmentCancelRefused.merged": assignment.FulfillmentCancelRefused{Base: base(), OrderID: "ord-1", GroupNo: 1, Reason: assignment.ReasonCancelWhileMerged},
		"AssignmentsDeconsolidated.refusal": assignment.AssignmentsDeconsolidated{Base: base(), ReleaseNo: 1, Reason: assignment.ReasonPurchaseOrderRejected,
			MemberAssignmentIDs: []assignment.ID{"asg-7f3c2d10", "asg-2c9d41aa"}, DeconsolidatedAt: at},
	}
}

func TestGoldenMergedAndRefusedForms(t *testing.T) {
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	for name, ev := range mergedGoldens() {
		t.Run(name, func(t *testing.T) {
			got := encodeIndented(t, enc, ev)
			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				writeGolden(t, path, got)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("%s differs from its golden file\n got:\n%s\nwant:\n%s", name, got, want)
			}
		})
	}
}

// TestUnmergedEventsCarryNoMergeField pins the omission rule of ADR 0015: an
// unmerged release, lock and decision carry none of the merge fields.
func TestUnmergedEventsCarryNoMergeField(t *testing.T) {
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	absent := []string{"members", "consolidatedInto", "mergedAssignmentIds", "carrierAssignmentId", "consolidatedCount"}
	for _, ev := range allEvents()[:5] {
		got := encodeIndented(t, enc, ev)
		for _, field := range absent {
			if bytes.Contains(got, []byte(`"`+field+`"`)) {
				t.Errorf("%s: %s must be omitted when not merged:\n%s", ev.EventName(), field, got)
			}
		}
	}
	rel := encodeIndented(t, enc, allEvents()[1])
	if bytes.Count(rel, []byte(`"assignmentId"`)) != 1 {
		t.Errorf("an unmerged release carries no per-line assignmentId:\n%s", rel)
	}
}
