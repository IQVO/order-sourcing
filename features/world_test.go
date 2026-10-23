package features_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// t0 is the service clock of every scenario.
var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func hours(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }

// world is the state of one scenario: the in-memory store behind the use cases and the outcome
// of the last command.
type world struct {
	store *memory.Store
	deps  usecases.Deps

	lastPlaced usecases.QueueAssignmentCommand
	lastErr    error
	outboxMark int
}

func newWorld() *world {
	w := &world{}
	w.reset()
	return w
}

func (w *world) reset() {
	s := memory.NewStore()
	w.store = s
	w.deps = usecases.Deps{
		Assignments: s.Assignments(), ReadModels: s.ReadModels(), World: s.World(), Outbox: s.Outbox(),
		Inbox: s.Inbox(), UoW: s.UnitOfWork(), Clock: memory.NewClock(t0), IDs: &memory.SequentialIDs{},
	}
	w.lastPlaced, w.lastErr, w.outboxMark = usecases.QueueAssignmentCommand{}, nil, 0
}

func (w *world) ctx() context.Context { return context.Background() }

// run executes one command, remembering its error and where the outbox stood, so the Then steps
// can ask what the command published. A refused command is not a failed step: the scenario
// asserts the outcome.
func (w *world) run(command func() error) {
	w.outboxMark = len(w.store.Outbox().Events())
	w.lastErr = command()
}

func (w *world) published() []string {
	events := w.store.Outbox().Events()[w.outboxMark:]
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.EventName()
	}
	return names
}

// seedNetwork stores two vendor fulfilment centres in two regions that reach one delivery
// station through one sort centre, with one wave and stock of cat-1 on both.
func (w *world) seedNetwork() error {
	rm, ctx := w.store.ReadModels(), w.ctx()
	steps := []error{
		rm.PutNode(ctx, vendorNode("fc-a", "r1")),
		rm.PutNode(ctx, vendorNode("fc-b", "r2")),
		rm.PutNode(ctx, network.Node{ID: "sc-1", Type: "SORTATION_CENTER", Operator: "NETWORK", Status: network.StatusActive, SortDwellMinutes: 30}),
		rm.PutDeparture(ctx, departure("pk-a", "fc-a", "sc-1", 4, 5, 8)),
		rm.PutDeparture(ctx, departure("pk-b", "fc-b", "sc-1", 4, 5, 8)),
		rm.PutDeparture(ctx, departure("lh-1", "sc-1", "ds-1", 10, 11, 14)),
		rm.PutWave(ctx, network.Wave{ID: "w1", StationID: "ds-1", InductCutoffAt: hours(15), WindowStart: hours(20), WindowEnd: hours(26),
			Capacity: 50, Remaining: 50, RemainingKnown: true, Scheduled: true}),
		rm.PutServiceArea(ctx, network.ServiceArea{PostalPrefix: "30301", StationID: "ds-1", RegionID: "r1", Version: 1}),
	}
	for _, n := range []string{"fc-a", "fc-b"} {
		steps = append(steps, rm.PutListing(ctx, listing(n, 10)))
	}
	for _, err := range steps {
		if err != nil {
			return fmt.Errorf("seed the network: %w", err)
		}
	}
	return nil
}

func vendorNode(id, region string) network.Node {
	return network.Node{ID: id, Type: network.TypeFulfillmentCenter, Subtype: "SORTABLE", Operator: network.OperatorVendor, RegionID: region,
		Status: network.StatusActive, ProfileRevision: 1, HandlingMinutes: 60}
}

func departure(id, from, to string, cutoff, departs, arrives int) network.Departure {
	return network.Departure{ID: id, LaneID: "lane-1", Kind: "LINEHAUL", Origin: from, Destination: to, CutoffAt: hours(cutoff),
		DepartsAt: hours(departs), ArrivesAt: hours(arrives), Capacity: 100, Remaining: 100, RemainingKnown: true, Scheduled: true}
}

func listing(node string, qty int) network.Listing {
	return network.Listing{ID: "lst-1-" + node, CatalogItemID: "cat-1", NodeID: node, AvailableQty: qty, AsOf: hours(0)}
}

// assignmentOf finds the assignment of one group of an order.
func (w *world) assignmentOf(order string, group int) (views.Assignment, error) {
	a, err := w.store.Assignments().FindByGroup(w.ctx(), order, group)
	if err != nil {
		return views.Assignment{}, fmt.Errorf("the scenario has no assignment for %s group %d: %w", order, group, err)
	}
	return views.AssignmentFrom(a), nil
}

func (w *world) idOf(order string, group int) (assignment.ID, error) {
	v, err := w.assignmentOf(order, group)
	return assignment.ID(v.AssignmentID), err
}

func joinNames(names []string) string { return strings.Join(names, ", ") }
