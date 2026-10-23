package features_test

import (
	"fmt"
	"strconv"

	"github.com/cucumber/godog"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

const orderPlacedType = "com.retailnetwork.commerce.customer-ordering.customerorder.CustomerOrderPlaced"

func registerGivenSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a network with vendor nodes "([^"]*)" and "([^"]*)" that both reach the delivery station "([^"]*)"$`,
		func(_, _, _ string) error { return w.seedNetwork() })
	sc.Step(`^order "([^"]*)" is placed with a (NEXT_DAY|SAME_DAY|TWO_DAY|STANDARD) group of (\d+) units of "([^"]*)"$`, w.place)
	sc.Step(`^the same order event is delivered again$`, func() error {
		w.run(func() error { _, err := usecases.NewQueueAssignment(w.deps).Execute(w.ctx(), w.lastPlaced); return err })
		return nil
	})
	sc.Step(`^the node "([^"]*)" has no stock of "([^"]*)" any more$`, func(node, item string) error {
		l := listing(node, 0)
		l.CatalogItemID, l.AsOf = item, hours(1)
		return w.store.ReadModels().PutListing(w.ctx(), l)
	})
}

// place queues one group of an order; the promise id is prm-<order>-1 and the deadline 48 hours out.
func (w *world) place(order, tier, units, item string) error {
	qty, err := strconv.Atoi(units)
	if err != nil {
		return fmt.Errorf("units %q: %w", units, err)
	}
	w.lastPlaced = usecases.QueueAssignmentCommand{
		Meta:    usecases.Meta{EventID: "evt-" + order, EventType: orderPlacedType},
		OrderID: order, DestinationPostalPrefix: "30301",
		Groups: []usecases.GroupCommand{{GroupNo: 1, PromiseID: "prm-" + order + "-1", Tier: assignment.Tier(tier), WitnessNodeID: "fc-a",
			DeliverByLatest: hours(48), Lines: []assignment.Line{{LineNo: 1, CatalogItemID: item, Quantity: qty}}}},
	}
	w.run(func() error { _, err := usecases.NewQueueAssignment(w.deps).Execute(w.ctx(), w.lastPlaced); return err })
	return nil
}
