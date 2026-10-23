package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func TestQueries(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	q := usecases.NewQueries(e.assignments)
	list, err := q.ListAssignments(context.Background(), repository.AssignmentFilter{State: assignment.StateQueued, NodeID: "fc-a", OrderID: "ord-1"})
	e.must(err)
	if len(list) != 1 || list[0].AssignmentID != string(id) || list[0].Releases == nil || list[0].ExcludedNodes == nil {
		t.Fatalf("list = %+v: arrays are always present", list)
	}
	list, err = q.ListAssignments(context.Background(), repository.AssignmentFilter{Limit: repository.MaxLimit})
	e.must(err)
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	none, err := q.ListAssignments(context.Background(), repository.AssignmentFilter{NodeID: "nobody"})
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("an empty result is an empty list: %v %v", none, err)
	}
	_, err = q.ListAssignments(context.Background(), repository.AssignmentFilter{State: "NOPE"})
	wantErr(t, err, shared.ErrValidation)
	_, err = q.ListAssignments(context.Background(), repository.AssignmentFilter{Limit: repository.MaxLimit + 1})
	wantErr(t, err, shared.ErrValidation)
	_, err = q.ListAssignments(context.Background(), repository.AssignmentFilter{Limit: -1})
	wantErr(t, err, shared.ErrValidation)
	e.assignments.list = fault{errBoom, 1}
	_, err = q.ListAssignments(context.Background(), repository.AssignmentFilter{})
	wantErr(t, err, errBoom)
	got, err := q.GetAssignment(context.Background(), id)
	e.must(err)
	if got.AssignmentID != string(id) || got.Candidate == nil || got.Candidate.Legs[0] != "lh-1" {
		t.Fatalf("get = %+v", got)
	}
	_, err = q.GetAssignment(context.Background(), "asg-none")
	wantErr(t, err, ports.ErrNotFound)
}

func newSweep(e *env) *usecases.SweepQueue {
	return usecases.NewSweepQueue(e.deps, usecases.NewReleaseToNode(e.deps), usecases.NewReoptimiseAssignment(e.deps))
}

func TestSweepReleasesDueAndReoptimisesTheRest(t *testing.T) {
	e := newEnv(t)
	due := e.queue() // releaseAt = cutoff h4 - 90 min = h2:30
	e.clock.Set(h(3))
	other, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), usecases.QueueAssignmentCommand{
		OrderID: "ord-2", DestinationPostalPrefix: "30301", Groups: []usecases.GroupCommand{group(1, "prm-9", assignment.TierNextDay)},
	})
	e.must(err)
	if other[0].ReleaseAt.Before(h(3)) {
		t.Fatalf("releaseAt = %v", other[0].ReleaseAt)
	}
	// Make the second group's node change so the sweep's re-optimisation has an effect.
	e.must(e.store.ReadModels().PutListing(context.Background(), listingZero("fc-a")))
	res, err := newSweep(e).Execute(context.Background(), "", 10)
	e.must(err)
	if res.Released != 2 || res.Reoptimised != 0 {
		t.Fatalf("both are due now (the clock passed releaseAt): %+v", res)
	}
	if e.get(due).State() != assignment.StateReleased {
		t.Fatal("due assignment not released")
	}
}

func queueOrder(e *env, order string) assignment.ID {
	e.t.Helper()
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), usecases.QueueAssignmentCommand{
		OrderID: order, DestinationPostalPrefix: "30301", Groups: []usecases.GroupCommand{group(1, "prm-"+order, assignment.TierNextDay)},
	})
	e.must(err)
	return assignment.ID(got[0].AssignmentID)
}

func TestSweepPagesThroughTheQueue(t *testing.T) {
	e := newEnv(t)
	for _, o := range []string{"ord-1", "ord-2", "ord-3"} {
		queueOrder(e, o)
	}
	res, err := newSweep(e).Execute(context.Background(), "", 2)
	e.must(err)
	if res.Released != 0 || res.Reoptimised != 2 || res.Next != "asg-2" {
		t.Fatalf("first page = %+v", res)
	}
	res, err = newSweep(e).Execute(context.Background(), res.Next, 2)
	e.must(err)
	if res.Reoptimised != 1 || res.Next != "" {
		t.Fatalf("last page = %+v", res)
	}
}

func TestSweepSkipsRowsAnotherWriterChangedAndCollectsOtherErrors(t *testing.T) {
	e := newEnv(t)
	queueOrder(e, "ord-1")
	e.must(e.store.ReadModels().PutListing(context.Background(), listingZero("fc-a"))) // the re-optimisation will move it
	e.assignments.save = fault{ports.ErrConcurrentUpdate, 9}
	res, err := newSweep(e).Execute(context.Background(), "", 5)
	e.must(err)
	if res.Skipped != 1 || res.Reoptimised != 0 {
		t.Fatalf("a lost race is skipped: %+v", res)
	}
	e.assignments.save = fault{}
	e.world.fault = fault{errBoom, 1}
	res, err = newSweep(e).Execute(context.Background(), "", 5)
	wantErr(t, err, errBoom)
	if res.Reoptimised != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestSweepListFailures(t *testing.T) {
	e := newEnv(t)
	queueOrder(e, "ord-1")
	e.assignments.due = fault{errBoom, 1}
	_, err := newSweep(e).Execute(context.Background(), "", 5)
	wantErr(t, err, errBoom)
	e.assignments.queued = fault{errBoom, 1}
	_, err = newSweep(e).Execute(context.Background(), "", 5)
	wantErr(t, err, errBoom)
}

func TestSweepSkipsRowsThatMovedOn(t *testing.T) {
	e := newEnv(t)
	id := queueOrder(e, "ord-1")
	e.clock.Set(h(3))
	e.release(id)
	// A stale listing of the sweep: the row is RELEASED now, the release timer finds nothing to do.
	res, err := newSweep(e).Execute(context.Background(), "", 5)
	e.must(err)
	if res.Released != 0 || res.Reoptimised != 0 {
		t.Fatalf("res = %+v", res)
	}
}

// staleQueue lists ids as QUEUED that already moved on, as a race would.
type staleQueue struct {
	ports.AssignmentRepository
	ids []assignment.ID
}

func (s staleQueue) Queued(context.Context, assignment.ID, int) ([]assignment.ID, error) {
	return s.ids, nil
}

func TestSweepSkipsAReoptimiseThatLostTheRaceToARelease(t *testing.T) {
	e := newEnv(t)
	id := queueOrder(e, "ord-1")
	e.release(id)
	e.deps.Assignments = staleQueue{AssignmentRepository: e.assignments, ids: []assignment.ID{id, "asg-ghost"}}
	res, err := newSweep(e).Execute(context.Background(), "", 5)
	e.must(err)
	if res.Skipped != 2 || res.Reoptimised != 0 {
		t.Fatalf("a locked-in or vanished row is skipped: %+v", res)
	}
}
