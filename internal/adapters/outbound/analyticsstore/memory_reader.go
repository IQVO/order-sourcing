package analyticsstore

import (
	"context"
	"sort"
	"time"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// The MemoryStore's report.Reader half (the same semantics as reader.go).

// SourcingMix implements report.Reader.
func (m *MemoryStore) SourcingMix(_ context.Context, r report.Range) ([]report.MixDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	days := map[time.Time]*report.MixDay{}
	newDay := func(day time.Time) *report.MixDay { return &report.MixDay{Day: day} }
	for _, d := range m.decisions {
		if !inRange(r, d.at) {
			continue
		}
		row := dayOf(days, utcDay(d.at), newDay)
		row.Releases++
		row.Legs += d.Legs
		countRegion(row, d.InRegion)
		if report.IsReassignment(d.ReleaseNo) {
			row.Reassignments++
		}
	}
	for _, at := range m.unsourceable {
		if inRange(r, at) {
			dayOf(days, utcDay(at), newDay).Unsourceable++
		}
	}
	return sortedDays(days, func(d report.MixDay) time.Time { return d.Day }), nil
}

// countRegion adds one decision's region outcome to its day.
func countRegion(row *report.MixDay, inRegion *bool) {
	switch {
	case inRegion == nil:
		row.UnknownRegion++
	case *inRegion:
		row.InRegion++
	default:
		row.OutOfRegion++
	}
}

// OrderSplits implements report.Reader.
func (m *MemoryStore) OrderSplits(_ context.Context, r report.Range) ([]report.SplitDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type order struct {
		first  time.Time
		groups map[int]bool
	}
	orders := map[string]*order{}
	for _, q := range m.queued {
		o := orders[q.order]
		if o == nil {
			o = &order{first: q.at, groups: map[int]bool{}}
			orders[q.order] = o
		}
		if q.at.Before(o.first) {
			o.first = q.at
		}
		o.groups[q.groupNo] = true
	}
	days := map[time.Time]*report.SplitDay{}
	for _, o := range orders {
		if !inRange(r, o.first) {
			continue
		}
		row := dayOf(days, utcDay(o.first), func(day time.Time) *report.SplitDay { return &report.SplitDay{Day: day} })
		row.Orders++
		row.Groups += len(o.groups)
		if report.IsSplit(len(o.groups)) {
			row.SplitOrders++
		}
	}
	return sortedDays(days, func(d report.SplitDay) time.Time { return d.Day }), nil
}

// lastDecisions is the decision with the highest releaseNo of every assignment.
func (m *MemoryStore) lastDecisions() []memDecision {
	last := map[string]memDecision{}
	for k, d := range m.decisions {
		if cur, ok := last[k.assignment]; !ok || d.ReleaseNo > cur.ReleaseNo {
			last[k.assignment] = d
		}
	}
	out := make([]memDecision, 0, len(last))
	for _, d := range last {
		out = append(out, d)
	}
	return out
}

// CostToServe implements report.Reader.
func (m *MemoryStore) CostToServe(_ context.Context, r report.Range) ([]report.CostDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	days := map[time.Time]*report.CostDay{}
	costs := map[time.Time][]float64{}
	for _, d := range m.lastDecisions() {
		if !inRange(r, d.at) {
			continue
		}
		day := utcDay(d.at)
		row := dayOf(days, day, func(day time.Time) *report.CostDay { return &report.CostDay{Day: day} })
		row.Decisions++
		if d.CostComplete {
			row.Complete++
			row.CostCents += *d.EstimatedCostCents
			row.Units += d.Units
			costs[day] = append(costs[day], float64(*d.EstimatedCostCents))
		}
	}
	for day, row := range days {
		row.P50 = report.Percentile(costs[day], report.CostP50())
		row.P90 = report.Percentile(costs[day], report.CostP90())
	}
	return sortedDays(days, func(d report.CostDay) time.Time { return d.Day }), nil
}

// LastEventAt implements report.Reader.
func (m *MemoryStore) LastEventAt(context.Context) (*time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ats := make([]time.Time, 0, len(m.processed))
	for _, at := range m.processed {
		ats = append(ats, at)
	}
	if len(ats) == 0 {
		return nil, nil
	}
	sort.Slice(ats, func(i, j int) bool { return ats[i].After(ats[j]) })
	return &ats[0], nil
}

// Ping is the twin's readiness check: always ready.
func (m *MemoryStore) Ping(context.Context) error { return nil }
