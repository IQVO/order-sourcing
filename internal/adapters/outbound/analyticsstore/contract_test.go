package analyticsstore_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// store is what both twins implement: the writer and the reader port.
type store interface {
	report.Projection
	report.Reader
}

// runContract is THE contract of the analytical store (ADR 0013). It runs
// against MemoryStore (memory_test.go) and against real Postgres
// (postgres_integration_test.go); newStore returns an EMPTY store.
func runContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Run("an empty store answers empty, non-nil reports", func(t *testing.T) { contractEmpty(t, newStore(t)) })
	t.Run("a replayed id is a no-op", func(t *testing.T) { contractReplay(t, newStore(t)) })
	t.Run("a republished decision keeps one row", func(t *testing.T) { contractRepublish(t, newStore(t)) })
	t.Run("sourcing mix per day", func(t *testing.T) { contractMix(t, newStore(t)) })
	t.Run("order splits per day", func(t *testing.T) { contractSplits(t, newStore(t)) })
	t.Run("cost to serve per day over the last decisions", func(t *testing.T) { contractCost(t, newStore(t)) })
	t.Run("the freshness basis is the newest CloudEvents time", func(t *testing.T) { contractLastEvent(t, newStore(t)) })
	t.Run("an invalid fact is rejected and leaves nothing", func(t *testing.T) { contractRejected(t, newStore(t)) })
}

var (
	ctx       = context.Background()
	nov3      = time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	nov4      = nov3.Add(24 * time.Hour)
	fullRange = report.Range{From: nov3, To: nov3.Add(48 * time.Hour)}
)

func boolp(b bool) *bool     { return &b }
func cents(v int64) *int64   { return &v }
func f(v float64) *float64   { return &v }
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func nearp(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return near(*a, *b)
}

// seeder applies facts with fresh ids; At is one minute after the instant.
type seeder struct {
	t   *testing.T
	s   store
	seq int
}

func (sd *seeder) apply(e report.Event) {
	sd.t.Helper()
	sd.seq++
	e.EventID = fmt.Sprintf("evt-%d", sd.seq)
	e.At = e.OccurredAt.Add(time.Minute)
	if applied, err := sd.s.Apply(ctx, e); err != nil || !applied {
		sd.t.Fatalf("apply %s %s: applied=%v err=%v", e.Kind, e.EventID, applied, err)
	}
}

func (sd *seeder) queued(assignment, order string, group int, at time.Time) {
	sd.apply(report.Event{Kind: report.KindFulfillmentQueued, AssignmentID: assignment, OrderID: order, GroupNo: group, OccurredAt: at})
}

func (sd *seeder) unsourceable(assignment string, at time.Time) {
	sd.apply(report.Event{Kind: report.KindFulfillmentUnsourceable, AssignmentID: assignment, OrderID: "ord-" + assignment, OccurredAt: at})
}

// decision is one sourcing decision of the fixture; cost nil = incomplete.
type decision struct {
	assignment string
	releaseNo  int
	at         time.Time
	inRegion   *bool
	legs       int
	cost       *int64
	units      int
}

func (sd *seeder) decided(d decision) {
	region := ""
	if d.inRegion != nil {
		region = "r1"
	}
	sd.apply(report.Event{Kind: report.KindFulfillmentDecided, AssignmentID: d.assignment, OrderID: "ord-" + d.assignment, GroupNo: 1, OccurredAt: d.at,
		Decision: report.Decision{ReleaseNo: d.releaseNo, NodeID: "n-" + d.assignment, NodeRegionID: region, DestinationRegionID: "r1",
			InRegion: d.inRegion, Legs: d.legs, EstimatedCostCents: d.cost, CostComplete: d.cost != nil, Units: d.units, CandidateCount: 2}})
}

// decisionsFixture is the decision fixture both the mix and the cost contracts
// read (see the ADR's KPI definitions). Range [nov3, nov5).
func decisionsFixture() []decision {
	return []decision{
		{"A", 1, nov3.Add(10 * time.Hour), boolp(true), 1, cents(300), 2},
		{"B", 1, nov3.Add(11 * time.Hour), boolp(false), 2, nil, 1},
		{"B", 2, nov4.Add(9 * time.Hour), nil, 0, cents(500), 5},           // B's last decision: day 4
		{"C", 1, nov4.Add(-time.Second), boolp(true), 0, cents(100), 1},    // the last second of day 3
		{"F", 1, nov4.Add(12 * time.Hour), boolp(true), 3, cents(900), 3},  //
		{"G", 1, nov4.Add(15 * time.Hour), boolp(false), 1, nil, 4},        // incomplete last decision
		{"D", 1, fullRange.To, boolp(true), 1, cents(50), 1},               // exactly at To: excluded
		{"E", 1, nov3.Add(-time.Second), boolp(true), 1, cents(70), 1},     // before From: excluded
		{"H", 1, nov3.Add(-time.Hour), boolp(true), 1, cents(10), 1},       // H's first release is outside ...
		{"H", 2, nov3.Add(20 * time.Hour), boolp(false), 0, cents(20), 2},  // ... its re-release counts on day 3
		{"I", 2, fullRange.To.Add(time.Hour), boolp(true), 0, cents(5), 1}, // I's LAST decision is outside ...
		{"I", 1, nov4.Add(1 * time.Hour), boolp(true), 0, cents(1000), 9},  // ... so its cost never counts
	}
}

func seedDecisions(t *testing.T, s store) {
	t.Helper()
	sd := &seeder{t: t, s: s}
	for _, d := range decisionsFixture() {
		sd.decided(d)
	}
	sd.unsourceable("U1", nov3.Add(12*time.Hour))
	sd.unsourceable("U2", nov4.Add(13*time.Hour))
	sd.unsourceable("U3", nov4.Add(14*time.Hour))
	sd.unsourceable("U4", fullRange.To)
}

func contractEmpty(t *testing.T, s store) {
	mix, err1 := s.SourcingMix(ctx, fullRange)
	splits, err2 := s.OrderSplits(ctx, fullRange)
	cost, err3 := s.CostToServe(ctx, fullRange)
	if err := errors.Join(err1, err2, err3); err != nil || mix == nil || splits == nil || cost == nil || len(mix)+len(splits)+len(cost) != 0 {
		t.Fatalf("reports = %#v %#v %#v, %v; want empty non-nil", mix, splits, cost, err)
	}
	if last, err := s.LastEventAt(ctx); err != nil || last != nil {
		t.Fatalf("last = %v, %v; want nil", last, err)
	}
}

func contractReplay(t *testing.T, s store) {
	e := report.Event{Kind: report.KindFulfillmentUnsourceable, EventID: "evt-1", AssignmentID: "A", OrderID: "o", At: nov3.Add(time.Hour), OccurredAt: nov3}
	if applied, err := s.Apply(ctx, e); err != nil || !applied {
		t.Fatalf("first apply: %v %v", applied, err)
	}
	if applied, err := s.Apply(ctx, e); err != nil || applied {
		t.Fatalf("replay: applied=%v err=%v; want a no-op", applied, err)
	}
	mix, err := s.SourcingMix(ctx, fullRange)
	if err != nil || len(mix) != 1 || mix[0].Unsourceable != 1 {
		t.Fatalf("mix after a replay = %+v, %v; want one declaration", mix, err)
	}
}

func contractRepublish(t *testing.T, s store) {
	sd := &seeder{t: t, s: s}
	d := decision{"A", 1, nov3.Add(time.Hour), boolp(true), 1, cents(300), 2}
	sd.decided(d)
	sd.decided(d) // the relay republished the same decision under a NEW id
	mix, err := s.SourcingMix(ctx, fullRange)
	if err != nil || len(mix) != 1 || mix[0].Releases != 1 {
		t.Fatalf("mix = %+v, %v; want one release", mix, err)
	}
}

func contractMix(t *testing.T, s store) {
	seedDecisions(t, s)
	got, err := s.SourcingMix(ctx, fullRange)
	if err != nil {
		t.Fatal(err)
	}
	want := []report.MixDay{
		{Day: nov3, Releases: 4, InRegion: 2, OutOfRegion: 2, Legs: 3, Reassignments: 1, Unsourceable: 1},
		{Day: nov4, Releases: 4, InRegion: 2, OutOfRegion: 1, UnknownRegion: 1, Legs: 4, Reassignments: 1, Unsourceable: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("mix = %+v\nwant %+v", got, want)
	}
	for i := range want {
		if !got[i].Day.Equal(want[i].Day) || got[i].Day.Location() != time.UTC {
			t.Errorf("row %d day = %v, want %v UTC", i, got[i].Day, want[i].Day)
		}
		got[i].Day = want[i].Day
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	one, err := s.SourcingMix(ctx, report.Range{From: nov4, To: fullRange.To})
	if err != nil || len(one) != 1 || !one[0].Day.Equal(nov4) {
		t.Fatalf("a one-day range = %+v, %v", one, err)
	}
}

func contractSplits(t *testing.T, s store) {
	sd := &seeder{t: t, s: s}
	sd.queued("A1", "O1", 1, nov3.Add(8*time.Hour))
	sd.queued("A2", "O1", 2, nov3.Add(8*time.Hour))
	sd.queued("A1", "O1", 1, nov3.Add(9*time.Hour)) // a republish of A1 under a new id keeps the first row
	sd.queued("B1", "O2", 1, nov3.Add(9*time.Hour))
	sd.queued("C1", "O3", 1, nov4.Add(7*time.Hour))
	sd.queued("C2", "O3", 2, nov4.Add(8*time.Hour))
	sd.queued("C3", "O3", 3, fullRange.To.Add(time.Hour)) // counted on O3's first day
	sd.queued("D1", "O4", 1, nov3.Add(-time.Second))
	sd.queued("D2", "O4", 2, nov3.Add(time.Hour)) // O4 started before the range: never counted
	sd.queued("E1", "O5", 1, fullRange.To)
	got, err := s.OrderSplits(ctx, fullRange)
	if err != nil {
		t.Fatal(err)
	}
	want := []report.SplitDay{{Day: nov3, Orders: 2, Groups: 3, SplitOrders: 1}, {Day: nov4, Orders: 1, Groups: 3, SplitOrders: 1}}
	if len(got) != len(want) {
		t.Fatalf("splits = %+v\nwant %+v", got, want)
	}
	for i := range want {
		if !got[i].Day.Equal(want[i].Day) || got[i].Orders != want[i].Orders || got[i].Groups != want[i].Groups || got[i].SplitOrders != want[i].SplitOrders {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func contractCost(t *testing.T, s store) {
	seedDecisions(t, s)
	got, err := s.CostToServe(ctx, fullRange)
	if err != nil {
		t.Fatal(err)
	}
	want := []report.CostDay{
		{Day: nov3, Decisions: 3, Complete: 3, CostCents: 420, Units: 5, P50: f(100), P90: f(260)},
		{Day: nov4, Decisions: 3, Complete: 2, CostCents: 1400, Units: 8, P50: f(700), P90: f(860)},
	}
	if len(got) != len(want) {
		t.Fatalf("cost = %+v\nwant %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if !g.Day.Equal(w.Day) || g.Decisions != w.Decisions || g.Complete != w.Complete || g.CostCents != w.CostCents || g.Units != w.Units ||
			!nearp(g.P50, w.P50) || !nearp(g.P90, w.P90) {
			t.Errorf("row %d = %+v (p50 %v p90 %v), want %+v", i, g, deref(g.P50), deref(g.P90), w)
		}
	}
	contractCostOfIncompleteDecisions(t, s)
}

// contractCostOfIncompleteDecisions: a day whose last decisions are all
// incomplete has no percentiles (percentile_cont over no rows is NULL).
func contractCostOfIncompleteDecisions(t *testing.T, s store) {
	sd := &seeder{t: t, s: s, seq: 100}
	sd.decided(decision{"Z", 1, nov3.Add(2 * time.Hour), nil, 0, nil, 1})
	only, err := s.CostToServe(ctx, report.Range{From: nov3, To: nov3.Add(3 * time.Hour)})
	if err != nil || len(only) != 1 || only[0].Complete != 0 || only[0].P50 != nil || only[0].P90 != nil {
		t.Fatalf("a day of incomplete decisions = %+v, %v; want nil percentiles", only, err)
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func contractLastEvent(t *testing.T, s store) {
	sd := &seeder{t: t, s: s}
	sd.unsourceable("A", nov3.Add(5*time.Hour))
	sd.queued("B", "O", 1, nov3.Add(2*time.Hour))
	last, err := s.LastEventAt(ctx)
	newest := nov3.Add(5*time.Hour + time.Minute)
	if err != nil || last == nil || !last.Equal(newest) || last.Location() != time.UTC {
		t.Fatalf("last = %v, %v; want %v UTC (the newest, not the last applied)", last, err, newest)
	}
}

func contractRejected(t *testing.T, s store) {
	bad := []report.Event{
		{Kind: report.KindFulfillmentUnsourceable, EventID: "evt-bad-1", At: nov3, OccurredAt: nov3, OrderID: "o"},
		{Kind: "FulfillmentLocked", EventID: "evt-bad-2", AssignmentID: "A", OrderID: "o", At: nov3, OccurredAt: nov3},
		{Kind: report.KindFulfillmentDecided, EventID: "evt-bad-3", AssignmentID: "A", OrderID: "o", At: nov3, OccurredAt: nov3,
			Decision: report.Decision{ReleaseNo: 1, NodeID: "n", Units: 1, CandidateCount: 1, CostComplete: true}},
	}
	for _, e := range bad {
		if applied, err := s.Apply(ctx, e); applied || !errors.Is(err, report.ErrRejected) {
			t.Errorf("%s: applied=%v err=%v; want ErrRejected", e.EventID, applied, err)
		}
	}
	if last, err := s.LastEventAt(ctx); err != nil || last != nil {
		t.Fatalf("a rejected fact must leave no mark, last = %v, %v", last, err)
	}
}
