package report

import (
	"math"
	"sort"
	"time"
)

// MixRow is a MixDay plus the KPIs of ADR 0013: the in-region share over the
// decisions with a known region, the mean legs and the reassignment rate over
// the releases. Each is nil when its denominator is 0.
type MixRow struct {
	MixDay
	InRegionShare    *float64
	MeanLegs         *float64
	ReassignmentRate *float64
}

// SourcingMix derives each day's KPIs. Order is preserved; a nil input yields
// an empty, non-nil slice.
func SourcingMix(days []MixDay) []MixRow {
	out := make([]MixRow, 0, len(days))
	for _, d := range days {
		out = append(out, MixRow{
			MixDay:           d,
			InRegionShare:    Share(d.InRegion, d.InRegion+d.OutOfRegion),
			MeanLegs:         Share(d.Legs, d.Releases),
			ReassignmentRate: Share(d.Reassignments, d.Releases),
		})
	}
	return out
}

// SplitRow is a SplitDay plus the mean groups per order and the share of split
// orders (more than one group), nil without orders.
type SplitRow struct {
	SplitDay
	GroupsPerOrder  *float64
	SplitOrderShare *float64
}

// OrderSplits derives each day's KPIs. Order is preserved; a nil input yields
// an empty, non-nil slice.
func OrderSplits(days []SplitDay) []SplitRow {
	out := make([]SplitRow, 0, len(days))
	for _, d := range days {
		out = append(out, SplitRow{SplitDay: d, GroupsPerOrder: Share(d.Groups, d.Orders), SplitOrderShare: Share(d.SplitOrders, d.Orders)})
	}
	return out
}

// CostRow is a CostDay plus the share of complete estimates over the decisions
// and the cost per unit of the complete ones (nil when undefined).
type CostRow struct {
	CostDay
	CostCompleteShare *float64
	CostPerUnitCents  *float64
}

// CostToServe derives each day's KPIs. Order is preserved; a nil input yields
// an empty, non-nil slice.
func CostToServe(days []CostDay) []CostRow {
	out := make([]CostRow, 0, len(days))
	for _, d := range days {
		out = append(out, CostRow{CostDay: d, CostCompleteShare: Share(d.Complete, d.Decisions), CostPerUnitCents: CostPerUnit(d.CostCents, d.Units)})
	}
	return out
}

// Share is num/den, or nil when den is not positive (nothing to divide by).
func Share(num, den int) *float64 {
	if den <= 0 {
		return nil
	}
	v := float64(num) / float64(den)
	return &v
}

// CostPerUnit is cents/units, or nil when units is not positive.
func CostPerUnit(cents int64, units int) *float64 {
	if units <= 0 {
		return nil
	}
	v := float64(cents) / float64(units)
	return &v
}

// Percentile is the continuous percentile (linear interpolation between
// closest ranks, p in [0,1]) of values, the same definition as Postgres'
// percentile_cont. values need not be sorted; it is not modified. An empty
// input yields nil, like percentile_cont over no rows.
func Percentile(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := p * float64(len(sorted)-1)
	lo := math.Floor(rank)
	hi := math.Ceil(rank)
	v := sorted[int(lo)] + (sorted[int(hi)]-sorted[int(lo)])*(rank-lo)
	return &v
}

// Freshness is the projection's freshness: AsOf is the CloudEvents time of the
// newest event applied and LagSeconds is now - AsOf. Both are nil while the
// projection has applied nothing.
type Freshness struct {
	AsOf       *time.Time
	LagSeconds *float64
}

// ComputeFreshness derives the lag from the newest applied event time. A clock
// that is behind the event (skew) reads as zero lag, never negative.
func ComputeFreshness(asOf *time.Time, now time.Time) Freshness {
	if asOf == nil {
		return Freshness{}
	}
	// math.Max, not `if lag < 0`: both yield 0 at the boundary, which would
	// leave an equivalent `<` -> `<=` mutant.
	lag := math.Max(0, now.Sub(*asOf).Seconds())
	utc := asOf.UTC()
	return Freshness{AsOf: &utc, LagSeconds: &lag}
}
