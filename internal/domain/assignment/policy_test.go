package assignment

import "testing"

// The sourcing policy stamps on the candidate, the release and the decision
// (ADR 0014).

func surcharged() *Candidate {
	c := cand("node-a")
	c.EstimatedCostCents, c.CostComplete = 900, true
	c.SurchargeCents, c.PolicyVersion = 300, 4
	return c
}

func releaseEvent(t *testing.T, c *Candidate) FulfillmentReleasedToNode {
	t.Helper()
	events, err := mustQueue(t).Release(c, releaseLines(), facts(), at(2))
	wantNoErr(t, err)
	return events[len(events)-2].(FulfillmentReleasedToNode)
}

func TestReleaseCarriesThePolicyVersionAndTheEffectiveCostWhenASurchargeApplied(t *testing.T) {
	events, err := mustQueue(t).Release(surcharged(), releaseLines(), facts(), at(2))
	wantNoErr(t, err)
	rel := events[len(events)-2].(FulfillmentReleasedToNode)
	if rel.PolicyVersion != 4 || rel.EffectiveCostCents == nil || *rel.EffectiveCostCents != 1200 || rel.EstimatedCostCents == nil || *rel.EstimatedCostCents != 900 {
		t.Fatalf("release = %+v", rel)
	}
	d := decisionOf(t, events)
	if d.PolicyVersion != 4 || d.EffectiveCostCents == nil || *d.EffectiveCostCents != 1200 {
		t.Fatalf("decision = %+v", d)
	}
}

func TestReleaseOmitsTheEffectiveCostWithoutASurchargeButKeepsTheVersion(t *testing.T) {
	c := surcharged()
	c.SurchargeCents = 0
	rel := releaseEvent(t, c)
	if rel.PolicyVersion != 4 || rel.EffectiveCostCents != nil {
		t.Fatalf("release = %+v", rel)
	}
}

func TestReleaseWithoutAPolicyCarriesNeither(t *testing.T) {
	rel := releaseEvent(t, cand("node-a"))
	if rel.PolicyVersion != 0 || rel.EffectiveCostCents != nil {
		t.Fatalf("release = %+v", rel)
	}
}

func TestAnIncompleteEstimateStillReportsTheEffectiveCost(t *testing.T) {
	c := surcharged()
	c.CostComplete, c.EstimatedCostCents = false, 0
	rel := releaseEvent(t, c)
	if rel.EstimatedCostCents != nil || rel.EffectiveCostCents == nil || *rel.EffectiveCostCents != 300 {
		t.Fatalf("release = %+v", rel)
	}
}

func TestCandidateSameSeesThePolicyFields(t *testing.T) {
	a, b := *surcharged(), *surcharged()
	if !a.Same(b) {
		t.Fatal("equal candidates")
	}
	b.SurchargeCents++
	if a.Same(b) {
		t.Fatal("a different surcharge is a different candidate")
	}
	b = *surcharged()
	b.PolicyVersion++
	if a.Same(b) {
		t.Fatal("a different policy version is a different candidate")
	}
}

func TestACandidateWithANegativeSurchargeOrVersionIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*Candidate){
		"negative surcharge": func(c *Candidate) { c.SurchargeCents = -1 },
		"negative version":   func(c *Candidate) { c.PolicyVersion = -1 },
		"version too large":  func(c *Candidate) { c.PolicyVersion = MaxInt32 + 1 },
	} {
		c := surcharged()
		mutate(c)
		if _, err := mustQueue(t).Release(c, releaseLines(), facts(), at(2)); err == nil {
			t.Errorf("%s: release accepted", name)
		}
	}
}
