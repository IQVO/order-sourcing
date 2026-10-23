package assignment

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func TestReoptimiseChangesNode(t *testing.T) {
	a := mustQueue(t)
	events, err := a.Reoptimise(cand("node-b"), at(3), 0, at(1))
	wantNoErr(t, err)
	wantState(t, a, StateQueued, 2)
	wantEvents(t, events, "FulfillmentReassigned")
	wantEventVersions(t, a, events)
	r := events[0].(FulfillmentReassigned)
	if r.FromNodeID != "node-a" || r.ToNodeID != "node-b" || r.Reason != ReasonReoptimised {
		t.Fatalf("reassigned = %+v", r)
	}
	c, _ := a.Candidate()
	if c.NodeID != "node-b" || !a.ReleaseAt().Equal(at(3)) || !a.UpdatedAt().Equal(at(1)) {
		t.Fatalf("candidate %+v releaseAt %v updatedAt %v", c, a.ReleaseAt(), a.UpdatedAt())
	}
}

func TestReoptimiseSameNodeBetterPathIsSilent(t *testing.T) {
	a := mustQueue(t)
	better := cand("node-a")
	better.Cost = 90
	events, err := a.Reoptimise(better, at(2), 0, at(1))
	wantNoErr(t, err)
	wantEvents(t, events)
	wantState(t, a, StateQueued, 2)
	if c, _ := a.Candidate(); c.Cost != 90 {
		t.Fatalf("cost = %d", c.Cost)
	}
}

func TestReoptimiseUnchangedIsANoOp(t *testing.T) {
	a := mustQueue(t)
	events, err := a.Reoptimise(cand("node-a"), at(2), 0, at(1))
	wantNoErr(t, err)
	wantEvents(t, events)
	wantState(t, a, StateQueued, 1)
}

func TestReoptimiseTreatsPastReleaseAtAsEqual(t *testing.T) {
	a, _, err := Queue(newInput(), cand("node-a"), at(0), t0)
	wantNoErr(t, err)
	now := at(3)
	events, err := a.Reoptimise(cand("node-a"), now, 0, now)
	wantNoErr(t, err)
	wantEvents(t, events)
	wantState(t, a, StateQueued, 1)
	// A future releaseAt that moved is a change.
	if _, err := a.Reoptimise(cand("node-a"), at(9), 0, now); err != nil {
		t.Fatal(err)
	}
	wantState(t, a, StateQueued, 2)
	// And so is the other direction: the stored one is in the future, the new one is not.
	if _, err := a.Reoptimise(cand("node-a"), now, 0, now); err != nil {
		t.Fatal(err)
	}
	wantState(t, a, StateQueued, 3)
}

func TestReoptimiseWithoutCandidateIsUnsourceable(t *testing.T) {
	a := mustQueue(t)
	events, err := a.Reoptimise(nil, time.Time{}, 0, at(1))
	wantNoErr(t, err)
	wantState(t, a, StateUnsourceable, 2)
	wantEvents(t, events, "FulfillmentUnsourceable")
	wantEventVersions(t, a, events)
	if _, ok := a.Candidate(); ok || !a.ReleaseAt().IsZero() {
		t.Fatal("an unsourceable assignment has neither candidate nor releaseAt")
	}
}

func TestReoptimiseGuards(t *testing.T) {
	late := cand("node-b")
	late.ETA = deadline.Add(time.Minute)
	a := mustQueue(t)
	_, err := a.Reoptimise(late, at(3), 0, at(1))
	wantErr(t, err, ErrCandidateBreaksPromise)
	wantState(t, a, StateQueued, 1)

	_, err = a.Reoptimise(cand("node-b"), at(3), 7, at(1))
	wantErr(t, err, ErrStaleAssignmentVersion)
	if _, err = a.Reoptimise(cand("node-b"), at(3), 1, at(1)); err != nil {
		t.Fatalf("the current version must pass: %v", err)
	}

	excl := mustRelease(t)
	_, err = excl.RefuseRelease(1, OutcomeRejected, "OOS", cand("node-b"), at(4), at(3))
	wantNoErr(t, err)
	_, err = excl.Reoptimise(cand("node-a"), at(5), 0, at(4))
	wantErr(t, err, ErrNodeExcluded)
}

func TestReoptimiseStateErrors(t *testing.T) {
	cases := []struct {
		name string
		a    *Assignment
		want error
	}{
		{"released", mustRelease(t), ErrReleaseInFlight},
		{"locked", mustLock(t), ErrAssignmentLocked},
		{"shipped", mustShip(t, "T1"), ErrAssignmentLocked},
		{"unsourceable", func() *Assignment { a := mustQueue(t); _, _ = a.Reoptimise(nil, time.Time{}, 0, at(1)); return a }(), shared.ErrIllegalTransition},
		{"cancelled", func() *Assignment { a := mustQueue(t); _, _ = a.Cancel("X", at(1)); return a }(), shared.ErrIllegalTransition},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.a.Version()
			_, err := c.a.Reoptimise(cand("node-b"), at(3), 0, at(5))
			wantErr(t, err, c.want)
			if c.a.Version() != v {
				t.Fatal("a refused command must not change the aggregate")
			}
		})
	}
	d := mustShip(t, "T1")
	_, _ = d.MarkDelivered("T1", at(6), at(6))
	_, err := d.Reoptimise(cand("node-b"), at(3), 0, at(7))
	wantErr(t, err, ErrAssignmentLocked)
}
