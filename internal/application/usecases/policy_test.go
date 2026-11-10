package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// recordedPolicyMetrics is a ports.PolicyMetrics that remembers what it was told.
type recordedPolicyMetrics struct {
	versions []int
	budget   []string
}

func (r *recordedPolicyMetrics) PolicyVersionRead(_ context.Context, v int) {
	r.versions = append(r.versions, v)
}
func (r *recordedPolicyMetrics) BudgetPreferred(_ context.Context, kind string) {
	r.budget = append(r.budget, kind)
}

var _ ports.PolicyMetrics = (*recordedPolicyMetrics)(nil)

// withPolicies wires the policy repository and a metrics recorder.
func (e *env) withPolicies() *recordedPolicyMetrics {
	m := &recordedPolicyMetrics{}
	e.deps.Policies, e.deps.PolicyMetrics = e.store.Policies(), m
	return m
}

func (e *env) publish(surcharge, percent int) {
	e.t.Helper()
	_, err := usecases.NewSourcingPolicies(e.deps).Publish(context.Background(), policy.Input{CrossRegionSurchargeCents: surcharge, MaxCostDeviationPercent: percent})
	e.must(err)
}

// price declares the pickup lane costs of fc-a (in region r1) and fc-b (cross region r2); the shared leg and the wave cost 0.
func (e *env) price(inRegion, crossRegion int64) {
	rm, ctx := e.store.ReadModels(), context.Background()
	for _, d := range []network.Departure{
		{ID: "pk-a", LaneID: "lane-a", Kind: "LINEHAUL", Origin: "fc-a", Destination: "sc-1", CutoffAt: h(4), DepartsAt: h(5), ArrivesAt: h(8), Capacity: 100, Remaining: 100, RemainingKnown: true, Scheduled: true},
		{ID: "pk-b", LaneID: "lane-b", Kind: "LINEHAUL", Origin: "fc-b", Destination: "sc-1", CutoffAt: h(4), DepartsAt: h(5), ArrivesAt: h(8), Capacity: 100, Remaining: 100, RemainingKnown: true, Scheduled: true},
	} {
		e.must(rm.PutDeparture(ctx, d))
	}
	for id, cents := range map[string]int64{"lane-a": inRegion, "lane-b": crossRegion, "lane-1": 0} {
		l := network.Lane{ID: id}
		l.ApplyCostDeclared(cents, h(1))
		e.must(rm.PutLane(ctx, l))
	}
	w, err := rm.GetWave(ctx, "w1")
	e.must(err)
	w.ApplyCostDeclared(0, h(1))
	e.must(rm.PutWave(ctx, w))
}

func (e *env) lastRelease() assignment.FulfillmentReleasedToNode {
	e.t.Helper()
	events := e.store.Outbox().Events()
	for i := len(events) - 1; i >= 0; i-- {
		if r, ok := events[i].(assignment.FulfillmentReleasedToNode); ok {
			return r
		}
	}
	e.t.Fatal("no FulfillmentReleasedToNode was published")
	return assignment.FulfillmentReleasedToNode{}
}

func TestCurrentPolicyIsTheDefaultUntilOnePublishes(t *testing.T) {
	e := newEnv(t)
	e.withPolicies()
	got, err := usecases.NewSourcingPolicies(e.deps).Current(context.Background())
	if err != nil || got.Version != 0 || got.CrossRegionSurchargeCents != 0 || got.MaxCostDeviationPercent != 0 || !got.PublishedAt.IsZero() {
		t.Fatalf("default = %+v %v", got, err)
	}
	// No repository wired at all behaves the same.
	got, err = usecases.NewSourcingPolicies(newEnv(t).deps).Current(context.Background())
	if err != nil || got.Version != 0 {
		t.Fatalf("unwired = %+v %v", got, err)
	}
}

func TestPublishStoresTheNextVersionAndRaisesTheAnalyticsEvent(t *testing.T) {
	e := newEnv(t)
	m := e.withPolicies()
	uc := usecases.NewSourcingPolicies(e.deps)
	first, err := uc.Publish(context.Background(), policy.Input{CrossRegionSurchargeCents: 250, MaxCostDeviationPercent: 20})
	e.must(err)
	second, err := uc.Publish(context.Background(), policy.Input{MaxCostDeviationPercent: 50})
	e.must(err)
	if first.Version != 1 || second.Version != 2 || second.CrossRegionSurchargeCents != 0 || second.MaxCostDeviationPercent != 50 || !second.PublishedAt.Equal(t0) {
		t.Fatalf("versions = %+v %+v", first, second)
	}
	cur, err := uc.Current(context.Background())
	if err != nil || cur.Version != 2 {
		t.Fatalf("current = %+v %v", cur, err)
	}
	wantPublishedEvents(t, e)
	if len(m.versions) == 0 || m.versions[len(m.versions)-1] != 2 {
		t.Fatalf("the gauge follows the publish: %v", m.versions)
	}
}

func wantPublishedEvents(t *testing.T, e *env) {
	t.Helper()
	events := e.store.Outbox().Events()
	if len(events) != 2 {
		t.Fatalf("events = %v", e.eventNames())
	}
	ev, ok := events[0].(policy.SourcingPolicyPublished)
	if !ok || ev.Number != 1 || ev.CrossRegionSurchargeCents != 250 || ev.MaxCostDeviationPercent != 20 || ev.Stream() != shared.StreamAnalytics || ev.SubjectID() != "policy" {
		t.Fatalf("event = %+v", events[0])
	}
}

func TestPublishRefusesAnInvalidPolicyAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.withPolicies()
	before := e.store.Counts()
	for _, in := range []policy.Input{{CrossRegionSurchargeCents: 100001}, {MaxCostDeviationPercent: 201}, {CrossRegionSurchargeCents: -1}} {
		if _, err := usecases.NewSourcingPolicies(e.deps).Publish(context.Background(), in); !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("publish %+v = %v", in, err)
		}
	}
	e.wantUnchanged(before)
	if _, err := e.store.Policies().Current(context.Background()); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("a refused policy was stored: %v", err)
	}
}

func TestPublishWithoutARepositoryIsAnError(t *testing.T) {
	if _, err := usecases.NewSourcingPolicies(newEnv(t).deps).Publish(context.Background(), policy.Input{}); err == nil {
		t.Fatal("publishing needs the repository")
	}
}

type conflictingPolicies struct {
	ports.PolicyRepository
	conflicts int
}

func (c *conflictingPolicies) Save(ctx context.Context, p *policy.Policy) error {
	if c.conflicts > 0 {
		c.conflicts--
		return ports.ErrConcurrentUpdate
	}
	return c.PolicyRepository.Save(ctx, p)
}

func TestPublishRetriesOnceOnAConcurrentPublish(t *testing.T) {
	e := newEnv(t)
	e.deps.Policies = &conflictingPolicies{PolicyRepository: e.store.Policies(), conflicts: 1}
	if got, err := usecases.NewSourcingPolicies(e.deps).Publish(context.Background(), policy.Input{}); err != nil || got.Version != 1 {
		t.Fatalf("after one conflict = %+v %v", got, err)
	}
	e.deps.Policies = &conflictingPolicies{PolicyRepository: e.store.Policies(), conflicts: 2}
	if _, err := usecases.NewSourcingPolicies(e.deps).Publish(context.Background(), policy.Input{}); !errors.Is(err, ports.ErrConcurrentUpdate) {
		t.Fatalf("after two conflicts = %v", err)
	}
}

// The scenario of the contract: a cheaper cross-region lane loses to an
// in-region one inside a 20 percent budget and wins outside it.
func TestACheaperCrossRegionLaneLosesInsideTheBudgetAndWinsOutsideIt(t *testing.T) {
	cases := []struct {
		name      string
		inRegion  int64
		want      string
		preferred int
	}{
		{"inside the budget (exactly 120 percent)", 1200, "fc-a", 1},
		{"one cent outside the budget", 1201, "fc-b", 0},
	}
	for _, c := range cases {
		e := newEnv(t)
		m := e.withPolicies()
		e.price(c.inRegion, 1000)
		e.publish(0, 20)
		id := e.queue()
		if cand, _ := e.get(id).Candidate(); cand.NodeID != c.want || cand.PolicyVersion != 1 {
			t.Fatalf("%s: queued on %+v", c.name, cand)
		}
		e.release(id)
		rel := e.lastRelease()
		if rel.NodeID != c.want || rel.PolicyVersion != 1 || rel.EffectiveCostCents != nil {
			t.Fatalf("%s: release = %+v", c.name, rel)
		}
		if len(m.budget) != c.preferred || (c.preferred == 1 && m.budget[0] != "region") {
			t.Fatalf("%s: budget counter = %v", c.name, m.budget)
		}
	}
}

func TestWithoutAPublishedPolicyTheCheaperCrossRegionLaneAlwaysWins(t *testing.T) {
	e := newEnv(t)
	m := e.withPolicies()
	e.price(1100, 1000)
	e.release(e.queue())
	rel := e.lastRelease()
	if rel.NodeID != "fc-b" || rel.PolicyVersion != 0 || rel.EffectiveCostCents != nil || len(m.budget) != 0 {
		t.Fatalf("release = %+v budget %v", rel, m.budget)
	}
	if len(m.versions) == 0 || m.versions[0] != 0 {
		t.Fatalf("the default policy version is read as 0: %v", m.versions)
	}
}

func TestTheSurchargeShowsOnTheReleaseAndTheDecision(t *testing.T) {
	e := newEnv(t)
	e.withPolicies()
	e.price(1000, 900)
	e.publish(300, 0) // 900 + 300 > 1000: the in-region lane is cheaper in effective cents
	e.release(e.queue())
	rel := e.lastRelease()
	if rel.NodeID != "fc-a" || rel.PolicyVersion != 1 || rel.EffectiveCostCents != nil {
		t.Fatalf("an in-region release carries no surcharge: %+v", rel)
	}
	// Remove fc-a: the cross-region lane is released with its surcharge on the wire.
	e2 := newEnv(t)
	e2.withPolicies()
	e2.price(1000, 900)
	e2.publish(300, 0)
	id := e2.queue()
	e2.must(e2.store.ReadModels().PutListing(context.Background(), listingZero("fc-a")))
	e2.release(id)
	rel = e2.lastRelease()
	if rel.NodeID != "fc-b" || rel.EstimatedCostCents == nil || *rel.EstimatedCostCents != 900 || rel.EffectiveCostCents == nil || *rel.EffectiveCostCents != 1200 || rel.PolicyVersion != 1 {
		t.Fatalf("release = %+v", rel)
	}
	d := e2.lastDecision()
	if d.PolicyVersion != 1 || d.EffectiveCostCents == nil || *d.EffectiveCostCents != 1200 {
		t.Fatalf("decision = %+v", d)
	}
}

func TestAPolicyReadFailureFailsTheSearchInsteadOfSilentlyUsingTheDefault(t *testing.T) {
	e := newEnv(t)
	e.deps.Policies = failingPolicies{}
	if _, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), placed("", group(1, "prm-1", assignment.TierNextDay))); err == nil {
		t.Fatal("a failing policy read must fail the queueing")
	}
}

type failingPolicies struct{ ports.PolicyRepository }

func (failingPolicies) Current(context.Context) (*policy.Policy, error) { return nil, errBoom }
