package features_test

import (
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

// shopperKey derives a stable 64-hex consolidation key from a shopper name.
func shopperKey(shopper string) string {
	return strings.Repeat(fmt.Sprintf("%02x", shopper[len(shopper)-1]), 32)
}

func registerConsolidationSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the operator allows (\d+) assignments per merged release$`, func(n int) error {
		w.run(func() error {
			_, err := usecases.NewSourcingPolicies(w.deps).Publish(w.ctx(), policy.Input{ConsolidationMaxAssignments: n})
			return err
		})
		return w.lastErr
	})
	sc.Step(`^shopper "([^"]*)" places order "([^"]*)" with a (CONSOLIDATED|SAME_DAY|NEXT_DAY|TWO_DAY|STANDARD) group of (\d+) units of "([^"]*)"$`,
		func(shopper, order, tier string, units int, item string) error {
			w.lastPlaced = usecases.QueueAssignmentCommand{
				Meta:    usecases.Meta{EventID: "evt-" + order, EventType: orderPlacedType},
				OrderID: order, DestinationPostalPrefix: "30301", ConsolidationKey: shopperKey(shopper),
				Groups: []usecases.GroupCommand{{GroupNo: 1, PromiseID: "prm-" + order + "-1", Tier: assignment.Tier(tier), WitnessNodeID: "fc-a",
					DeliverByLatest: hours(48), Lines: []assignment.Line{{LineNo: 1, CatalogItemID: item, Quantity: units}}}},
			}
			w.run(func() error { _, err := usecases.NewQueueAssignment(w.deps).Execute(w.ctx(), w.lastPlaced); return err })
			return w.lastErr
		})
	sc.Step(`^the node accepts release (\d+) of "([^"]*)" group (\d+)$`, func(no int, order string, group int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewLockAssignment(w.deps).Execute(w.ctx(), usecases.LockAssignmentCommand{AssignmentID: id, ReleaseNo: no, AcceptedAt: hours(1)})
			return err
		})
	})
	sc.Step(`^the node rejects release (\d+) of "([^"]*)" group (\d+)$`, func(no int, order string, group int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewHandleNodeRefusal(w.deps).Execute(w.ctx(), usecases.HandleNodeRefusalCommand{
				AssignmentID: id, ReleaseNo: no, Outcome: assignment.OutcomeRejected, ReasonCode: "BDD"})
			return err
		})
	})
	sc.Step(`^the last release event lists (\d+) members and (\d+) units$`, func(members, units int) error {
		rel, err := w.lastRelease()
		if err != nil {
			return err
		}
		got := 0
		for _, l := range rel.Lines {
			got += l.Quantity
		}
		if len(rel.Members) != members || got != units {
			return fmt.Errorf("release lists %d members and %d units, want %d and %d", len(rel.Members), got, members, units)
		}
		return nil
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) follows the carrier "([^"]*)" group (\d+)$`, func(order string, group int, carrierOrder string, carrierGroup int) error {
		carrier, err := w.idOf(carrierOrder, carrierGroup)
		if err != nil {
			return err
		}
		return w.check(order, group, func(v views.Assignment) error {
			if v.ConsolidatedInto != string(carrier) {
				return fmt.Errorf("consolidatedInto = %q, want %q", v.ConsolidatedInto, carrier)
			}
			return nil
		})
	})
}
