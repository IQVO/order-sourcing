package report_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

func f(v float64) *float64 { return &v }

func TestSourcingMixDerivesTheKPIs(t *testing.T) {
	day := utcDay(2026, 11, 3)
	got := report.SourcingMix([]report.MixDay{
		{Day: day, Releases: 4, InRegion: 2, OutOfRegion: 1, UnknownRegion: 1, Legs: 6, Reassignments: 1, Unsourceable: 2},
	})
	want := []report.MixRow{{
		MixDay:        report.MixDay{Day: day, Releases: 4, InRegion: 2, OutOfRegion: 1, UnknownRegion: 1, Legs: 6, Reassignments: 1, Unsourceable: 2},
		InRegionShare: f(2.0 / 3), MeanLegs: f(1.5), ReassignmentRate: f(0.25),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %+v\nwant %+v", got, want)
	}
}

func TestSourcingMixOfADayWithoutReleases(t *testing.T) {
	got := report.SourcingMix([]report.MixDay{{Day: utcDay(2026, 11, 4), Unsourceable: 3}})
	if got[0].InRegionShare != nil || got[0].MeanLegs != nil || got[0].ReassignmentRate != nil || got[0].Unsourceable != 3 {
		t.Fatalf("row = %+v", got[0])
	}
	unknown := report.SourcingMix([]report.MixDay{{Day: utcDay(2026, 11, 4), Releases: 2, UnknownRegion: 2, Legs: 0}})
	if unknown[0].InRegionShare != nil || unknown[0].MeanLegs == nil || *unknown[0].MeanLegs != 0 {
		t.Fatalf("only unknown regions: %+v", unknown[0])
	}
	if empty := report.SourcingMix(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("nil input must yield an empty, non-nil slice, got %#v", empty)
	}
}

func TestOrderSplitsDerivesTheKPIs(t *testing.T) {
	day := utcDay(2026, 11, 3)
	got := report.OrderSplits([]report.SplitDay{{Day: day, Orders: 4, Groups: 7, SplitOrders: 2}, {Day: day.AddDate(0, 0, 1)}})
	if len(got) != 2 || *got[0].GroupsPerOrder != 1.75 || *got[0].SplitOrderShare != 0.5 || got[0].Orders != 4 {
		t.Fatalf("rows = %+v", got)
	}
	if got[1].GroupsPerOrder != nil || got[1].SplitOrderShare != nil {
		t.Fatalf("a day without orders has no shares: %+v", got[1])
	}
	if empty := report.OrderSplits(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("nil input must yield an empty, non-nil slice, got %#v", empty)
	}
}

func TestCostToServeDerivesTheKPIs(t *testing.T) {
	day := utcDay(2026, 11, 3)
	got := report.CostToServe([]report.CostDay{
		{Day: day, Decisions: 4, Complete: 3, CostCents: 900, Units: 6, P50: f(300), P90: f(340)},
		{Day: day.AddDate(0, 0, 1), Decisions: 2},
	})
	if *got[0].CostCompleteShare != 0.75 || *got[0].CostPerUnitCents != 150 || *got[0].P50 != 300 || *got[0].P90 != 340 {
		t.Fatalf("row = %+v", got[0])
	}
	if *got[1].CostCompleteShare != 0 || got[1].CostPerUnitCents != nil || got[1].P50 != nil {
		t.Fatalf("no complete decision: %+v", got[1])
	}
	if empty := report.CostToServe(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("nil input must yield an empty, non-nil slice, got %#v", empty)
	}
}

func TestShare(t *testing.T) {
	cases := []struct {
		num, den int
		want     *float64
	}{{1, 4, f(0.25)}, {3, 3, f(1)}, {0, 5, f(0)}, {2, 0, nil}, {2, -1, nil}, {1, 1, f(1)}}
	for _, c := range cases {
		if got := report.Share(c.num, c.den); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Share(%d, %d) = %v, want %v", c.num, c.den, got, c.want)
		}
	}
}

func TestCostPerUnit(t *testing.T) {
	if got := report.CostPerUnit(900, 0); got != nil {
		t.Fatalf("no units = %v, want nil", *got)
	}
	if got := report.CostPerUnit(900, -2); got != nil {
		t.Fatalf("negative units = %v, want nil", *got)
	}
	if got := report.CostPerUnit(5, 2); got == nil || *got != 2.5 {
		t.Fatalf("CostPerUnit(5, 2) = %v, want 2.5", got)
	}
	if got := report.CostPerUnit(7, 1); got == nil || *got != 7 {
		t.Fatalf("CostPerUnit(7, 1) = %v, want 7", got)
	}
}

func TestPercentileMatchesPercentileCont(t *testing.T) {
	values := []float64{7, 1, 3, 5}
	cases := []struct{ p, want float64 }{{0.5, 4}, {0.9, 6.4}, {0, 1}, {1, 7}, {0.25, 2.5}}
	for _, c := range cases {
		if got := report.Percentile(values, c.p); got == nil || *got < c.want-1e-9 || *got > c.want+1e-9 {
			t.Errorf("Percentile(%v) = %v, want %v", c.p, got, c.want)
		}
	}
	if !reflect.DeepEqual(values, []float64{7, 1, 3, 5}) {
		t.Fatalf("Percentile modified its input: %v", values)
	}
	if got := report.Percentile(nil, 0.5); got != nil {
		t.Fatalf("empty = %v, want nil", *got)
	}
	if got := report.Percentile([]float64{3}, 0.9); got == nil || *got != 3 {
		t.Fatalf("single = %v, want 3", got)
	}
	if report.CostP50() != 0.5 || report.CostP90() != 0.9 {
		t.Fatalf("ranks = %v, %v", report.CostP50(), report.CostP90())
	}
}

func TestComputeFreshness(t *testing.T) {
	if fr := report.ComputeFreshness(nil, now); fr.AsOf != nil || fr.LagSeconds != nil {
		t.Fatalf("nothing applied must give nil/nil, got %+v", fr)
	}
	asOf := time.Date(2026, 11, 8, 18, 29, 0, 0, time.UTC) // now is 18:30 UTC
	fr := report.ComputeFreshness(&asOf, now)
	if fr.AsOf == nil || !fr.AsOf.Equal(asOf) || fr.AsOf.Location() != time.UTC || fr.LagSeconds == nil || *fr.LagSeconds != 60 {
		t.Fatalf("freshness = %+v", fr)
	}
	future := now.Add(time.Minute)
	if fr := report.ComputeFreshness(&future, now); *fr.LagSeconds != 0 {
		t.Fatalf("a clock behind the event must read zero lag, got %v", *fr.LagSeconds)
	}
}
