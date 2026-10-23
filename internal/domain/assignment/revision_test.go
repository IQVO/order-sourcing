package assignment

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var later = deadline.Add(48 * time.Hour)

func rev(no int) Revision {
	return Revision{RevisionNo: no, DeliverByLatest: later, WitnessNodeID: "node-w"}
}

func unsourceable(t *testing.T) *Assignment {
	t.Helper()
	a := mustRelease(t)
	_, err := a.RefuseRelease(1, OutcomeRejected, "X", nil, time.Time{}, at(3))
	wantNoErr(t, err)
	return a
}

func TestReviseUnsourceableRequeues(t *testing.T) {
	a := unsourceable(t)
	events, err := a.ApplyRevisedPromise(rev(1), cand("node-a"), at(6), at(4))
	wantNoErr(t, err)
	wantState(t, a, StateQueued, 4)
	wantEvents(t, events, "FulfillmentConstraintRevised")
	wantEventVersions(t, a, events)
	r := events[0].(FulfillmentConstraintRevised)
	if r.RevisionNo != 1 || !r.DeliverByLatest.Equal(later) {
		t.Fatalf("revised = %+v", r)
	}
	p := a.Promise()
	if p.RevisionNo != 1 || !p.DeliverByLatest.Equal(later) || p.WitnessNodeID != "node-w" || len(a.ExcludedNodes()) != 0 {
		t.Fatalf("promise %+v excluded %v: exclusions reset and constraint replaced", p, a.ExcludedNodes())
	}
	if c, ok := a.Candidate(); !ok || c.NodeID != "node-a" || !a.ReleaseAt().Equal(at(6)) {
		t.Fatal("the previously excluded node is a candidate again")
	}
}

func TestReviseUnsourceableWithoutCandidateStaysUnsourceable(t *testing.T) {
	a := unsourceable(t)
	events, err := a.ApplyRevisedPromise(rev(1), nil, time.Time{}, at(4))
	wantNoErr(t, err)
	wantState(t, a, StateUnsourceable, 4)
	wantEvents(t, events, "FulfillmentConstraintRevised")
}

func TestReviseQueued(t *testing.T) {
	a := mustQueue(t)
	events, err := a.ApplyRevisedPromise(Revision{RevisionNo: 2, DeliverByLatest: later}, cand("node-b"), at(6), at(4))
	wantNoErr(t, err)
	wantState(t, a, StateQueued, 2)
	wantEvents(t, events, "FulfillmentConstraintRevised", "FulfillmentReassigned")
	wantEventVersions(t, a, events)
	if r := events[1].(FulfillmentReassigned); r.Reason != ReasonPromiseRevised || r.FromNodeID != "node-a" || r.ToNodeID != "node-b" {
		t.Fatalf("reassigned = %+v", r)
	}
	if a.Promise().WitnessNodeID != "node-a" {
		t.Fatal("an absent witness keeps the witness")
	}
	same, err := a.ApplyRevisedPromise(Revision{RevisionNo: 3, DeliverByLatest: later}, cand("node-b"), at(6), at(5))
	wantNoErr(t, err)
	wantEvents(t, same, "FulfillmentConstraintRevised")
}

func TestReviseQueuedWithoutCandidateBecomesUnsourceable(t *testing.T) {
	a := mustQueue(t)
	events, err := a.ApplyRevisedPromise(rev(1), nil, time.Time{}, at(4))
	wantNoErr(t, err)
	wantState(t, a, StateUnsourceable, 2)
	wantEvents(t, events, "FulfillmentConstraintRevised", "FulfillmentUnsourceable")
	wantEventVersions(t, a, events)
}

func TestReviseCommittedStatesChangeOnlyTheConstraint(t *testing.T) {
	for name, a := range map[string]*Assignment{"released": mustRelease(t), "locked": mustLock(t), "shipped": mustShip(t, "T1")} {
		t.Run(name, func(t *testing.T) {
			s, v := a.State(), a.Version()
			cBefore, _ := a.Candidate()
			events, err := a.ApplyRevisedPromise(rev(1), cand("node-z"), at(9), at(8))
			wantNoErr(t, err)
			wantState(t, a, s, v+1)
			wantEvents(t, events, "FulfillmentConstraintRevised")
			if cAfter, _ := a.Candidate(); !cAfter.Same(cBefore) {
				t.Fatal("the candidate must not move once released")
			}
			if a.Promise().RevisionNo != 1 {
				t.Fatal("revision not applied")
			}
		})
	}
}

func TestReviseResetsExclusionsOnlyWhenRevisionApplies(t *testing.T) {
	a := mustRelease(t)
	_, _ = a.RefuseRelease(1, OutcomeRejected, "X", cand("node-b"), at(4), at(3))
	if len(a.ExcludedNodes()) != 1 {
		t.Fatal("setup")
	}
	_, err := a.ApplyRevisedPromise(rev(0), nil, time.Time{}, at(5))
	wantErr(t, err, ErrStaleRevision)
	if len(a.ExcludedNodes()) != 1 {
		t.Fatal("a stale revision must keep the exclusions")
	}
	_, err = a.ApplyRevisedPromise(rev(1), cand("node-a"), at(6), at(5))
	wantNoErr(t, err)
	if len(a.ExcludedNodes()) != 0 {
		t.Fatal("exclusions reset with the revision")
	}
	_, err = a.ApplyRevisedPromise(rev(1), cand("node-a"), at(6), at(6))
	wantErr(t, err, ErrStaleRevision)
}

func TestReviseGuards(t *testing.T) {
	d := mustShip(t, "T1")
	_, _ = d.MarkDelivered("T1", at(9), at(9))
	_, err := d.ApplyRevisedPromise(rev(1), nil, time.Time{}, at(10))
	wantErr(t, err, shared.ErrIllegalTransition)
	c := mustQueue(t)
	_, _ = c.Cancel("X", at(1))
	_, err = c.ApplyRevisedPromise(rev(1), nil, time.Time{}, at(10))
	wantErr(t, err, shared.ErrIllegalTransition)

	a := mustQueue(t)
	_, err = a.ApplyRevisedPromise(Revision{RevisionNo: 1}, cand("node-a"), at(6), at(4))
	wantErr(t, err, shared.ErrValidation)
	_, err = a.ApplyRevisedPromise(Revision{RevisionNo: 1, DeliverByLatest: later, WitnessNodeID: "-w"}, cand("node-a"), at(6), at(4))
	wantErr(t, err, shared.ErrValidation)
	// A candidate that breaks the NEW deadline leaves the aggregate untouched.
	early := Revision{RevisionNo: 1, DeliverByLatest: at(10)}
	_, err = a.ApplyRevisedPromise(early, cand("node-a"), at(6), at(4))
	wantErr(t, err, ErrCandidateBreaksPromise)
	wantState(t, a, StateQueued, 1)
	if a.Promise().RevisionNo != 0 || !a.Promise().DeliverByLatest.Equal(deadline) {
		t.Fatal("a refused revision must roll back")
	}
}

func TestReviseRollsBackExclusionsOnAGuardFailure(t *testing.T) {
	a := unsourceable(t)
	_, err := a.ApplyRevisedPromise(Revision{RevisionNo: 1, DeliverByLatest: at(10)}, cand("node-a"), at(6), at(4))
	wantErr(t, err, ErrCandidateBreaksPromise)
	if len(a.ExcludedNodes()) != 1 {
		t.Fatal("exclusions must be restored")
	}
}
