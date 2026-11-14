package assignment

import (
	"reflect"
	"testing"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// The analytics-only fact of every release (ADR 0013).

func decisionOf(t *testing.T, events []shared.Event) FulfillmentSourcingDecided {
	t.Helper()
	d, ok := events[len(events)-1].(FulfillmentSourcingDecided)
	if !ok {
		t.Fatalf("the last event of a release is %T, want FulfillmentSourcingDecided", events[len(events)-1])
	}
	return d
}

func TestReleaseRaisesTheSourcingDecision(t *testing.T) {
	a := mustQueue(t)
	events, err := a.Release(cand("node-a"), releaseLines(), facts(), at(2))
	wantNoErr(t, err)
	d := decisionOf(t, events)
	want := FulfillmentSourcingDecided{Base: d.Base, OrderID: "ord-1", GroupNo: 1, ReleaseNo: 1, NodeID: "node-a", NodeRegionID: "r1",
		DestinationRegionID: "r1", Legs: 1, Units: 3, CandidateCount: 2, DecidedAt: at(2)}
	if !reflect.DeepEqual(d, want) || d.AssignmentID != "asg-1" || d.CostComplete() {
		t.Fatalf("decision = %+v\nwant %+v", d, want)
	}
	rel := events[len(events)-2].(FulfillmentReleasedToNode)
	if d.AggregateVersion() != rel.AggregateVersion() || d.SubjectID() != rel.SubjectID() {
		t.Fatalf("the decision belongs to the release: %+v vs %+v", d.Base, rel.Base)
	}
	if d.Stream() != shared.StreamAnalytics || d.EventName() != "FulfillmentSourcingDecided" || d.Entity() != "assignment" {
		t.Fatalf("decision is %s on %s", d.EventName(), d.Stream())
	}
}

func TestTheSourcingDecisionCarriesACompleteEstimate(t *testing.T) {
	c := cand("node-a")
	c.Legs = nil
	c.EstimatedCostCents, c.CostComplete = 450, true
	a := mustQueue(t)
	events, err := a.Release(c, releaseLines(), facts(), at(2))
	wantNoErr(t, err)
	d := decisionOf(t, events)
	if d.EstimatedCostCents == nil || *d.EstimatedCostCents != 450 || !d.CostComplete() || d.Legs != 0 {
		t.Fatalf("decision = %+v", d)
	}
}

// TestTheSourcingDecisionCarriesTheCarbonEstimate: the grams follow the same
// rule as the cents — carried when complete, nil when a component had no
// declared emission, independently of the money estimate (ADR 0017).
func TestTheSourcingDecisionCarriesTheCarbonEstimate(t *testing.T) {
	c := cand("node-a")
	c.EstimatedCo2eGrams, c.EmissionComplete = 760, true
	a := mustQueue(t)
	events, err := a.Release(c, releaseLines(), facts(), at(2))
	wantNoErr(t, err)
	d := decisionOf(t, events)
	if d.EstimatedCo2eGrams == nil || *d.EstimatedCo2eGrams != 760 || !d.EmissionComplete() || d.CostComplete() {
		t.Fatalf("decision = %+v", d)
	}
	rel := events[len(events)-2].(FulfillmentReleasedToNode)
	if rel.EstimatedCo2eGrams == nil || *rel.EstimatedCo2eGrams != 760 || !rel.EmissionComplete {
		t.Fatalf("release = %+v", rel)
	}
	// The incomplete twin: no declared emission anywhere, the field is nil.
	incomplete := cand("node-b")
	incomplete.EstimatedCostCents, incomplete.CostComplete = 450, true
	a2 := mustQueue(t)
	events2, err := a2.Release(incomplete, releaseLines(), facts(), at(2))
	wantNoErr(t, err)
	d2 := decisionOf(t, events2)
	if d2.EstimatedCo2eGrams != nil || d2.EmissionComplete() || !d2.CostComplete() {
		t.Fatalf("decision = %+v", d2)
	}
	rel2 := events2[len(events2)-2].(FulfillmentReleasedToNode)
	if rel2.EstimatedCo2eGrams != nil || rel2.EmissionComplete {
		t.Fatalf("release = %+v", rel2)
	}
}

func TestSourcingDecisionInRegion(t *testing.T) {
	cases := []struct {
		node, dest      string
		inRegion, known bool
	}{
		{"r1", "r1", true, true},
		{"r1", "r2", false, true},
		{"", "r1", false, false},
		{"r1", "", false, false},
		{"", "", false, false},
	}
	for _, c := range cases {
		in, known := FulfillmentSourcingDecided{NodeRegionID: c.node, DestinationRegionID: c.dest}.InRegion()
		if in != c.inRegion || known != c.known {
			t.Errorf("InRegion(%q, %q) = %v, %v; want %v, %v", c.node, c.dest, in, known, c.inRegion, c.known)
		}
	}
}

func TestReleaseRefusesADecisionWithoutACandidateCount(t *testing.T) {
	for _, n := range []int{0, -1, MaxInt32 + 1} {
		a := mustQueue(t)
		_, err := a.Release(cand("node-a"), releaseLines(), SourcingFacts{CandidateCount: n}, at(2))
		wantErr(t, err, shared.ErrValidation)
		wantState(t, a, StateQueued, 1)
	}
	for _, n := range []int{1, MaxInt32} {
		a := mustQueue(t)
		events, err := a.Release(cand("node-a"), releaseLines(), SourcingFacts{CandidateCount: n}, at(2))
		wantNoErr(t, err)
		if d := decisionOf(t, events); d.CandidateCount != n || d.NodeRegionID != "" || d.DestinationRegionID != "" {
			t.Fatalf("decision = %+v", d)
		}
	}
}

func TestUnitsOfSumsTheQuantities(t *testing.T) {
	if got := unitsOf(nil); got != 0 {
		t.Fatalf("no lines = %d units", got)
	}
	if got := unitsOf([]Line{{Quantity: 4}, {Quantity: 1}, {Quantity: 2}}); got != 7 {
		t.Fatalf("units = %d, want 7", got)
	}
}
