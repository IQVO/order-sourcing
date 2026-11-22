package report_test

import (
	"reflect"
	"testing"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

func TestBucketOfCapsAtTheLastBucketAndFloorsAtTheFirst(t *testing.T) {
	for groups, want := range map[int]int{-1: 0, 0: 0, 1: 0, 2: 1, 3: 2, 4: 3, 5: 3, 100: 3} {
		if got := report.BucketOf(groups); got != want {
			t.Errorf("BucketOf(%d) = %d, want %d", groups, got, want)
		}
	}
	if report.ShipmentBuckets != 4 {
		t.Fatalf("ShipmentBuckets = %d, want the four buckets 1, 2, 3, 4+", report.ShipmentBuckets)
	}
}

func TestBucketLabels(t *testing.T) {
	var got []string
	for i := range report.ShipmentBuckets {
		got = append(got, report.BucketLabel(i))
	}
	if want := []string{"1", "2", "3", "4+"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func TestShipmentKeyIsTheCarrierWhenThereIsOne(t *testing.T) {
	if got := report.ShipmentKey("asg-2", "asg-1"); got != "asg-1" {
		t.Errorf("a follower ships with its carrier, got %q", got)
	}
	if got := report.ShipmentKey("asg-2", ""); got != "asg-2" {
		t.Errorf("an unmerged release ships on its own, got %q", got)
	}
}

func TestSourcingMixJoinsTheSplitShipmentBlockOfEachDay(t *testing.T) {
	d1, d2, d3 := utcDay(2026, 11, 3), utcDay(2026, 11, 4), utcDay(2026, 11, 5)
	got := report.SourcingMix(
		[]report.MixDay{{Day: d1, Releases: 6}, {Day: d2, Unsourceable: 1}, {Day: d3, Releases: 1}},
		[]report.ShipmentCount{
			{Day: d1, Groups: 1, Decisions: 3}, {Day: d1, Groups: 2, Decisions: 1}, {Day: d1, Groups: 3, Decisions: 1},
			{Day: d1, Groups: 4, Decisions: 1}, {Day: d1, Groups: 9, Decisions: 2},
			{Day: d3, Groups: 1, Decisions: 1},
		})
	if len(got) != 3 {
		t.Fatalf("rows = %+v", got)
	}
	first := got[0].SplitShipment
	if first.Decisions != 8 || first.SplitDecisions != 5 || first.Buckets != [report.ShipmentBuckets]int{3, 1, 1, 3} ||
		first.SplitShare == nil || *first.SplitShare != 5.0/8 {
		t.Errorf("day 1 = %+v, want 8 decisions, 5 split, buckets 3/1/1/3, share 0.625", first)
	}
	if empty := got[1].SplitShipment; empty.Decisions != 0 || empty.SplitDecisions != 0 || empty.SplitShare != nil || empty.Buckets != [report.ShipmentBuckets]int{} {
		t.Errorf("a day without a decision = %+v, want the zero block with a nil share", empty)
	}
	if last := got[2].SplitShipment; last.Decisions != 1 || last.SplitDecisions != 0 || last.SplitShare == nil || *last.SplitShare != 0 {
		t.Errorf("an unsplit day = %+v, want share 0 (not nil)", last)
	}
}

func TestSourcingMixIgnoresShipmentCountsOfADayWithoutARow(t *testing.T) {
	got := report.SourcingMix([]report.MixDay{{Day: utcDay(2026, 11, 3), Releases: 1}},
		[]report.ShipmentCount{{Day: utcDay(2026, 11, 9), Groups: 2, Decisions: 5}})
	if len(got) != 1 || got[0].SplitShipment.Decisions != 0 {
		t.Fatalf("rows = %+v; a count of another day must not leak in", got)
	}
}
