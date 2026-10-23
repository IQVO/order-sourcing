package assignment

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func TestReleaseSameNode(t *testing.T) {
	a := mustQueue(t)
	events, err := a.Release(cand("node-a"), releaseLines(), at(2))
	wantNoErr(t, err)
	wantState(t, a, StateReleased, 2)
	wantEvents(t, events, "FulfillmentReleasedToNode")
	wantEventVersions(t, a, events)
	wantReleaseEvent(t, events[0])
	wantReleaseRecorded(t, a)
}

func wantReleaseEvent(t *testing.T, ev shared.Event) {
	t.Helper()
	e := ev.(FulfillmentReleasedToNode)
	if e.ReleaseNo != 1 || e.NodeID != "node-a" || e.PickupDepartureID != "dep-node-a" || !e.RequiredShipBy.Equal(at(5)) ||
		!e.PromisedDeliverByLatest.Equal(deadline) || e.Tier != TierTwoDay || len(e.Lines) != 2 || e.Lines[0].ListingID != "lst-1" ||
		e.OrderID != "ord-1" || e.GroupNo != 1 {
		t.Fatalf("release event = %+v", e)
	}
}

func wantReleaseRecorded(t *testing.T, a *Assignment) {
	t.Helper()
	rels := a.Releases()
	if len(rels) != 1 || rels[0].Outcome != OutcomePending || rels[0].No != 1 || !rels[0].ReleasedAt.Equal(at(2)) || !rels[0].RequiredShipBy.Equal(at(5)) {
		t.Fatalf("releases = %+v", rels)
	}
	if !a.ReleaseAt().IsZero() || a.Lines()[0].ListingID != "lst-1" {
		t.Fatal("releaseAt is cleared and lines carry the listing")
	}
}

func TestReleaseRecheckPicksAnotherNode(t *testing.T) {
	a := mustQueue(t)
	events, err := a.Release(cand("node-b"), releaseLines(), at(2))
	wantNoErr(t, err)
	wantState(t, a, StateReleased, 2)
	wantEvents(t, events, "FulfillmentReassigned", "FulfillmentReleasedToNode")
	wantEventVersions(t, a, events)
	r := events[0].(FulfillmentReassigned)
	if r.FromNodeID != "node-a" || r.ToNodeID != "node-b" || r.Reason != ReasonReleaseRecheck {
		t.Fatalf("reassigned = %+v", r)
	}
}

func TestReleaseWithoutCandidateIsUnsourceable(t *testing.T) {
	a := mustQueue(t)
	events, err := a.Release(nil, nil, at(2))
	wantNoErr(t, err)
	wantState(t, a, StateUnsourceable, 2)
	wantEvents(t, events, "FulfillmentUnsourceable")
	if len(a.Releases()) != 0 {
		t.Fatal("no release was made")
	}
}

func TestReleaseGuards(t *testing.T) {
	late := cand("node-a")
	late.ETA = deadline.Add(time.Second)
	a := mustQueue(t)
	_, err := a.Release(late, releaseLines(), at(2))
	wantErr(t, err, ErrCandidateBreaksPromise)

	noListing := releaseLines()
	noListing[1].ListingID = ""
	short := releaseLines()[:1]
	otherItem := releaseLines()
	otherItem[0].CatalogItemID = "cat-9"
	otherQty := releaseLines()
	otherQty[0].Quantity = 3
	otherNo := releaseLines()
	otherNo[0].LineNo = 5
	for name, lines := range map[string][]Line{"no listing": noListing, "short": short, "other item": otherItem, "other quantity": otherQty, "other line": otherNo, "none": nil} {
		t.Run(name, func(t *testing.T) {
			_, err := a.Release(cand("node-a"), lines, at(2))
			wantErr(t, err, shared.ErrValidation)
			wantState(t, a, StateQueued, 1)
		})
	}
}

func TestReleaseStateErrors(t *testing.T) {
	r := mustRelease(t)
	_, err := r.Release(cand("node-a"), releaseLines(), at(3))
	wantErr(t, err, ErrReleaseInFlight)
	l := mustLock(t)
	_, err = l.Release(cand("node-a"), releaseLines(), at(3))
	wantErr(t, err, ErrAssignmentLocked)
}

func TestRefuseReleaseRejectedRequeues(t *testing.T) {
	a := mustRelease(t)
	events, err := a.RefuseRelease(1, OutcomeRejected, "OUT_OF_STOCK", cand("node-b"), at(4), at(3))
	wantNoErr(t, err)
	wantState(t, a, StateQueued, 3)
	wantEvents(t, events, "FulfillmentReassigned")
	wantEventVersions(t, a, events)
	r := events[0].(FulfillmentReassigned)
	if r.FromNodeID != "node-a" || r.ToNodeID != "node-b" || r.Reason != ReasonPurchaseOrderRejected {
		t.Fatalf("reassigned = %+v", r)
	}
	rel := a.Releases()[0]
	if rel.Outcome != OutcomeRejected || rel.ReasonCode != "OUT_OF_STOCK" {
		t.Fatalf("release = %+v", rel)
	}
	ex := a.ExcludedNodes()
	if len(ex) != 1 || ex[0].NodeID != "node-a" || ex[0].Reason != "REJECTED:OUT_OF_STOCK" {
		t.Fatalf("excluded = %+v", ex)
	}
	if !a.ReleaseAt().Equal(at(4)) {
		t.Fatalf("releaseAt = %v", a.ReleaseAt())
	}
}

func TestRefuseReleaseExpiredWithoutCandidateIsUnsourceable(t *testing.T) {
	a := mustRelease(t)
	events, err := a.RefuseRelease(1, OutcomeExpired, "", nil, time.Time{}, at(3))
	wantNoErr(t, err)
	wantState(t, a, StateUnsourceable, 3)
	wantEvents(t, events, "FulfillmentUnsourceable")
	u := events[0].(FulfillmentUnsourceable)
	if u.Reason != ReasonNodeRefused || len(u.ExcludedNodes) != 1 || u.ExcludedNodes[0].Reason != "EXPIRED" {
		t.Fatalf("unsourceable = %+v", u)
	}
	if a.Releases()[0].Outcome != OutcomeExpired {
		t.Fatal("outcome must be EXPIRED")
	}
}

func TestRefuseReleaseRejectedWithoutReasonCode(t *testing.T) {
	a := mustRelease(t)
	_, err := a.RefuseRelease(1, OutcomeRejected, "", cand("node-b"), at(4), at(3))
	wantNoErr(t, err)
	if a.ExcludedNodes()[0].Reason != "REJECTED" {
		t.Fatalf("reason = %q", a.ExcludedNodes()[0].Reason)
	}
}

func TestRefuseReleaseExpiredReassignReason(t *testing.T) {
	a := mustRelease(t)
	events, err := a.RefuseRelease(1, OutcomeExpired, "", cand("node-b"), at(4), at(3))
	wantNoErr(t, err)
	if events[0].(FulfillmentReassigned).Reason != ReasonPurchaseOrderExpired {
		t.Fatal("reason must be PURCHASE_ORDER_EXPIRED")
	}
}

func TestRefuseReleaseGuards(t *testing.T) {
	a := mustRelease(t)
	_, err := a.RefuseRelease(1, OutcomeAccepted, "", cand("node-b"), at(4), at(3))
	wantErr(t, err, shared.ErrValidation)
	_, err = a.RefuseRelease(1, OutcomeRejected, "bad\tcode", cand("node-b"), at(4), at(3))
	wantErr(t, err, shared.ErrValidation)
	_, err = a.RefuseRelease(2, OutcomeRejected, "", cand("node-b"), at(4), at(3))
	wantErr(t, err, ErrReleaseNotPending)
	// The same node again is excluded, and a failed refusal leaves the aggregate untouched.
	_, err = a.RefuseRelease(1, OutcomeRejected, "X", cand("node-a"), at(4), at(3))
	wantErr(t, err, ErrNodeExcluded)
	wantState(t, a, StateReleased, 2)
	if a.Releases()[0].Outcome != OutcomePending || a.Releases()[0].ReasonCode != "" || len(a.ExcludedNodes()) != 0 {
		t.Fatal("a refused refusal must roll back")
	}
	late := cand("node-b")
	late.ETA = deadline.Add(time.Second)
	_, err = a.RefuseRelease(1, OutcomeRejected, "X", late, at(4), at(3))
	wantErr(t, err, ErrCandidateBreaksPromise)
}

func TestRefuseReleaseStateErrors(t *testing.T) {
	_, err := mustQueue(t).RefuseRelease(1, OutcomeRejected, "", nil, time.Time{}, at(3))
	wantErr(t, err, ErrReleaseNotPending)
	_, err = mustLock(t).RefuseRelease(1, OutcomeRejected, "", nil, time.Time{}, at(3))
	wantErr(t, err, ErrAssignmentLocked)
	done := mustRelease(t)
	_, _ = done.RefuseRelease(1, OutcomeRejected, "", cand("node-b"), at(4), at(3))
	_, err = done.RefuseRelease(1, OutcomeRejected, "", cand("node-c"), at(4), at(3))
	wantErr(t, err, ErrReleaseNotPending)
}

func TestSecondReleaseIsNumberedTwo(t *testing.T) {
	a := mustRelease(t)
	_, _ = a.RefuseRelease(1, OutcomeRejected, "", cand("node-b"), at(4), at(3))
	events, err := a.Release(cand("node-b"), releaseLines(), at(4))
	wantNoErr(t, err)
	if e := events[0].(FulfillmentReleasedToNode); e.ReleaseNo != 2 || e.NodeID != "node-b" {
		t.Fatalf("second release = %+v", e)
	}
	if len(a.Releases()) != 2 {
		t.Fatal("two releases expected")
	}
}
