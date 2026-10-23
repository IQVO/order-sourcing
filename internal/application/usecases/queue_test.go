package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func TestQueueAssignmentQueuesEachGroup(t *testing.T) {
	e := newEnv(t)
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(),
		placed("evt-1", group(1, "prm-1", assignment.TierNextDay), group(2, "prm-2", assignment.TierStandard)))
	e.must(err)
	if len(got) != 2 || got[0].State != "QUEUED" || got[1].State != "QUEUED" || got[0].AssignmentVersion != 1 {
		t.Fatalf("views = %+v", got)
	}
	v := got[0]
	if v.Candidate == nil || v.Candidate.NodeID != "fc-a" || v.Candidate.Cost != 250 || v.ReleaseAt.IsZero() || v.Promise.PromiseID != "prm-1" ||
		v.DestinationPostalPrefix != "30301" || v.OrderID != "ord-1" || len(v.Lines) != 1 {
		t.Fatalf("view = %+v", v)
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentQueued")
	for _, ev := range e.store.Outbox().Events() {
		if ev.Stream() != shared.StreamAnalytics {
			t.Fatal("FulfillmentQueued is analytics only")
		}
	}
}

func TestQueueAssignmentIsIdempotent(t *testing.T) {
	e := newEnv(t)
	uc := usecases.NewQueueAssignment(e.deps)
	_, err := uc.Execute(context.Background(), placed("evt-1", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	before := e.store.Counts()
	got, err := uc.Execute(context.Background(), placed("evt-1", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	if got != nil {
		t.Fatalf("a repeated event id does nothing: %+v", got)
	}
	got, err = uc.Execute(context.Background(), placed("evt-2", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	if len(got) != 0 {
		t.Fatalf("an existing group is skipped: %+v", got)
	}
	after := e.store.Counts()
	if after.Assignments != before.Assignments || after.OutboxEvents != before.OutboxEvents || after.InboxRows != before.InboxRows+1 {
		t.Fatalf("counts %+v -> %+v", before, after)
	}
}

func TestQueueAssignmentSameDayReleasesAtOnce(t *testing.T) {
	e := newEnv(t)
	n := e.must
	rm := e.store.ReadModels()
	node, _ := rm.GetNode(context.Background(), "fc-a")
	node.Subtype = "SUB_SAME_DAY"
	n(rm.PutNode(context.Background(), node))
	n(rm.PutDeparture(context.Background(), directDeparture()))
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), placed("evt-1", group(1, "prm-1", assignment.TierSameDay)))
	e.must(err)
	if got[0].State != "RELEASED" || got[0].AssignmentVersion != 2 || len(got[0].Releases) != 1 || got[0].Lines[0].ListingID != "lst-1-fc-a" {
		t.Fatalf("view = %+v", got[0])
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentReleasedToNode")
}

func TestQueueAssignmentWithoutCandidateIsUnsourceable(t *testing.T) {
	e := newEnv(t)
	cmd := placed("evt-1", group(1, "prm-1", assignment.TierNextDay))
	cmd.DestinationPostalPrefix = "99999"
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), cmd)
	e.must(err)
	if got[0].State != "UNSOURCEABLE" || got[0].Candidate != nil || got[0].AssignmentVersion != 1 {
		t.Fatalf("view = %+v", got[0])
	}
	wantNames(t, e.eventNames(), "FulfillmentQueued", "FulfillmentUnsourceable")
}

func TestQueueAssignmentFailuresChangeNothing(t *testing.T) {
	bad := group(2, "prm-2", assignment.TierNextDay)
	bad.Tier = "WEEKLY"
	cases := map[string]struct {
		prepare func(*env)
		cmd     usecases.QueueAssignmentCommand
		want    error
	}{
		"invalid second group": {func(*env) {}, placed("evt-1", group(1, "prm-1", assignment.TierNextDay), bad), shared.ErrValidation},
		"outbox down":          {func(e *env) { e.outbox.fault = fault{errBoom, 9} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), errBoom},
		"read models down":     {func(e *env) { e.world.fault = fault{errBoom, 9} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), errBoom},
		"inbox down":           {func(e *env) { e.inbox.fault = fault{errBoom, 9} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), errBoom},
		"save down":            {func(e *env) { e.assignments.save = fault{errBoom, 9} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), errBoom},
		"duplicate group":      {func(e *env) { e.assignments.save = fault{ports.ErrDuplicateGroup, 9} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), ports.ErrDuplicateGroup},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c.prepare(e)
			before := e.store.Counts()
			_, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), c.cmd)
			wantErr(t, err, c.want)
			e.wantUnchanged(before)
		})
	}
}

func TestQueueAssignmentRetriesOnceOnAConcurrentUpdate(t *testing.T) {
	e := newEnv(t)
	e.assignments.save = fault{ports.ErrConcurrentUpdate, 1}
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), placed("evt-1", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	if len(got) != 1 || e.store.Counts().Assignments != 1 {
		t.Fatalf("the retry must queue once: %+v", got)
	}
	e2 := newEnv(t)
	e2.assignments.save = fault{ports.ErrConcurrentUpdate, 2}
	_, err = usecases.NewQueueAssignment(e2.deps).Execute(context.Background(), placed("evt-1", group(1, "prm-1", assignment.TierNextDay)))
	wantErr(t, err, ports.ErrConcurrentUpdate)
}
