package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func h(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }

var errBoom = errors.New("boom")

// fault makes one named port call fail: n failures with err, then it works.
type fault struct {
	err error
	n   int
}

func (f *fault) hit() error {
	if f.n > 0 {
		f.n--
		return f.err
	}
	return nil
}

type flakyAssignments struct {
	ports.AssignmentRepository
	save, get, due, queued, list fault
}

func (f *flakyAssignments) Save(ctx context.Context, a *assignment.Assignment) error {
	if err := f.save.hit(); err != nil {
		return err
	}
	return f.AssignmentRepository.Save(ctx, a)
}

func (f *flakyAssignments) Get(ctx context.Context, id assignment.ID) (*assignment.Assignment, error) {
	if err := f.get.hit(); err != nil {
		return nil, err
	}
	return f.AssignmentRepository.Get(ctx, id)
}

func (f *flakyAssignments) Due(ctx context.Context, now time.Time, limit int) ([]assignment.ID, error) {
	if err := f.due.hit(); err != nil {
		return nil, err
	}
	return f.AssignmentRepository.Due(ctx, now, limit)
}

func (f *flakyAssignments) Queued(ctx context.Context, after assignment.ID, limit int) ([]assignment.ID, error) {
	if err := f.queued.hit(); err != nil {
		return nil, err
	}
	return f.AssignmentRepository.Queued(ctx, after, limit)
}

func (f *flakyAssignments) List(ctx context.Context, fl repository.AssignmentFilter) ([]*assignment.Assignment, error) {
	if err := f.list.hit(); err != nil {
		return nil, err
	}
	return f.AssignmentRepository.List(ctx, fl)
}

type flakyOutbox struct {
	ports.EventOutbox
	fault
}

func (f *flakyOutbox) Append(ctx context.Context, events ...shared.Event) error {
	if err := f.hit(); err != nil {
		return err
	}
	return f.EventOutbox.Append(ctx, events...)
}

type flakyWorld struct {
	ports.WorldReader
	fault
}

func (f *flakyWorld) Load(ctx context.Context, q repository.WorldQuery) (sourcing.World, error) {
	if err := f.hit(); err != nil {
		return sourcing.World{}, err
	}
	return f.WorldReader.Load(ctx, q)
}

type flakyInbox struct {
	ports.Inbox
	fault
}

func (f *flakyInbox) Claim(ctx context.Context, id, typ string) (bool, error) {
	if err := f.hit(); err != nil {
		return false, err
	}
	return f.Inbox.Claim(ctx, id, typ)
}

type flakyReadModels struct {
	ports.ReadModels
	get, put fault
}

func (f *flakyReadModels) GetNode(ctx context.Context, id string) (network.Node, error) {
	if err := f.get.hit(); err != nil {
		return network.Node{}, err
	}
	return f.ReadModels.GetNode(ctx, id)
}

func (f *flakyReadModels) PutNode(ctx context.Context, n network.Node) error {
	if err := f.put.hit(); err != nil {
		return err
	}
	return f.ReadModels.PutNode(ctx, n)
}

// env is one use-case test bench: the memory doubles behind fault-injecting wrappers.
type env struct {
	t           *testing.T
	store       *memory.Store
	clock       *memory.Clock
	assignments *flakyAssignments
	outbox      *flakyOutbox
	world       *flakyWorld
	inbox       *flakyInbox
	rm          *flakyReadModels
	deps        usecases.Deps
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := memory.NewStore()
	e := &env{
		t: t, store: s, clock: memory.NewClock(t0),
		assignments: &flakyAssignments{AssignmentRepository: s.Assignments()},
		outbox:      &flakyOutbox{EventOutbox: s.Outbox()},
		world:       &flakyWorld{WorldReader: s.World()},
		inbox:       &flakyInbox{Inbox: s.Inbox()},
		rm:          &flakyReadModels{ReadModels: s.ReadModels()},
	}
	e.deps = usecases.Deps{
		Assignments: e.assignments, ReadModels: e.rm, World: e.world, Outbox: e.outbox, Inbox: e.inbox,
		UoW: s.UnitOfWork(), Clock: e.clock, IDs: &memory.SequentialIDs{},
	}
	e.seed()
	return e
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

// seed stores the network of the sourcing tests: fc-a (region r1) and fc-b (r2) reach ds-1 (r1) through sc-1.
func (e *env) seed() {
	rm := e.store.ReadModels()
	ctx := context.Background()
	fc := func(id, region string) network.Node {
		return network.Node{ID: id, Type: network.TypeFulfillmentCenter, Subtype: "SORTABLE", Operator: network.OperatorVendor, RegionID: region,
			Status: network.StatusActive, ProfileRevision: 1, HandlingMinutes: 60}
	}
	e.must(rm.PutNode(ctx, fc("fc-a", "r1")))
	e.must(rm.PutNode(ctx, fc("fc-b", "r2")))
	e.must(rm.PutNode(ctx, network.Node{ID: "sc-1", Type: "SORTATION_CENTER", Operator: "NETWORK", Status: network.StatusActive, SortDwellMinutes: 30}))
	dep := func(id, from, to string, cutoff, departs, arrives int) network.Departure {
		return network.Departure{ID: id, LaneID: "lane-1", Kind: "LINEHAUL", Origin: from, Destination: to, CutoffAt: h(cutoff), DepartsAt: h(departs),
			ArrivesAt: h(arrives), Capacity: 100, Remaining: 100, RemainingKnown: true, Scheduled: true}
	}
	for _, d := range []network.Departure{dep("pk-a", "fc-a", "sc-1", 4, 5, 8), dep("pk-b", "fc-b", "sc-1", 4, 5, 8), dep("lh-1", "sc-1", "ds-1", 10, 11, 14)} {
		e.must(rm.PutDeparture(ctx, d))
	}
	e.must(rm.PutWave(ctx, network.Wave{ID: "w1", StationID: "ds-1", InductCutoffAt: h(15), WindowStart: h(20), WindowEnd: h(26), Capacity: 50,
		Remaining: 50, RemainingKnown: true, Scheduled: true}))
	e.must(rm.PutServiceArea(ctx, network.ServiceArea{PostalPrefix: "30301", StationID: "ds-1", RegionID: "r1", Version: 1}))
	for _, n := range []string{"fc-a", "fc-b"} {
		e.must(rm.PutListing(ctx, network.Listing{ID: "lst-1-" + n, CatalogItemID: "cat-1", NodeID: n, AvailableQty: 10, AsOf: h(0)}))
	}
}

func lines() []assignment.Line {
	return []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}}
}

func group(no int, promise string, tier assignment.Tier) usecases.GroupCommand {
	return usecases.GroupCommand{GroupNo: no, PromiseID: promise, Tier: tier, WitnessNodeID: "fc-a", DeliverByLatest: h(48), Lines: lines()}
}

func placed(eventID string, groups ...usecases.GroupCommand) usecases.QueueAssignmentCommand {
	return usecases.QueueAssignmentCommand{
		Meta:    usecases.Meta{EventID: eventID, EventType: "com.retailnetwork.commerce.customer-ordering.customerorder.CustomerOrderPlaced"},
		OrderID: "ord-1", DestinationPostalPrefix: "30301", Groups: groups,
	}
}

// queue places one NEXT_DAY group and returns its id.
func (e *env) queue() assignment.ID {
	e.t.Helper()
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), placed("", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	if len(got) != 1 {
		e.t.Fatalf("queued %d groups", len(got))
	}
	return assignment.ID(got[0].AssignmentID)
}

func (e *env) release(id assignment.ID) {
	e.t.Helper()
	_, err := usecases.NewReleaseToNode(e.deps).Execute(context.Background(), id)
	e.must(err)
}

func (e *env) lock(id assignment.ID) {
	e.t.Helper()
	e.release(id)
	_, err := usecases.NewLockAssignment(e.deps).Execute(context.Background(), usecases.LockAssignmentCommand{AssignmentID: id, ReleaseNo: 1, AcceptedAt: h(1)})
	e.must(err)
}

func (e *env) get(id assignment.ID) *assignment.Assignment {
	e.t.Helper()
	a, err := e.store.Assignments().Get(context.Background(), id)
	e.must(err)
	return a
}

func (e *env) eventNames() []string {
	var out []string
	for _, ev := range e.store.Outbox().Events() {
		out = append(out, ev.EventName())
	}
	return out
}

func wantNames(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func (e *env) wantUnchanged(before memory.Counts) {
	e.t.Helper()
	if got := e.store.Counts(); got != before {
		e.t.Fatalf("a failed use case must change nothing: %+v -> %+v", before, got)
	}
}

func listingQty(node string, qty int) network.Listing {
	return network.Listing{ID: "lst-1-" + node, CatalogItemID: "cat-1", NodeID: node, AvailableQty: qty, AsOf: h(5)}
}

func listingZero(node string) network.Listing { return listingQty(node, 0) }
