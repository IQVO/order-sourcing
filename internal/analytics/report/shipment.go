package report

import (
	"strconv"
	"time"
)

// The split-shipment rules of ADR 0018. A decision is a sourced order; its
// shipment groups are the distinct shipments among the current releases of its
// assignments, a follower of a merged release sharing its carrier's shipment.

// ShipmentBuckets is the number of buckets of the groups-per-decision
// distribution: 1, 2, 3 and 4+ (the last holds every larger count).
const ShipmentBuckets = 4

// BucketOf is the index (0-based) of the bucket a decision with this many
// shipment groups falls in. Counts above the last bucket land in it; a count
// below 1 cannot be told from 1 and lands in the first.
func BucketOf(groups int) int { return min(max(groups, 1), ShipmentBuckets) - 1 }

// BucketLabel is the wire label of bucket i: "1", "2", "3" and "4+".
func BucketLabel(i int) string {
	if i == ShipmentBuckets-1 {
		return strconv.Itoa(ShipmentBuckets) + "+"
	}
	return strconv.Itoa(i + 1)
}

// ShipmentKey identifies the shipment (package) of a current release: its
// carrier's assignment id when the fact names a carrier, else its own.
func ShipmentKey(assignmentID, carrierAssignmentID string) string {
	if carrierAssignmentID != "" {
		return carrierAssignmentID
	}
	return assignmentID
}

// ShipmentCount is what the store counts for one UTC day of first decision:
// the orders (decisions) that have exactly Groups shipment groups.
type ShipmentCount struct {
	Day       time.Time
	Groups    int
	Decisions int
}

// SplitShipment is the split-shipment block of one day: the decisions, those
// with 2 or more shipment groups, their share (nil without a decision) and the
// distribution of shipment groups over the buckets (BucketOf).
type SplitShipment struct {
	Decisions      int
	SplitDecisions int
	SplitShare     *float64
	Buckets        [ShipmentBuckets]int
}

// splitShipments folds the store's counts into one block per UTC day (keyed by
// FormatDay). A day without counts has no key; blockOf then yields the zero
// block.
func splitShipments(counts []ShipmentCount) map[string]SplitShipment {
	out := map[string]SplitShipment{}
	for _, c := range counts {
		key := FormatDay(c.Day)
		s := out[key]
		s.Buckets[BucketOf(c.Groups)] += c.Decisions
		out[key] = s
	}
	for key, s := range out {
		for _, n := range s.Buckets {
			s.Decisions += n
		}
		s.SplitDecisions = s.Decisions - s.Buckets[BucketOf(SingleGroup())]
		s.SplitShare = Share(s.SplitDecisions, s.Decisions)
		out[key] = s
	}
	return out
}
