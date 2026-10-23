package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func directDeparture() network.Departure {
	return network.Departure{ID: "pk-direct", LaneID: "lane-1", Kind: "PICKUP", Origin: "fc-a", Destination: "ds-1", CutoffAt: h(4), DepartsAt: h(5),
		ArrivesAt: h(7), Capacity: 10, Remaining: 10, RemainingKnown: true, Scheduled: true}
}

func TestReoptimiseMovesToABetterNode(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	// fc-a loses a listing: fc-b becomes the only candidate.
	e.must(e.store.ReadModels().PutListing(context.Background(), network.Listing{ID: "lst-1-fc-a", CatalogItemID: "cat-1", NodeID: "fc-a", AvailableQty: 0, AsOf: h(1)}))
	got, err := usecases.NewReoptimiseAssignment(e.deps).Execute(context.Background(), id, 0)
	e.must(err)
	if got.Candidate.NodeID != "fc-b" || got.AssignmentVersion != 2 {
		t.Fatalf("view = %+v", got)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReassigned")
}

func TestReoptimiseUnchangedWritesNothing(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	before := e.store.Counts()
	got, err := usecases.NewReoptimiseAssignment(e.deps).Execute(context.Background(), id, 1)
	e.must(err)
	if got.AssignmentVersion != 1 {
		t.Fatalf("version = %d", got.AssignmentVersion)
	}
	e.wantUnchanged(before)
}

func TestReoptimiseErrors(t *testing.T) {
	e := newEnv(t)
	uc := usecases.NewReoptimiseAssignment(e.deps)
	_, err := uc.Execute(context.Background(), "asg-none", 0)
	wantErr(t, err, ports.ErrNotFound)
	id := e.queue()
	_, err = uc.Execute(context.Background(), id, 9)
	wantErr(t, err, assignment.ErrStaleAssignmentVersion)
	e.world.fault = fault{errBoom, 1}
	_, err = uc.Execute(context.Background(), id, 0)
	wantErr(t, err, errBoom)
	e.release(id)
	_, err = uc.Execute(context.Background(), id, 0)
	wantErr(t, err, assignment.ErrReleaseInFlight)
	_, err = usecases.NewLockAssignment(e.deps).Execute(context.Background(), usecases.LockAssignmentCommand{AssignmentID: id, ReleaseNo: 1, AcceptedAt: h(1)})
	e.must(err)
	_, err = uc.Execute(context.Background(), id, 0)
	wantErr(t, err, assignment.ErrAssignmentLocked)
}

func TestReoptimiseWithNoCandidateLeftIsUnsourceable(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.must(e.store.ReadModels().PutDiscontinued(context.Background(), "cat-1"))
	got, err := usecases.NewReoptimiseAssignment(e.deps).Execute(context.Background(), id, 0)
	e.must(err)
	if got.State != "UNSOURCEABLE" {
		t.Fatalf("state = %s", got.State)
	}
	_, err = usecases.NewReoptimiseAssignment(e.deps).Execute(context.Background(), id, 0)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestReleaseToNode(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	got, err := usecases.NewReleaseToNode(e.deps).Execute(context.Background(), id)
	e.must(err)
	if got.State != "RELEASED" || got.Releases[0].NodeID != "fc-a" || got.Lines[0].ListingID != "lst-1-fc-a" {
		t.Fatalf("view = %+v", got)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReleasedToNode")
	before := e.store.Counts()
	_, err = usecases.NewReleaseToNode(e.deps).Execute(context.Background(), id)
	e.must(err)
	e.wantUnchanged(before) // a second timer firing finds RELEASED
}

func TestReleaseToNodeRechecksAndMayGoUnsourceable(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.must(e.store.ReadModels().PutListing(context.Background(), network.Listing{ID: "lst-1-fc-a", CatalogItemID: "cat-1", NodeID: "fc-a", AvailableQty: 0, AsOf: h(1)}))
	got, err := usecases.NewReleaseToNode(e.deps).Execute(context.Background(), id)
	e.must(err)
	if got.Releases[0].NodeID != "fc-b" {
		t.Fatalf("the re-check releases to the best node now: %+v", got.Releases)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReassigned", "FulfillmentReleasedToNode")

	e2 := newEnv(t)
	id2 := e2.queue()
	e2.must(e2.store.ReadModels().PutDiscontinued(context.Background(), "cat-1"))
	got, err = usecases.NewReleaseToNode(e2.deps).Execute(context.Background(), id2)
	e2.must(err)
	if got.State != "UNSOURCEABLE" {
		t.Fatalf("state = %s", got.State)
	}
	e3 := newEnv(t)
	id3 := e3.queue()
	e3.world.fault = fault{errBoom, 1}
	_, err = usecases.NewReleaseToNode(e3.deps).Execute(context.Background(), id3)
	wantErr(t, err, errBoom)
}

func refusal(id assignment.ID, no int, outcome assignment.Outcome) usecases.HandleNodeRefusalCommand {
	return usecases.HandleNodeRefusalCommand{
		Meta: usecases.Meta{EventID: "evt-r-" + string(outcome), EventType: "x"}, AssignmentID: id, ReleaseNo: no, Outcome: outcome, ReasonCode: "OUT_OF_STOCK",
	}
}

func TestHandleNodeRefusalRequeuesOnAnotherNode(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.release(id)
	got, err := usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), refusal(id, 1, assignment.OutcomeRejected))
	e.must(err)
	if got.State != "QUEUED" || got.Candidate.NodeID != "fc-b" || len(got.ExcludedNodes) != 1 || got.ExcludedNodes[0].NodeID != "fc-a" {
		t.Fatalf("view = %+v", got)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReleasedToNode", "FulfillmentReassigned")
	// The same event id is a no-op, a late outcome of release 1 is ErrReleaseNotPending.
	before := e.store.Counts()
	_, err = usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), refusal(id, 1, assignment.OutcomeRejected))
	e.must(err)
	e.wantUnchanged(before)
	late := refusal(id, 1, assignment.OutcomeExpired)
	_, err = usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), late)
	wantErr(t, err, assignment.ErrReleaseNotPending)
}

func TestHandleNodeRefusalWithNoNodeLeftIsUnsourceable(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.must(e.store.ReadModels().PutListing(context.Background(), network.Listing{ID: "lst-1-fc-b", CatalogItemID: "cat-1", NodeID: "fc-b", AvailableQty: 0, AsOf: h(1)}))
	e.release(id)
	got, err := usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), refusal(id, 1, assignment.OutcomeExpired))
	e.must(err)
	if got.State != "UNSOURCEABLE" {
		t.Fatalf("state = %s", got.State)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReleasedToNode", "FulfillmentUnsourceable")
	unknown := refusal("asg-none", 1, assignment.OutcomeExpired)
	unknown.EventID = "evt-unknown"
	_, err = usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), unknown)
	wantErr(t, err, ports.ErrNotFound)
}

func TestHandleNodeRefusalSearchFailureChangesNothing(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.release(id)
	before := e.store.Counts()
	e.world.fault = fault{errBoom, 1}
	_, err := usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), refusal(id, 1, assignment.OutcomeRejected))
	wantErr(t, err, errBoom)
	e.wantUnchanged(before)
	// In QUEUED there is no pending release to exclude: the domain says so.
	e2 := newEnv(t)
	id2 := e2.queue()
	_, err = usecases.NewHandleNodeRefusal(e2.deps).Execute(context.Background(), refusal(id2, 1, assignment.OutcomeRejected))
	wantErr(t, err, assignment.ErrReleaseNotPending)
}

func TestLockShipDeliver(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.release(id)
	lock := usecases.LockAssignmentCommand{Meta: usecases.Meta{EventID: "evt-l", EventType: "x"}, AssignmentID: id, ReleaseNo: 1, AcceptedAt: h(1)}
	got, err := usecases.NewLockAssignment(e.deps).Execute(context.Background(), lock)
	e.must(err)
	if got.State != "LOCKED" || got.LockReason != assignment.LockReasonAccepted || got.PickupDepartureID != "pk-a" || !got.LockedAt.Equal(h(1)) {
		t.Fatalf("locked = %+v", got)
	}
	ship := usecases.MarkShippedCommand{Meta: usecases.Meta{EventID: "evt-s", EventType: "x"}, AssignmentID: id, TrackingNumbers: []string{"T1", "T2"}, ShippedAt: h(2)}
	got, err = usecases.NewMarkShipped(e.deps).Execute(context.Background(), ship)
	e.must(err)
	if got.State != "SHIPPED" || len(got.TrackingNumbers) != 2 {
		t.Fatalf("shipped = %+v", got)
	}
	deliver := func(evt, tn string) (string, error) {
		v, err := usecases.NewMarkDelivered(e.deps).Execute(context.Background(),
			usecases.MarkDeliveredCommand{Meta: usecases.Meta{EventID: evt, EventType: "x"}, AssignmentID: id, TrackingNumber: tn, DeliveredAt: h(30)})
		return v.State, err
	}
	state, err := deliver("evt-d1", "T1")
	e.must(err)
	if state != "SHIPPED" {
		t.Fatalf("one of two packages: %s", state)
	}
	state, err = deliver("evt-d2", "T2")
	e.must(err)
	if state != "DELIVERED" {
		t.Fatalf("both packages: %s", state)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReleasedToNode", "FulfillmentLocked", "FulfillmentShipped", "FulfillmentDelivered")
	if e.store.Outbox().Events()[4].Stream() != shared.StreamAnalytics {
		t.Fatal("FulfillmentDelivered is analytics only")
	}
	_, err = usecases.NewMarkShipped(e.deps).Execute(context.Background(), usecases.MarkShippedCommand{AssignmentID: id, TrackingNumbers: []string{"X"}})
	e.must(err) // a repeat on a DELIVERED assignment is a no-op
	_, err = deliver("evt-d3", "T9")
	e.must(err) // DELIVERED: nothing to do
}

func TestLockShipDeliverErrors(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	_, err := usecases.NewLockAssignment(e.deps).Execute(context.Background(), usecases.LockAssignmentCommand{AssignmentID: id, ReleaseNo: 1})
	wantErr(t, err, assignment.ErrReleaseNotPending)
	_, err = usecases.NewMarkShipped(e.deps).Execute(context.Background(), usecases.MarkShippedCommand{AssignmentID: id, TrackingNumbers: []string{"T"}})
	wantErr(t, err, shared.ErrIllegalTransition)
	_, err = usecases.NewMarkDelivered(e.deps).Execute(context.Background(), usecases.MarkDeliveredCommand{AssignmentID: id, TrackingNumber: "T"})
	wantErr(t, err, shared.ErrIllegalTransition)
	_, err = usecases.NewLockAssignment(e.deps).Execute(context.Background(), usecases.LockAssignmentCommand{AssignmentID: "asg-none", ReleaseNo: 1})
	wantErr(t, err, ports.ErrNotFound)
	e.assignments.get = fault{errBoom, 1}
	_, err = usecases.NewLockAssignment(e.deps).Execute(context.Background(), usecases.LockAssignmentCommand{AssignmentID: id, ReleaseNo: 1})
	wantErr(t, err, errBoom)
	e.lock(id)
	e.outbox.fault = fault{errBoom, 9}
	before := e.store.Counts()
	_, err = usecases.NewMarkShipped(e.deps).Execute(context.Background(), usecases.MarkShippedCommand{AssignmentID: id, TrackingNumbers: []string{"T"}, ShippedAt: h(2)})
	wantErr(t, err, errBoom)
	e.wantUnchanged(before)
}

func TestRecordPickupMovedAndProcessed(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.lock(id)
	got, err := usecases.NewRecordPickupMoved(e.deps).Execute(context.Background(),
		usecases.RecordPickupMovedCommand{Meta: usecases.Meta{EventID: "evt-m", EventType: "x"}, AssignmentID: id, FromDeparture: "pk-a", ToDeparture: "pk-next"})
	e.must(err)
	if got.PickupDepartureID != "pk-next" || got.AssignmentVersion != 4 {
		t.Fatalf("view = %+v", got)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReleasedToNode", "FulfillmentLocked") // no event for a moved booking
	first, err := usecases.NewRecordProcessed(e.deps).Execute(context.Background(), usecases.Meta{EventID: "evt-f", EventType: "x"})
	e.must(err)
	again, err := usecases.NewRecordProcessed(e.deps).Execute(context.Background(), usecases.Meta{EventID: "evt-f", EventType: "x"})
	e.must(err)
	if !first || again {
		t.Fatalf("first=%v again=%v", first, again)
	}
	e.inbox.fault = fault{errBoom, 1}
	_, err = usecases.NewRecordProcessed(e.deps).Execute(context.Background(), usecases.Meta{EventID: "evt-g"})
	wantErr(t, err, errBoom)
}

func TestMutationsStampTheClock(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.clock.Advance(3 * time.Hour)
	e.release(id)
	if got := e.get(id); !got.Releases()[0].ReleasedAt.Equal(h(3)) {
		t.Fatalf("releasedAt = %v", got.Releases()[0].ReleasedAt)
	}
}
