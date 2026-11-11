package features_test

import (
	"fmt"

	"github.com/cucumber/godog"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// registerTierSteps registers the steps of the CONSOLIDATED delivery day (ADR 0016).
func registerTierSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the delivery station "([^"]*)" has a second wave on day two$`, func(station string) error {
		return w.store.ReadModels().PutWave(w.ctx(), network.Wave{ID: "w2", StationID: station, InductCutoffAt: hours(39), WindowStart: hours(40),
			WindowEnd: hours(44), Capacity: 50, Remaining: 50, RemainingKnown: true, Scheduled: true})
	})
	sc.Step(`^shopper "([^"]*)" places order "([^"]*)" with a (CONSOLIDATED|NEXT_DAY|TWO_DAY|STANDARD) group of (\d+) units of "([^"]*)" and a window starting in (\d+) hours$`,
		func(shopper, order, tier string, units int, item string, start int) error {
			return w.placeWindow(shopper, order, tier, units, item, start)
		})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) targets the delivery wave "([^"]*)"$`, func(order string, group int, wave string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.Candidate == nil || v.Candidate.DeliveryWaveID != wave {
				return fmt.Errorf("candidate = %+v, want wave %s", v.Candidate, wave)
			}
			return nil
		})
	})
	sc.Step(`^the last release event carries tier (\w+)$`, func(tier string) error {
		rel, err := w.lastRelease()
		if err != nil {
			return err
		}
		if string(rel.Tier) != tier {
			return fmt.Errorf("release tier = %s, want %s", rel.Tier, tier)
		}
		return nil
	})
	sc.Step(`^promise "([^"]*)" is revised to revision (\d+) with the window from (\d+) to (\d+) hours$`, func(promise string, revision, from, to int) error {
		w.run(func() error {
			_, err := usecases.NewApplyRevisedPromise(w.deps).Execute(w.ctx(), usecases.ApplyRevisedPromiseCommand{
				PromiseID: promise, RevisionNo: revision, DeliverByEarliest: hours(from), DeliverByLatest: hours(to)})
			return err
		})
		return w.lastErr
	})
}

// placeWindow queues one group whose promised window starts start hours out and ends 60 hours out.
func (w *world) placeWindow(shopper, order, tier string, units int, item string, start int) error {
	w.lastPlaced = usecases.QueueAssignmentCommand{
		Meta:    usecases.Meta{EventID: "evt-" + order, EventType: orderPlacedType},
		OrderID: order, DestinationPostalPrefix: "30301", ConsolidationKey: shopperKey(shopper),
		Groups: []usecases.GroupCommand{{GroupNo: 1, PromiseID: "prm-" + order + "-1", Tier: assignment.Tier(tier), WitnessNodeID: "fc-a",
			DeliverByEarliest: hours(start), DeliverByLatest: hours(60), Lines: []assignment.Line{{LineNo: 1, CatalogItemID: item, Quantity: units}}}},
	}
	w.run(func() error { _, err := usecases.NewQueueAssignment(w.deps).Execute(w.ctx(), w.lastPlaced); return err })
	return w.lastErr
}
