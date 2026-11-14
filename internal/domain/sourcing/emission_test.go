package sourcing

import (
	"testing"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// emissionLane is a lane with only an emission declaration.
func emissionLane(id string, grams int64) network.Lane {
	var l network.Lane
	l.ID = id
	l.ApplyEmissionDeclared(grams, h(1))
	return l
}

func withWaveEmission(w World, grams int64) World {
	w.Waves[0].ApplyEmissionDeclared(grams, h(1))
	return w
}

// TestEmissionEstimateSumsDeclaredComponents: the carbon estimate covers the
// same components as the money estimate — the pickup lane, every onward leg
// and the final wave — each in declared grams of CO2e (ADR 0017).
func TestEmissionEstimateSumsDeclaredComponents(t *testing.T) {
	w := world()
	w.Lanes["lane-a"] = declaredLane("lane-a", 300)
	w.Lanes["lane-b"] = emissionLane("lane-b", 20)
	w.Lanes["lane-s"] = declaredLane("lane-s", 50)
	s := w.Lanes["lane-s"]
	s.ApplyEmissionDeclared(40, h(1))
	w.Lanes["lane-s"] = s
	a := w.Lanes["lane-a"]
	a.ApplyEmissionDeclared(100, h(1))
	w.Lanes["lane-a"] = a
	w = withWaveCost(withWaveEmission(w, 60), 0)
	var fcA *assignment.Candidate
	for _, o := range Search(w, request()) {
		if o.Candidate.NodeID == "fc-a" {
			fcA = &o.Candidate
		}
	}
	if fcA == nil {
		t.Fatal("fc-a has no candidate")
	}
	// fc-a: lane-a 100 + lane-s 40 + wave 60 = 200 gCO2e, complete.
	if !fcA.EmissionComplete || fcA.EstimatedCo2eGrams != 200 {
		t.Fatalf("fc-a grams = %d complete %v", fcA.EstimatedCo2eGrams, fcA.EmissionComplete)
	}
	// The money estimate is complete on the same path: 300 + 50 + 0.
	if !fcA.CostComplete || fcA.EstimatedCostCents != 350 {
		t.Fatalf("fc-a cents = %d complete %v", fcA.EstimatedCostCents, fcA.CostComplete)
	}
}

// TestEmissionIndependenceOfTheCostDeclaration: a cost declaration does not
// declare an emission and an emission declaration does not declare a cost;
// either estimate may be complete while the other is not (ADR 0017).
func TestEmissionIndependenceOfTheCostDeclaration(t *testing.T) {
	w := world()
	// Emissions only: fc-a's carbon estimate is complete, its money one is not.
	w.Lanes["lane-a"], w.Lanes["lane-s"] = emissionLane("lane-a", 100), emissionLane("lane-s", 40)
	w = withWaveEmission(w, 60)
	opts := Search(w, request())
	if !opts[0].Candidate.EmissionComplete || opts[0].Candidate.EstimatedCo2eGrams != 200 {
		t.Fatalf("fc-a grams = %d complete %v", opts[0].Candidate.EstimatedCo2eGrams, opts[0].Candidate.EmissionComplete)
	}
	if opts[0].Candidate.CostComplete {
		t.Fatal("no declared cost anywhere must leave the money estimate incomplete")
	}
	// Costs only: the mirror image.
	w2 := world()
	w2.Lanes["lane-a"], w2.Lanes["lane-s"] = declaredLane("lane-a", 300), declaredLane("lane-s", 50)
	w2 = withWaveCost(w2, 0)
	for _, o := range Search(w2, request()) {
		if o.Candidate.EmissionComplete {
			t.Fatalf("%s: no declared emission anywhere must leave the carbon estimate incomplete", o.Candidate.NodeID)
		}
	}
}

// TestEmissionIncompleteWhenAComponentIsUndeclared: an undeclared lane or an
// undeclared wave contributes 0 grams and marks the estimate incomplete; a
// declared 0 is complete (the ADR 0011 §2 rule, in grams).
func TestEmissionIncompleteWhenAComponentIsUndeclared(t *testing.T) {
	w := world()
	w.Lanes["lane-a"] = emissionLane("lane-a", 100)
	w = withWaveEmission(w, 60) // lane-s undeclared
	var grams int64
	var complete, found bool
	for _, o := range Search(w, request()) {
		if o.Candidate.NodeID == "fc-a" {
			grams, complete, found = o.Candidate.EstimatedCo2eGrams, o.Candidate.EmissionComplete, true
		}
	}
	if !found {
		t.Fatal("fc-a has no candidate")
	}
	if grams != 160 || complete {
		t.Fatalf("fc-a grams = %d complete %v", grams, complete)
	}
	// The undeclared wave twin.
	w2 := world()
	w2.Lanes["lane-a"], w2.Lanes["lane-s"] = emissionLane("lane-a", 100), emissionLane("lane-s", 40)
	for _, o := range Search(w2, request()) {
		if o.Candidate.NodeID == "fc-a" && (o.Candidate.EmissionComplete || o.Candidate.EstimatedCo2eGrams != 140) {
			t.Fatalf("fc-a grams = %d complete %v", o.Candidate.EstimatedCo2eGrams, o.Candidate.EmissionComplete)
		}
	}
	// A declared 0 everywhere is a complete estimate at 0 grams.
	w3 := world()
	w3.Lanes["lane-a"], w3.Lanes["lane-s"], w3.Lanes["lane-b"] = emissionLane("lane-a", 0), emissionLane("lane-s", 0), emissionLane("lane-b", 0)
	w3 = withWaveEmission(w3, 0)
	for _, o := range Search(w3, request()) {
		if !o.Candidate.EmissionComplete || o.Candidate.EstimatedCo2eGrams != 0 {
			t.Fatalf("%s grams = %d complete %v", o.Candidate.NodeID, o.Candidate.EstimatedCo2eGrams, o.Candidate.EmissionComplete)
		}
	}
}

// TestEmissionsNeverChangeTheRanking: the comparator never sees grams. The
// same world searched with and without emission declarations picks the same
// nodes in the same order with the same costs, even when the declared grams
// alone would invert the order (ADR 0017 ranking-unchanged property).
func TestEmissionsNeverChangeTheRanking(t *testing.T) {
	base := world()
	base.Lanes["lane-a"], base.Lanes["lane-b"], base.Lanes["lane-s"] = declaredLane("lane-a", 300), declaredLane("lane-b", 350), declaredLane("lane-s", 50)
	base = withWaveCost(base, 0)
	baseline := Search(base, request())

	twin := world()
	twin.Lanes["lane-a"], twin.Lanes["lane-b"], twin.Lanes["lane-s"] = declaredLane("lane-a", 300), declaredLane("lane-b", 350), declaredLane("lane-s", 50)
	// Inverted carbon: the money-cheapest path is by far the dirtiest.
	a, b, s := twin.Lanes["lane-a"], twin.Lanes["lane-b"], twin.Lanes["lane-s"]
	a.ApplyEmissionDeclared(900, h(1))
	b.ApplyEmissionDeclared(1, h(1))
	s.ApplyEmissionDeclared(0, h(1))
	twin.Lanes["lane-a"], twin.Lanes["lane-b"], twin.Lanes["lane-s"] = a, b, s
	twin = withWaveEmission(withWaveCost(twin, 0), 0)
	withEmissions := Search(twin, request())

	if len(baseline) != len(withEmissions) || len(baseline) == 0 {
		t.Fatalf("option counts differ: %d vs %d", len(baseline), len(withEmissions))
	}
	for i := range baseline {
		b, e := baseline[i].Candidate, withEmissions[i].Candidate
		if b.NodeID != e.NodeID || b.Cost != e.Cost || b.EstimatedCostCents != e.EstimatedCostCents ||
			b.CostComplete != e.CostComplete || b.PickupDepartureID != e.PickupDepartureID {
			t.Fatalf("option %d changed: %+v vs %+v", i, b, e)
		}
		if !baseline[i].ReleaseAt.Equal(withEmissions[i].ReleaseAt) {
			t.Fatalf("release point of option %d changed", i)
		}
	}
	// The emissions were seen and differ (fc-a 900 g vs fc-b 1 g); the order
	// still follows the money (fc-a first).
	if withEmissions[0].Candidate.EstimatedCo2eGrams != 900 || withEmissions[1].Candidate.EstimatedCo2eGrams != 1 {
		t.Fatalf("grams = %d and %d", withEmissions[0].Candidate.EstimatedCo2eGrams, withEmissions[1].Candidate.EstimatedCo2eGrams)
	}
	wantNodes(t, withEmissions, nodesOf(baseline)...)
}
