package sourcing

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// costedWorld prices the two lanes of world() (fc-a in region r1 = in-region,
// fc-b in region r2 = cross-region); the shared leg and the wave cost 0, every
// component declared, so the estimate of each node is its pickup lane cost.
func costedWorld(inRegionCents, crossRegionCents int64) World {
	w := world()
	w.Lanes = map[string]network.Lane{
		"lane-a": declaredLane("lane-a", inRegionCents), "lane-b": declaredLane("lane-b", crossRegionCents), "lane-s": declaredLane("lane-s", 0),
	}
	return withWaveCost(w, 0)
}

func policyRequest(p Policy) Request {
	r := request()
	r.Policy = p
	return r
}

// legacyOrder is ADR 0011's comparator exactly as it was before ADR 0014.
func legacyOrder(r Request) func(a, b Option) int {
	return func(a, b Option) int {
		return cmp.Or(
			cmp.Compare(a.Candidate.EstimatedCostCents, b.Candidate.EstimatedCostCents),
			cmp.Compare(a.Candidate.Cost, b.Candidate.Cost),
			boolRank(a.Candidate.NodeID == r.WitnessNodeID, b.Candidate.NodeID == r.WitnessNodeID),
			strings.Compare(a.Candidate.NodeID, b.Candidate.NodeID),
			strings.Compare(a.Candidate.PickupDepartureID, b.Candidate.PickupDepartureID),
		)
	}
}

// fixtures are the search worlds of the existing tests that return more than
// one option, plus a world where the legacy score and the cents disagree.
func fixtures() map[string]World {
	tie := world()
	tie.Nodes["fc-b"] = fc("fc-b", "r1")
	// The legacy score and the cents disagree: equal cents, but the in-region
	// pickup waits 405 hours for the wave (4050) while the cross-region one waits
	// 110 (1100 + the 1000 penalty), so the legacy tie-break ranks fc-b first.
	slowInRegion := costedWorld(100, 100)
	slowInRegion.Departures = []network.Departure{
		dep("pk-a", "lane-a", "fc-a", "sc-1", 4, 5, 8), dep("pk-b", "lane-b", "fc-b", "sc-1", 4, 300, 301),
		dep("lh-1", "lane-s", "sc-1", "ds-1", 400, 401, 402),
	}
	slowInRegion.Waves = []network.Wave{wave("w1", 405, 410, 420)}
	pressured := world()
	pressured.Scorecards = map[string]network.Scorecard{"fc-a": {NodeID: "fc-a", Window: network.Window30d, Grade: "D"}}
	return map[string]World{
		"plain": world(), "equal cents": costedWorld(500, 500), "cross cheaper": costedWorld(900, 100), "in-region cheaper": costedWorld(100, 900),
		"incomplete": withWaveCost(world(), 0), "same region tie": tie, "legacy disagrees": slowInRegion, "graded": pressured,
	}
}

func TestDefaultPolicyOrderingIsByteIdenticalToTheLegacyOrdering(t *testing.T) {
	for name, w := range fixtures() {
		for _, witness := range []string{"fc-a", "fc-b", ""} {
			r := request()
			r.WitnessNodeID = witness
			r.DeliverByLatest = h(500)
			got := Search(w, r)
			want := slices.Clone(got)
			slices.SortFunc(want, legacyOrder(r))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s (witness %q): default policy order = %v, legacy order = %v", name, witness, nodesOf(got), nodesOf(want))
			}
			for _, o := range got {
				if o.Candidate.SurchargeCents != 0 || o.Candidate.PolicyVersion != 0 || o.Candidate.EffectiveCostOnWire() != nil {
					t.Errorf("%s: default policy left a mark on %+v", name, o.Candidate)
				}
			}
		}
	}
}

func TestDefaultPolicyWithAVersionStillOrdersAsLegacy(t *testing.T) {
	// A published policy with every lever at 0 only stamps its version.
	for name, w := range fixtures() {
		r := policyRequest(Policy{Version: 3})
		got := Search(w, r)
		want := Search(w, request())
		if !slices.Equal(nodesOf(got), nodesOf(want)) {
			t.Errorf("%s: %v vs %v", name, nodesOf(got), nodesOf(want))
		}
		for _, o := range got {
			if o.Candidate.PolicyVersion != 3 || o.Candidate.EffectiveCostOnWire() != nil {
				t.Errorf("%s: candidate = %+v", name, o.Candidate)
			}
		}
	}
}

// budgetBoundaries: the cross-region lane is the cheapest at 1000 cents.
func TestBudgetBoundaryExactlyAtTheBudgetAndOneCentOver(t *testing.T) {
	p := Policy{Version: 1, MaxCostDeviationPercent: 20}
	cases := []struct {
		name     string
		cheapest int64
		inRegion int64
		want     string
	}{
		{"exactly at the budget", 1000, 1200, "fc-a"},
		{"one cent over the budget", 1000, 1201, "fc-b"},
		{"floor of a fractional budget (1003 * 20% = 200.6)", 1003, 1203, "fc-a"},
		{"one cent over the floored budget", 1003, 1204, "fc-b"},
		{"in-region already cheapest", 1000, 900, "fc-a"},
		{"in-region equal cents", 1000, 1000, "fc-a"},
	}
	for _, c := range cases {
		opts := Search(costedWorld(c.inRegion, c.cheapest), policyRequest(p))
		if opts[0].Candidate.NodeID != c.want {
			t.Errorf("%s: winner = %s, want %s (%v)", c.name, opts[0].Candidate.NodeID, c.want, nodesOf(opts))
		}
	}
}

func TestBudgetKeepsTheCheapestWhenItIsInRegion(t *testing.T) {
	r := Rank(costedWorld(100, 900), policyRequest(Policy{MaxCostDeviationPercent: 200}))
	wantNodes(t, r.Options, "fc-a", "fc-b")
	if r.BudgetPreferred {
		t.Fatal("the budget did not change the winner")
	}
}

func TestBudgetPreferredFlagIsSetOnlyWhenTheBudgetChangedTheWinner(t *testing.T) {
	p := Policy{MaxCostDeviationPercent: 20}
	if r := Rank(costedWorld(1200, 1000), policyRequest(p)); !r.BudgetPreferred || r.Options[0].Candidate.NodeID != "fc-a" {
		t.Fatalf("inside the budget: %+v", r)
	}
	if r := Rank(costedWorld(1201, 1000), policyRequest(p)); r.BudgetPreferred || r.Options[0].Candidate.NodeID != "fc-b" {
		t.Fatalf("outside the budget: %+v", r)
	}
	if r := Rank(costedWorld(1200, 1000), policyRequest(Policy{})); r.BudgetPreferred || r.Options[0].Candidate.NodeID != "fc-b" {
		t.Fatalf("budget off: %+v", r)
	}
	one := costedWorld(1200, 1000)
	delete(one.Nodes, "fc-b")
	if r := Rank(one, policyRequest(p)); r.BudgetPreferred || len(r.Options) != 1 {
		t.Fatalf("single option: %+v", r)
	}
	if r := Rank(World{}, policyRequest(p)); r.BudgetPreferred || r.Options != nil {
		t.Fatalf("empty world: %+v", r)
	}
}

func TestBudgetOrdersWithinBudgetInRegionFirstThenCrossThenOverBudget(t *testing.T) {
	w := costedWorld(120, 100)
	w.Nodes["fc-c"] = fc("fc-c", "r1")
	w.Listings = append(w.Listings, listing("fc-c", "cat-1", 10))
	w.Departures = append(w.Departures, dep("pk-c", "lane-c", "fc-c", "sc-1", 4, 5, 8))
	w.Lanes["lane-c"] = declaredLane("lane-c", 130) // in-region but over the 20% budget of 100
	opts := Search(w, policyRequest(Policy{MaxCostDeviationPercent: 20}))
	wantNodes(t, opts, "fc-a", "fc-b", "fc-c")
}

func TestZeroPercentIgnoresRegionEvenWithASurcharge(t *testing.T) {
	// Budget off: the surcharge moves the cents, nothing else; 1100 + 150 > 1200.
	opts := Search(costedWorld(1200, 1100), policyRequest(Policy{Version: 2, CrossRegionSurchargeCents: 150}))
	wantNodes(t, opts, "fc-a", "fc-b")
	opts = Search(costedWorld(1200, 1100), policyRequest(Policy{Version: 2, CrossRegionSurchargeCents: 50}))
	wantNodes(t, opts, "fc-b", "fc-a")
}

func TestSurchargeIsAppliedToTheCrossRegionCandidateOnly(t *testing.T) {
	opts := Search(costedWorld(1000, 900), policyRequest(Policy{Version: 4, CrossRegionSurchargeCents: 300}))
	a, b := find(t, opts, "fc-a").Candidate, find(t, opts, "fc-b").Candidate
	if a.SurchargeCents != 0 || a.EffectiveCostCents() != 1000 || a.EffectiveCostOnWire() != nil || a.PolicyVersion != 4 {
		t.Fatalf("in-region candidate = %+v", a)
	}
	if b.SurchargeCents != 300 || b.EstimatedCostCents != 900 || b.EffectiveCostCents() != 1200 || *b.EffectiveCostOnWire() != 1200 || b.PolicyVersion != 4 {
		t.Fatalf("cross-region candidate = %+v", b)
	}
	if !find(t, opts, "fc-b").CrossRegion || find(t, opts, "fc-a").CrossRegion {
		t.Fatal("CrossRegion flags")
	}
}

func TestSurchargeNeedsBothRegionsKnown(t *testing.T) {
	for name, mutate := range map[string]func(*World){
		"node without region": func(w *World) { w.Nodes["fc-b"] = fc("fc-b", "") },
		"area without region": func(w *World) { w.Area.RegionID = "" },
	} {
		w := costedWorld(1000, 900)
		mutate(&w)
		for _, o := range Search(w, policyRequest(Policy{CrossRegionSurchargeCents: 300, MaxCostDeviationPercent: 20})) {
			if o.Candidate.SurchargeCents != 0 || o.CrossRegion {
				t.Errorf("%s: %s was charged %d", name, o.Candidate.NodeID, o.Candidate.SurchargeCents)
			}
		}
	}
}

func TestSurchargeAppliesToAnIncompleteEstimateWhichStaysReported(t *testing.T) {
	w := costedWorld(1000, 900)
	delete(w.Lanes, "lane-b") // the cross-region lane has no declaration: incomplete, 0 cents
	opts := Search(w, policyRequest(Policy{CrossRegionSurchargeCents: 300}))
	b := find(t, opts, "fc-b").Candidate
	if b.CostComplete || b.EstimatedCostCents != 0 || b.SurchargeCents != 300 || *b.EffectiveCostOnWire() != 300 {
		t.Fatalf("incomplete candidate = %+v", b)
	}
	wantNodes(t, opts, "fc-b", "fc-a") // still competes by its effective sum
}
