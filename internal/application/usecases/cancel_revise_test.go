package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func cancelCmd(evt, reason string, groups ...int) usecases.CancelAssignmentCommand {
	return usecases.CancelAssignmentCommand{Meta: usecases.Meta{EventID: evt, EventType: "x"}, OrderID: "ord-1", GroupNos: groups, Reason: reason}
}

func TestCancelAssignmentBeforeTheLock(t *testing.T) {
	e := newEnv(t)
	e.queue()
	got, err := usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-1", "CUSTOMER_REQUEST", 1, 7))
	e.must(err)
	if len(got) != 1 || got[0].State != "CANCELLED" || got[0].CancelReason != "CUSTOMER_REQUEST" || got[0].Candidate != nil {
		t.Fatalf("views = %+v (group 7 was never queued: skipped)", got)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentCancelled")
}

func TestCancelAssignmentDefaultReasonAndRepeat(t *testing.T) {
	e := newEnv(t)
	e.queue()
	got, err := usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-1", "", 1))
	e.must(err)
	if got[0].CancelReason != usecases.DefaultCancelReason {
		t.Fatalf("reason = %q", got[0].CancelReason)
	}
	before := e.store.Counts()
	_, err = usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-2", "AGAIN", 1))
	e.must(err)
	if after := e.store.Counts(); after.OutboxEvents != before.OutboxEvents || after.VersionSum != before.VersionSum {
		t.Fatal("cancelling a CANCELLED assignment changes nothing")
	}
}

func TestCancelAssignmentAfterTheLockIsRefused(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.lock(id)
	got, err := usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-1", "CUSTOMER_REQUEST", 1))
	e.must(err)
	if got[0].State != "LOCKED" || got[0].AssignmentVersion != 4 {
		t.Fatalf("a refused cancel keeps the state and bumps the version: %+v", got[0])
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReleasedToNode", "FulfillmentLocked", "FulfillmentCancelRefused")
}

func TestCancelAssignmentFailures(t *testing.T) {
	e := newEnv(t)
	e.queue()
	e.assignments.save = fault{errBoom, 9}
	before := e.store.Counts()
	_, err := usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-1", "R", 1))
	wantErr(t, err, errBoom)
	e.wantUnchanged(before)
	e.assignments.save = fault{}
	_, err = usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-2", "bad\treason", 1))
	wantErr(t, err, shared.ErrValidation)
	e.inbox.fault = fault{errBoom, 1}
	_, err = usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-3", "R", 1))
	wantErr(t, err, errBoom)
	unknown, err := usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cancelCmd("evt-5", "R"))
	e.must(err)
	if len(unknown) != 0 {
		t.Fatal("no groups, no work")
	}
	e2 := newEnv(t)
	e2.queue()
	e2.deps.Assignments = brokenFind{AssignmentRepository: e2.assignments}
	_, err = usecases.NewCancelAssignment(e2.deps).Execute(context.Background(), cancelCmd("evt-4", "R", 1))
	wantErr(t, err, errBoom)
}

type brokenFind struct{ ports.AssignmentRepository }

func (brokenFind) FindByGroup(context.Context, string, int) (*assignment.Assignment, error) {
	return nil, errBoom
}

func (brokenFind) FindByPromise(context.Context, string) (*assignment.Assignment, error) {
	return nil, errBoom
}

func revise(evt string, no int, deadline time.Time, witness string) usecases.ApplyRevisedPromiseCommand {
	return usecases.ApplyRevisedPromiseCommand{
		Meta: usecases.Meta{EventID: evt, EventType: "x"}, PromiseID: "prm-1", RevisionNo: no, DeliverByLatest: deadline, WitnessNodeID: witness,
	}
}

func TestApplyRevisedPromiseRequeuesAnUnsourceableGroup(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.release(id)
	_, err := usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), refusal(id, 1, assignment.OutcomeRejected))
	e.must(err)
	e.must(e.store.ReadModels().PutListing(context.Background(), listingZero("fc-b")))
	_, err = usecases.NewReleaseToNode(e.deps).Execute(context.Background(), id)
	e.must(err)
	if e.get(id).State() != assignment.StateUnsourceable {
		t.Fatalf("setup: state = %s", e.get(id).State())
	}
	e.must(e.store.ReadModels().PutListing(context.Background(), listingQty("fc-a", 5)))
	got, err := usecases.NewApplyRevisedPromise(e.deps).Execute(context.Background(), revise("evt-r", 1, h(72), "fc-a"))
	e.must(err)
	if got.State != "QUEUED" || got.Promise.RevisionNo != 1 || len(got.ExcludedNodes) != 0 || got.Candidate.NodeID != "fc-a" {
		t.Fatalf("view = %+v: the revision resets the exclusions, so fc-a is a candidate again", got)
	}
	names := e.eventNames()
	if names[len(names)-1] != "FulfillmentConstraintRevised" {
		t.Fatalf("events = %v", names)
	}
}

func TestApplyRevisedPromiseInOtherStates(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	got, err := usecases.NewApplyRevisedPromise(e.deps).Execute(context.Background(), revise("evt-1", 1, h(72), ""))
	e.must(err)
	if got.State != "QUEUED" || got.Promise.RevisionNo != 1 || got.Promise.WitnessNodeID != "fc-a" {
		t.Fatalf("queued: %+v", got)
	}
	e.lock(id)
	got, err = usecases.NewApplyRevisedPromise(e.deps).Execute(context.Background(), revise("evt-2", 2, h(80), "fc-b"))
	e.must(err)
	if got.State != "LOCKED" || got.Promise.RevisionNo != 2 || got.Promise.WitnessNodeID != "fc-b" {
		t.Fatalf("locked: %+v", got)
	}
}

func TestApplyRevisedPromiseIgnoresStaleAndRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	e.queue()
	uc := usecases.NewApplyRevisedPromise(e.deps)
	_, err := uc.Execute(context.Background(), revise("evt-1", 1, h(72), ""))
	e.must(err)
	before := e.store.Counts()
	got, err := uc.Execute(context.Background(), revise("evt-2", 1, h(90), ""))
	e.must(err)
	if got.Promise.RevisionNo != 1 {
		t.Fatalf("a stale revision is ignored: %+v", got)
	}
	if after := e.store.Counts(); after.OutboxEvents != before.OutboxEvents || after.VersionSum != before.VersionSum {
		t.Fatal("a stale revision writes nothing")
	}
	_, err = uc.Execute(context.Background(), revise("evt-3", 2, time.Time{}, ""))
	wantErr(t, err, shared.ErrValidation)
	missing := revise("evt-4", 2, h(72), "")
	missing.PromiseID = "prm-none"
	_, err = uc.Execute(context.Background(), missing)
	wantErr(t, err, ports.ErrNotFound)
	e.world.fault = fault{errBoom, 1}
	_, err = uc.Execute(context.Background(), revise("evt-5", 2, h(72), ""))
	wantErr(t, err, errBoom)
	e.inbox.fault = fault{errBoom, 1}
	_, err = uc.Execute(context.Background(), revise("evt-6", 2, h(72), ""))
	wantErr(t, err, errBoom)
	e.outbox.fault = fault{errBoom, 9}
	_, err = uc.Execute(context.Background(), revise("evt-7", 2, h(72), ""))
	wantErr(t, err, errBoom)
	e.outbox.fault = fault{}
	_, err = uc.Execute(context.Background(), revise("evt-8", 2, h(1), "")) // earlier than any eta: no candidate keeps the promise
	e.must(err)
	if e.get("asg-1").State() != assignment.StateUnsourceable {
		t.Fatalf("state = %s", e.get("asg-1").State())
	}
}

func TestApplyRevisedPromiseOnADeliveredGroupIsRefused(t *testing.T) {
	e := newEnv(t)
	id := e.queue()
	e.lock(id)
	_, err := usecases.NewMarkShipped(e.deps).Execute(context.Background(), usecases.MarkShippedCommand{AssignmentID: id, TrackingNumbers: []string{"T"}, ShippedAt: h(2)})
	e.must(err)
	_, err = usecases.NewMarkDelivered(e.deps).Execute(context.Background(), usecases.MarkDeliveredCommand{AssignmentID: id, TrackingNumber: "T", DeliveredAt: h(3)})
	e.must(err)
	_, err = usecases.NewApplyRevisedPromise(e.deps).Execute(context.Background(), revise("evt-1", 1, h(72), ""))
	wantErr(t, err, shared.ErrIllegalTransition)
}
