package features_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	resthttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// This file holds the steps of the documentation-derived scenarios (features/*_rules.feature):
// the rules of warehouse-docs network/docs/contexts/order-sourcing (use cases, aggregate
// canvas, policies) that no earlier scenario pins. Read-model facts are seeded through the
// Projector, the use case the Kafka consumers call, so the out-of-order and redelivery rules
// of the projections are exercised too.

// rulesSeq numbers the read-model events of one scenario.
var rulesSeq int

// policyRouter serves /sourcing-policy over the same in-memory store as the use cases.
var policyRouter http.Handler

func registerRulesSteps(sc *godog.ScenarioContext, w *world) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		rulesSeq = 0
		policies := usecases.NewSourcingPolicies(w.deps)
		policyRouter = (&resthttp.Server{PolicyGet: policies, PolicyPut: policies}).Handler()
		return ctx, nil
	})
	registerRulesReadModelSteps(sc, w)
	registerRulesPlacementSteps(sc, w)
	registerRulesCommandSteps(sc, w)
	registerRulesAssertionSteps(sc, w)
	registerRulesEventSteps(sc, w)
	registerRulesPolicySteps(sc, w)
}

// readModelMeta mints the identity of one more sibling event.
func readModelMeta(kind string) usecases.Meta {
	rulesSeq++
	return usecases.Meta{EventID: fmt.Sprintf("evt-rm-%d", rulesSeq), EventType: "com.retailnetwork.rules." + kind}
}

func (w *world) projector() *usecases.Projector { return usecases.NewProjector(w.deps) }

// seed reports the error of a Given step that changes a read model.
func seed(err error) error {
	if err != nil {
		return fmt.Errorf("seed the read model: %w", err)
	}
	return nil
}

func registerRulesReadModelSteps(sc *godog.ScenarioContext, w *world) {
	statuses := map[string]string{"suspended": network.StatusSuspended, "activated": network.StatusActive, "retired": network.StatusRetired}
	sc.Step(`^the node "([^"]*)" is (suspended|activated|retired) at hour (\d+)$`, func(node, what string, hour int) error {
		return seed(w.projector().NodeStatusChanged(w.ctx(), readModelMeta("NodeStatus"), node, statuses[what], hours(hour)))
	})
	sc.Step(`^the node "([^"]*)" declares operating profile revision (\d+) with a handling time of (\d+) minutes$`, func(node string, revision, minutes int) error {
		return seed(w.projector().NodeProfileDeclared(w.ctx(), readModelMeta("NodeOperationalProfileDeclared"), node, revision, minutes, 0))
	})
	sc.Step(`^the node "([^"]*)" is registered in region "([^"]*)"$`, func(node, region string) error {
		n, err := w.store.ReadModels().GetNode(w.ctx(), node)
		if err != nil {
			return seed(err)
		}
		return seed(w.projector().NodeRegistered(w.ctx(), readModelMeta("NodeRegistered"), node,
			network.Registered{Type: n.Type, Subtype: n.Subtype, Operator: n.Operator, RegionID: region, At: hours(0)}))
	})
	sc.Step(`^the node "([^"]*)" is registered as operated by "([^"]*)"$`, func(node, operator string) error {
		n, err := w.store.ReadModels().GetNode(w.ctx(), node)
		if err != nil {
			return seed(err)
		}
		return seed(w.projector().NodeRegistered(w.ctx(), readModelMeta("NodeRegistered"), node,
			network.Registered{Type: n.Type, Subtype: n.Subtype, Operator: operator, RegionID: n.RegionID, At: hours(0)}))
	})
	sc.Step(`^the pickup departure "([^"]*)" is cancelled$`, func(id string) error {
		return seed(w.projector().DepartureCancelled(w.ctx(), readModelMeta("DepartureCancelled"), id))
	})
	sc.Step(`^the pickup departure "([^"]*)" reports (\d+) places left$`, func(id string, places int) error {
		return seed(w.projector().DepartureCapacityChanged(w.ctx(), readModelMeta("DepartureCapacityChanged"), id, places))
	})
	sc.Step(`^the pickup departure "([^"]*)" reports (\d+) places left in event "([^"]*)"$`, func(id string, places int, event string) error {
		return seed(w.projector().DepartureCapacityChanged(w.ctx(), usecases.Meta{EventID: event, EventType: "com.retailnetwork.rules.DepartureCapacityChanged"}, id, places))
	})
	sc.Step(`^the delivery wave "([^"]*)" reports (\d+) stops left$`, func(id string, stops int) error {
		return seed(w.projector().WaveCapacityChanged(w.ctx(), readModelMeta("DeliveryWaveCapacityChanged"), id, stops))
	})
	sc.Step(`^the lane of the pickup of "([^"]*)" is closed$`, func(node string) error {
		return seed(w.projector().LaneClosed(w.ctx(), readModelMeta("LaneClosed"), "lane-"+node))
	})
	sc.Step(`^the listing of "([^"]*)" at "([^"]*)" is (suppressed|restored)$`, func(item, node, what string) error {
		return seed(w.projector().ListingSuppressionChanged(w.ctx(), readModelMeta("Listing"+what), "lst-1-"+node, what == "suppressed"))
	})
	sc.Step(`^the node "([^"]*)" reports (\d+) units of "([^"]*)" as of hour (\d+)$`, func(node string, qty int, item string, hour int) error {
		return seed(w.projector().ListingAvailabilityChanged(w.ctx(), readModelMeta("ListingAvailabilityChanged"),
			network.Listing{ID: "lst-1-" + node, CatalogItemID: item, NodeID: node, AvailableQty: qty, AsOf: hours(hour)}))
	})
	sc.Step(`^the node "([^"]*)" has a "([^"]*)" scorecard over the "([^"]*)" window$`, func(node, grade, window string) error {
		return seed(w.projector().ScorecardUpdated(w.ctx(), readModelMeta("NodeScorecardUpdated"),
			network.Scorecard{NodeID: node, Window: window, Grade: grade, ComputedAt: hours(1)}))
	})
	sc.Step(`^the promise book of the pickup departure "([^"]*)" of node "([^"]*)" is exhausted$`, func(departure, node string) error {
		return seed(w.projector().BookExhausted(w.ctx(), readModelMeta("PromiseBookExhausted"),
			network.Pressure{BookID: "book-" + departure, NodeID: node, PickupDepartureID: departure}))
	})
	sc.Step(`^the pickup of "([^"]*)" has its cutoff at hour (\d+)$`, func(node string, hour int) error {
		d := departure("pk-"+suffix(node), node, "sc-1", hour, hour+1, hour+2)
		return seed(w.store.ReadModels().PutDeparture(w.ctx(), d))
	})
	sc.Step(`^a (micro-fulfilment|sortable) node "([^"]*)" ships straight to the delivery station "([^"]*)"$`, func(kind, node, station string) error {
		n := vendorNode(node, "r1")
		if kind == "micro-fulfilment" {
			n.Subtype = network.SubtypeSubSameDay
		}
		rm := w.store.ReadModels()
		for _, err := range []error{
			rm.PutNode(w.ctx(), n),
			rm.PutDeparture(w.ctx(), departure("pk-"+suffix(node), node, station, 3, 4, 5)),
			rm.PutListing(w.ctx(), listing(node, 10)),
		} {
			if err != nil {
				return seed(err)
			}
		}
		return nil
	})
	sc.Step(`^the postal prefix "([^"]*)" is also served by the delivery station "([^"]*)"$`, func(prefix, station string) error {
		return seed(w.store.ReadModels().PutServiceArea(w.ctx(), network.ServiceArea{PostalPrefix: prefix, StationID: station, RegionID: "r1", Version: 1}))
	})
	sc.Step(`^the clock is advanced by (\d+) minutes$`, func(n int) error {
		c, ok := w.deps.Clock.(*memory.Clock)
		if !ok {
			return fmt.Errorf("the scenario clock is %T, not the settable double", w.deps.Clock)
		}
		c.Advance(time.Duration(n) * time.Minute)
		return nil
	})
}

// order builds the command of one placed order.
func (w *world) order(id, key, prefix string, groups ...usecases.GroupCommand) usecases.QueueAssignmentCommand {
	return usecases.QueueAssignmentCommand{
		Meta:    usecases.Meta{EventID: "evt-" + id, EventType: orderPlacedType},
		OrderID: id, DestinationPostalPrefix: prefix, ConsolidationKey: key, Groups: groups,
	}
}

// group builds one shipment group of an order; the promise id is prm-<order>-<groupNo>.
func groupOf(order string, no int, tier string, units int, item string, earliest, latest time.Time) usecases.GroupCommand {
	return usecases.GroupCommand{GroupNo: no, PromiseID: fmt.Sprintf("prm-%s-%d", order, no), Tier: assignment.Tier(tier), WitnessNodeID: "fc-a",
		DeliverByEarliest: earliest, DeliverByLatest: latest, Lines: []assignment.Line{{LineNo: 1, CatalogItemID: item, Quantity: units}}}
}

// queue runs QueueAssignment for cmd and remembers it as the last placed order.
func (w *world) queue(cmd usecases.QueueAssignmentCommand) error {
	w.lastPlaced = cmd
	w.run(func() error { _, err := usecases.NewQueueAssignment(w.deps).Execute(w.ctx(), cmd); return err })
	return nil
}

func registerRulesPlacementSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^order "([^"]*)" is placed with a (CONSOLIDATED|SAME_DAY|NEXT_DAY|TWO_DAY|STANDARD) group of (\d+) units of "([^"]*)" and a deadline in (\d+) minutes$`,
		func(order, tier string, units int, item string, minutes int) error {
			return w.queue(w.order(order, "", "30301", groupOf(order, 1, tier, units, item, time.Time{}, t0.Add(time.Duration(minutes)*time.Minute))))
		})
	sc.Step(`^order "([^"]*)" is placed again under a new event id$`, func(order string) error {
		cmd := w.lastPlaced
		cmd.Meta.EventID += "-again"
		return w.queue(cmd)
	})
	sc.Step(`^order "([^"]*)" is placed with these groups:$`, func(order string, table *godog.Table) error {
		var groups []usecases.GroupCommand
		for _, row := range table.Rows[1:] {
			no, errNo := strconv.Atoi(row.Cells[0].Value)
			units, errUnits := strconv.Atoi(row.Cells[2].Value)
			if err := errors.Join(errNo, errUnits); err != nil {
				return fmt.Errorf("the groups table: %w", err)
			}
			groups = append(groups, groupOf(order, no, row.Cells[1].Value, units, row.Cells[3].Value, time.Time{}, hours(48)))
		}
		cmd := w.order(order, "", "30301", groups...)
		cmd.Meta.EventID = fmt.Sprintf("evt-%s-%dgroups", order, len(groups))
		return w.queue(cmd)
	})
	sc.Step(`^order "([^"]*)" is placed with a CONSOLIDATED group of (\d+) units of "([^"]*)" under the consolidation key "([^"]*)"$`,
		func(order string, units int, item, key string) error {
			return w.queue(w.order(order, key, "30301", groupOf(order, 1, "CONSOLIDATED", units, item, time.Time{}, hours(48))))
		})
	sc.Step(`^shopper "([^"]*)" places order "([^"]*)" with a CONSOLIDATED group of (\d+) units of "([^"]*)" for the postal prefix "([^"]*)"$`,
		func(shopper, order string, units int, item, prefix string) error {
			return w.queue(w.order(order, shopperKey(shopper), prefix, groupOf(order, 1, "CONSOLIDATED", units, item, time.Time{}, hours(48))))
		})
	sc.Step(`^shopper "([^"]*)" places order "([^"]*)" with a CONSOLIDATED group of (\d+) units of "([^"]*)" and a window from (\d+) to (\d+) hours$`,
		func(shopper, order string, units int, item string, from, to int) error {
			return w.queue(w.order(order, shopperKey(shopper), "30301", groupOf(order, 1, "CONSOLIDATED", units, item, hours(from), hours(to))))
		})
	sc.Step(`^the group of order "([^"]*)" has been (released|locked|shipped|delivered)$`, w.takeGroupTo)
}

// takeGroupTo walks group 1 of an order through the happy path up to the named state.
func (w *world) takeGroupTo(order, to string) error {
	id, err := w.idOf(order, 1)
	if err != nil {
		return err
	}
	steps := []func() error{
		func() error { _, err := usecases.NewReleaseToNode(w.deps).Execute(w.ctx(), id); return err },
		func() error {
			_, err := usecases.NewLockAssignment(w.deps).Execute(w.ctx(), usecases.LockAssignmentCommand{AssignmentID: id, ReleaseNo: 1, AcceptedAt: hours(1)})
			return err
		},
		func() error {
			_, err := usecases.NewMarkShipped(w.deps).Execute(w.ctx(), usecases.MarkShippedCommand{AssignmentID: id, TrackingNumbers: []string{"TRK1"}, ShippedAt: hours(2)})
			return err
		},
		func() error {
			_, err := usecases.NewMarkDelivered(w.deps).Execute(w.ctx(), usecases.MarkDeliveredCommand{AssignmentID: id, TrackingNumber: "TRK1", DeliveredAt: hours(30)})
			return err
		},
	}
	n := map[string]int{"released": 1, "locked": 2, "shipped": 3, "delivered": 4}[to]
	for i := 0; i < n; i++ {
		if err := steps[i](); err != nil {
			return fmt.Errorf("take %s to %s: %w", order, to, err)
		}
	}
	return nil
}

func registerRulesCommandSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the customer cancels groups "([^"]*)" of order "([^"]*)"$`, func(list, order string) error {
		var nos []int
		for _, s := range splitList(list) {
			n, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("group number %q: %w", s, err)
			}
			nos = append(nos, n)
		}
		w.run(func() error {
			_, err := usecases.NewCancelAssignment(w.deps).Execute(w.ctx(), usecases.CancelAssignmentCommand{OrderID: order, GroupNos: nos})
			return err
		})
		return nil
	})
	sc.Step(`^the customer cancels group (\d+) of order "([^"]*)" with the reason "([^"]*)"$`, func(group int, order, reason string) error {
		w.run(func() error {
			_, err := usecases.NewCancelAssignment(w.deps).Execute(w.ctx(), usecases.CancelAssignmentCommand{OrderID: order, GroupNos: []int{group}, Reason: reason})
			return err
		})
		return nil
	})
	sc.Step(`^the node rejects release (\d+) with the reason code "([^"]*)"$`, func(no int, code string) error {
		return w.onOnly(func(id assignment.ID) error {
			_, err := usecases.NewHandleNodeRefusal(w.deps).Execute(w.ctx(), usecases.HandleNodeRefusalCommand{
				AssignmentID: id, ReleaseNo: no, Outcome: assignment.OutcomeRejected, ReasonCode: code})
			return err
		})
	})
	sc.Step(`^the node lets release (\d+) of "([^"]*)" group (\d+) expire$`, func(no int, order string, group int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewHandleNodeRefusal(w.deps).Execute(w.ctx(), usecases.HandleNodeRefusalCommand{
				AssignmentID: id, ReleaseNo: no, Outcome: assignment.OutcomeExpired})
			return err
		})
	})
	sc.Step(`^promise "([^"]*)" is revised to revision (\d+) with the deadline in (\d+) hours and the witness node "([^"]*)"$`, func(promise string, revision, deadline int, node string) error {
		w.run(func() error {
			_, err := usecases.NewApplyRevisedPromise(w.deps).Execute(w.ctx(), usecases.ApplyRevisedPromiseCommand{
				PromiseID: promise, RevisionNo: revision, DeliverByLatest: hours(deadline), WitnessNodeID: node})
			return err
		})
		return nil
	})
}

func registerRulesAssertionSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the command is refused as an illegal transition$`, func() error {
		if !errors.Is(w.lastErr, shared.ErrIllegalTransition) {
			return fmt.Errorf("the command returned %v, want an illegal transition", w.lastErr)
		}
		return nil
	})
	sc.Step(`^there are exactly (\d+) assignments$`, func(n int) error { return w.wantAssignments(n) })
	sc.Step(`^the assignment of "([^"]*)" group (\d+) is due for release (\d+) minutes after the start$`, func(order string, group, minutes int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if want := t0.Add(time.Duration(minutes) * time.Minute); !v.ReleaseAt.Equal(want) {
				return fmt.Errorf("release point is %s, want %s", v.ReleaseAt, want)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has version (\d+)$`, func(order string, group, version int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.AssignmentVersion != int64(version) {
				return fmt.Errorf("assignment version is %d, want %d", v.AssignmentVersion, version)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has the estimated cost (\d+) cents$`, func(order string, group, cents int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.Candidate == nil || !v.Candidate.CostComplete || v.Candidate.EstimatedCostCents != int64(cents) {
				return fmt.Errorf("candidate = %+v, want a complete estimate of %d cents", v.Candidate, cents)
			}
			return nil
		})
	})
	registerRulesReleaseAssertions(sc, w)
	registerRulesFactAssertions(sc, w)
}

func (w *world) wantAssignments(n int) error {
	all, err := w.store.Assignments().List(w.ctx(), repository.AssignmentFilter{})
	if err != nil {
		return fmt.Errorf("assignments: %w", err)
	}
	if len(all) != n {
		return fmt.Errorf("assignments = %d, want %d", len(all), n)
	}
	return nil
}

func registerRulesReleaseAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has (\d+) releases?$`, func(order string, group, n int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if len(v.Releases) != n {
				return fmt.Errorf("releases are %+v, want %d", v.Releases, n)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has release number (\d+) with the outcome "([^"]*)"$`, func(order string, group, no int, outcome string) error {
		return w.check(order, group, func(v views.Assignment) error {
			for _, r := range v.Releases {
				if r.ReleaseNo == no {
					if r.Outcome != outcome {
						return fmt.Errorf("release %d is %s, want %s", no, r.Outcome, outcome)
					}
					return nil
				}
			}
			return fmt.Errorf("releases are %+v, want a release number %d", v.Releases, no)
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has release number (\d+) with the reason code "([^"]*)"$`, func(order string, group, no int, code string) error {
		return w.check(order, group, func(v views.Assignment) error {
			for _, r := range v.Releases {
				if r.ReleaseNo == no && r.ReasonCode == code {
					return nil
				}
			}
			return fmt.Errorf("releases are %+v, want release %d with reason code %q", v.Releases, no, code)
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) excluded the node "([^"]*)" as "([^"]*)"$`, func(order string, group int, node, reason string) error {
		return w.check(order, group, func(v views.Assignment) error {
			for _, e := range v.ExcludedNodes {
				if e.NodeID == node && e.Reason == reason {
					return nil
				}
			}
			return fmt.Errorf("excluded nodes are %+v, want %s as %q", v.ExcludedNodes, node, reason)
		})
	})
	sc.Step(`^the last release event goes to node "([^"]*)" as release number (\d+) with the listing "([^"]*)" and required ship-by at hour (\d+)$`,
		func(node string, no int, listing string, hour int) error {
			rel, err := w.lastRelease()
			if err != nil {
				return err
			}
			if rel.NodeID != node || rel.ReleaseNo != no || len(rel.Lines) != 1 || rel.Lines[0].ListingID != listing || !rel.RequiredShipBy.Equal(hours(hour)) {
				return fmt.Errorf("release = %+v, want node %s, number %d, listing %s, ship-by %s", rel, node, no, listing, hours(hour))
			}
			return nil
		})
}

func registerRulesFactAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the assignment of "([^"]*)" group (\d+) was locked at hour (\d+) because "([^"]*)" on the pickup departure "([^"]*)"$`,
		func(order string, group, hour int, reason, departure string) error {
			return w.check(order, group, func(v views.Assignment) error {
				if !v.LockedAt.Equal(hours(hour)) || v.LockReason != reason || v.PickupDepartureID != departure {
					return fmt.Errorf("locked at %s because %q on %q, want hour %d, %q, %q", v.LockedAt, v.LockReason, v.PickupDepartureID, hour, reason, departure)
				}
				return nil
			})
		})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) shipped the packages "([^"]*)" at hour (\d+)$`, func(order string, group int, packages string, hour int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if got := strings.Join(v.TrackingNumbers, ", "); got != packages || !v.ShippedAt.Equal(hours(hour)) {
				return fmt.Errorf("shipped %q at %s, want %q at hour %d", got, v.ShippedAt, packages, hour)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) was delivered at hour (\d+)$`, func(order string, group, hour int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if !v.DeliveredAt.Equal(hours(hour)) {
				return fmt.Errorf("delivered at %s, want hour %d", v.DeliveredAt, hour)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) was cancelled because "([^"]*)"$`, func(order string, group int, reason string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.CancelReason != reason {
				return fmt.Errorf("cancel reason is %q, want %q", v.CancelReason, reason)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) follows no carrier$`, func(order string, group int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.ConsolidatedInto != "" {
				return fmt.Errorf("consolidatedInto = %q, want none", v.ConsolidatedInto)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has the witness node "([^"]*)"$`, func(order string, group int, node string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.Promise.WitnessNodeID != node {
				return fmt.Errorf("witness node is %q, want %q", v.Promise.WitnessNodeID, node)
			}
			return nil
		})
	})
}

// lastEvent finds the most recent published event of a name, as a struct value.
func (w *world) lastEvent(name string) (reflect.Value, error) {
	events := w.store.Outbox().Events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventName() == name {
			v := reflect.ValueOf(events[i])
			for v.Kind() == reflect.Pointer {
				v = v.Elem()
			}
			return v, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("no %s was published in the scenario", name)
}

func registerRulesEventSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the last "([^"]*)" event has "([^"]*)" "([^"]*)"$`, func(name, field, want string) error {
		ev, err := w.lastEvent(name)
		if err != nil {
			return err
		}
		f := ev.FieldByName(field)
		if !f.IsValid() {
			return fmt.Errorf("%s has no field %s", name, field)
		}
		if got := fmt.Sprint(f.Interface()); got != want {
			return fmt.Errorf("%s.%s is %q, want %q", name, field, got, want)
		}
		return nil
	})
	sc.Step(`^the last "([^"]*)" event lists (\d+) "([^"]*)"$`, func(name string, n int, field string) error {
		ev, err := w.lastEvent(name)
		if err != nil {
			return err
		}
		f := ev.FieldByName(field)
		if !f.IsValid() || f.Kind() != reflect.Slice {
			return fmt.Errorf("%s has no list %s", name, field)
		}
		if f.Len() != n {
			return fmt.Errorf("%s.%s has %d entries, want %d", name, field, f.Len(), n)
		}
		return nil
	})
}

func registerRulesPolicySteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the operator publishes a sourcing policy of (-?\d+) cents, (-?\d+) percent and (-?\d+) assignments per merged release$`, func(cents, percent, cap int) error {
		w.run(func() error {
			_, err := usecases.NewSourcingPolicies(w.deps).Publish(w.ctx(), policy.Input{
				CrossRegionSurchargeCents: cents, MaxCostDeviationPercent: percent, ConsolidationMaxAssignments: cap})
			return err
		})
		return nil
	})
	sc.Step(`^the planner reads the sourcing policy$`, func() error { return rest.do(policyRouter, http.MethodGet, "/sourcing-policy", "") })
	sc.Step(`^the planner publishes the sourcing policy with the JSON body (.+)$`, func(body string) error {
		return rest.do(policyRouter, http.MethodPut, "/sourcing-policy", body)
	})
	sc.Step(`^the planner publishes the sourcing policy with an empty body$`, func() error {
		return rest.do(policyRouter, http.MethodPut, "/sourcing-policy", "")
	})
}
