package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// twoDays adds a second delivery wave (hours 40 to 44) to the seeded network, so
// a window starting after hour 26 has a later wave to land on.
func (e *env) twoDays() {
	e.must(e.store.ReadModels().PutWave(context.Background(), network.Wave{ID: "w2", StationID: "ds-1", InductCutoffAt: h(39), WindowStart: h(40),
		WindowEnd: h(44), Capacity: 50, Remaining: 50, RemainingKnown: true, Scheduled: true}))
}

// waveOf is the delivery wave of the assignment's candidate, or of its latest release.
func (e *env) waveOf(a *assignment.Assignment) string {
	if c, ok := a.Candidate(); ok {
		return c.DeliveryWaveID
	}
	return ""
}

// queueWindow queues one group of the given tier with a window starting at earliest (zero = unknown).
func (e *env) queueWindow(order string, tier assignment.Tier, earliest time.Time) string {
	e.t.Helper()
	g := group(1, "prm-"+order, tier)
	g.DeliverByEarliest, g.DeliverByLatest = earliest, h(60)
	cmd := placed("", g)
	cmd.OrderID, cmd.ConsolidationKey = order, shopperKey
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), cmd)
	e.must(err)
	if got[0].Candidate == nil {
		return ""
	}
	return got[0].Candidate.DeliveryWaveID
}

func TestAConsolidatedGroupLandsOnTheWaveOfItsChosenDay(t *testing.T) {
	for name, tc := range map[string]struct {
		tier     assignment.Tier
		earliest time.Time
		wave     string
	}{
		"CONSOLIDATED with the window start on day two": {assignment.TierConsolidated, h(30), "w2"},
		"CONSOLIDATED with the start at the wave end":   {assignment.TierConsolidated, h(26), "w1"},
		"CONSOLIDATED with an unknown start":            {assignment.TierConsolidated, time.Time{}, "w1"},
		"STANDARD ignores the window start":             {assignment.TierStandard, h(30), "w1"},
		"TWO_DAY ignores the window start":              {assignment.TierTwoDay, h(30), "w1"},
		"NEXT_DAY ignores the window start":             {assignment.TierNextDay, h(30), "w1"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.twoDays()
			if got := e.queueWindow("ord-1", tc.tier, tc.earliest); got != tc.wave {
				t.Fatalf("wave = %q, want %q", got, tc.wave)
			}
		})
	}
}

func TestAConsolidatedGroupWhoseDayHasNoWaveIsUnsourceable(t *testing.T) {
	e := newEnv(t)
	e.twoDays()
	if got := e.queueWindow("ord-1", assignment.TierConsolidated, h(50)); got != "" {
		t.Fatalf("no wave ends after hour 50, got %q", got)
	}
	if e.count("FulfillmentUnsourceable") != 1 {
		t.Fatalf("events = %v", e.eventNames())
	}
}

func TestReleaseOfAConsolidatedGroupKeepsItsDayAndTheVendorSeesStandard(t *testing.T) {
	e := newEnv(t)
	e.twoDays()
	e.queueWindow("ord-1", assignment.TierConsolidated, h(30))
	a, err := e.store.Assignments().FindByGroup(context.Background(), "ord-1", 1)
	e.must(err)
	e.release(a.ID())
	got := e.get(a.ID())
	if wave := e.waveOf(got); got.State() != assignment.StateReleased || wave != "w2" {
		t.Fatalf("state %s wave %q", got.State(), wave)
	}
	for _, ev := range e.store.Outbox().Events() {
		if r, ok := ev.(assignment.FulfillmentReleasedToNode); ok && (r.Tier != assignment.TierStandard || !r.PromisedDeliverByLatest.Equal(h(60))) {
			t.Fatalf("the vendor sees the fulfilment class: %+v", r)
		}
	}
	if got.Promise().Tier != assignment.TierConsolidated {
		t.Fatalf("the aggregate keeps the real tier: %s", got.Promise().Tier)
	}
}

func TestARevisionMovesAConsolidatedGroupToTheNewDay(t *testing.T) {
	e := newEnv(t)
	e.twoDays()
	e.queueWindow("ord-1", assignment.TierConsolidated, h(10))
	a, err := e.store.Assignments().FindByGroup(context.Background(), "ord-1", 1)
	e.must(err)
	if wave := e.waveOf(a); wave != "w1" {
		t.Fatalf("the first day: %q", wave)
	}
	_, err = usecases.NewApplyRevisedPromise(e.deps).Execute(context.Background(), usecases.ApplyRevisedPromiseCommand{
		Meta: usecases.Meta{EventID: "evt-rev", EventType: "x"}, PromiseID: "prm-ord-1", RevisionNo: 1, DeliverByEarliest: h(30), DeliverByLatest: h(60), WitnessNodeID: "fc-a"})
	e.must(err)
	got := e.get(a.ID())
	if wave := e.waveOf(got); wave != "w2" || !got.Promise().DeliverByEarliest.Equal(h(30)) {
		t.Fatalf("the revised window start moves the wave: %q earliest %v", wave, got.Promise().DeliverByEarliest)
	}
}

func TestMergedGroupsOfTheSameChosenDayShareItsWave(t *testing.T) {
	e := newEnv(t)
	e.twoDays()
	e.merging(4)
	var ids []assignment.ID
	for _, order := range []string{"ord-1", "ord-2"} {
		e.queueWindow(order, assignment.TierConsolidated, h(30))
		a, err := e.store.Assignments().FindByGroup(context.Background(), order, 1)
		e.must(err)
		ids = append(ids, a.ID())
	}
	e.release(ids[0])
	c := e.get(ids[0])
	if wave := e.waveOf(c); !c.Merged() || wave != "w2" {
		t.Fatalf("both groups want day two and merge there: merged=%v wave %q", c.Merged(), wave)
	}
}
