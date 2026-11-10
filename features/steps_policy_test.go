package features_test

import (
	"errors"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

func registerPolicySteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the pickup lane of "([^"]*)" costs (\d+) cents$`, w.priceLane)
	sc.Step(`^the operator publishes a sourcing policy with a cross-region surcharge of (-?\d+) cents and a budget of (-?\d+) percent$`, w.publishPolicy)
	sc.Step(`^the current sourcing policy is version (\d+)$`, w.wantPolicyVersion)
	sc.Step(`^the assignment of "([^"]*)" group (\d+) was queued under policy version (\d+)$`, w.wantQueuedUnder)
	sc.Step(`^the last release event carries no policy version$`, func() error { return w.wantRelease(0, nil) })
	sc.Step(`^the last release event carries policy version (\d+) and no effective cost$`, func(version int) error { return w.wantRelease(version, nil) })
	sc.Step(`^the last release event carries policy version (\d+) and effective cost (\d+) cents$`, func(version, cents int) error {
		effective := int64(cents)
		return w.wantRelease(version, &effective)
	})
}

func (w *world) publishPolicy(surcharge, percent int) error {
	w.run(func() error {
		_, err := usecases.NewSourcingPolicies(w.deps).Publish(w.ctx(), policy.Input{CrossRegionSurchargeCents: surcharge, MaxCostDeviationPercent: percent})
		return err
	})
	return nil
}

func (w *world) wantPolicyVersion(version int) error {
	got, err := usecases.NewSourcingPolicies(w.deps).Current(w.ctx())
	if err != nil {
		return fmt.Errorf("read the current policy: %w", err)
	}
	if got.Version != version {
		return fmt.Errorf("current policy = %+v, want version %d", got, version)
	}
	return nil
}

func (w *world) wantQueuedUnder(order string, group, version int) error {
	v, err := w.assignmentOf(order, group)
	if err != nil {
		return err
	}
	if v.Candidate == nil || v.Candidate.PolicyVersion != version {
		return fmt.Errorf("candidate = %+v, want policy version %d", v.Candidate, version)
	}
	return nil
}

// wantRelease checks the policy fields of the last release event: effective nil means the field is absent.
func (w *world) wantRelease(version int, effective *int64) error {
	rel, err := w.lastRelease()
	if err != nil {
		return err
	}
	if rel.PolicyVersion != version || (rel.EffectiveCostCents == nil) != (effective == nil) || (effective != nil && *rel.EffectiveCostCents != *effective) {
		return fmt.Errorf("release = %+v, want policy version %d and effective cost %v", rel, version, effective)
	}
	return nil
}

// priceLane gives the pickup of one node its own lane with the declared cost; the shared
// onward leg and the wave cost 0, every component declared, so a node's estimate is its pickup lane.
func (w *world) priceLane(node string, cents int) error {
	rm, ctx := w.store.ReadModels(), w.ctx()
	lane := "lane-" + node
	d := departure("pk-"+suffix(node), node, "sc-1", 4, 5, 8)
	d.LaneID = lane
	steps := []error{rm.PutDeparture(ctx, d), putLaneCost(w, lane, int64(cents)), putLaneCost(w, "lane-1", 0)}
	wave, err := rm.GetWave(ctx, "w1")
	steps = append(steps, err)
	if err == nil {
		wave.ApplyCostDeclared(0, hours(1))
		steps = append(steps, rm.PutWave(ctx, wave))
	}
	for _, e := range steps {
		if e != nil {
			return fmt.Errorf("price the lane of %s: %w", node, e)
		}
	}
	return nil
}

func putLaneCost(w *world, id string, cents int64) error {
	l := network.Lane{ID: id}
	l.ApplyCostDeclared(cents, hours(1))
	return w.store.ReadModels().PutLane(w.ctx(), l)
}

// suffix is the last letter of a node id ("fc-a" -> "a"): the seeded pickups are pk-a and pk-b.
func suffix(node string) string { return node[len(node)-1:] }

// lastRelease is the FulfillmentReleasedToNode last published in the scenario.
func (w *world) lastRelease() (assignment.FulfillmentReleasedToNode, error) {
	events := w.store.Outbox().Events()
	for i := len(events) - 1; i >= 0; i-- {
		if r, ok := events[i].(assignment.FulfillmentReleasedToNode); ok {
			return r, nil
		}
	}
	return assignment.FulfillmentReleasedToNode{}, errors.New("no FulfillmentReleasedToNode was published")
}
