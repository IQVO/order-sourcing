package assignment

import (
	"strings"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func TestLock(t *testing.T) {
	a := mustRelease(t)
	events, err := a.Lock(1, at(3), at(4))
	wantNoErr(t, err)
	wantState(t, a, StateLocked, 3)
	wantEvents(t, events, "FulfillmentLocked")
	wantEventVersions(t, a, events)
	e := events[0].(FulfillmentLocked)
	if e.NodeID != "node-a" || e.PickupDepartureID != "dep-node-a" || !e.LockedAt.Equal(at(3)) || e.LockReason != LockReasonAccepted ||
		e.PromiseID != "prm-1" || e.OrderID != "ord-1" || e.GroupNo != 1 {
		t.Fatalf("locked = %+v", e)
	}
	if a.Releases()[0].Outcome != OutcomeAccepted || !a.LockedAt().Equal(at(3)) || a.LockReason() != LockReasonAccepted || a.PickupDepartureID() != "dep-node-a" {
		t.Fatal("lock facts not recorded")
	}
}

func TestLockUsesNowWhenAcceptedAtIsZero(t *testing.T) {
	a := mustRelease(t)
	_, err := a.Lock(1, time.Time{}, at(4))
	wantNoErr(t, err)
	if !a.LockedAt().Equal(at(4)) {
		t.Fatalf("lockedAt = %v", a.LockedAt())
	}
}

func TestLockIsIdempotentForTheSameRelease(t *testing.T) {
	for name, a := range map[string]*Assignment{"locked": mustLock(t), "shipped": mustShip(t, "T1")} {
		t.Run(name, func(t *testing.T) {
			v := a.Version()
			events, err := a.Lock(1, at(3), at(9))
			wantNoErr(t, err)
			wantEvents(t, events)
			if a.Version() != v {
				t.Fatal("a repeat must not change the version")
			}
			_, err = a.Lock(2, at(3), at(9))
			wantErr(t, err, ErrAssignmentLocked)
		})
	}
}

func TestLockErrors(t *testing.T) {
	_, err := mustRelease(t).Lock(2, at(3), at(4))
	wantErr(t, err, ErrReleaseNotPending)
	_, err = mustQueue(t).Lock(1, at(3), at(4))
	wantErr(t, err, ErrReleaseNotPending)
	c := mustQueue(t)
	_, _ = c.Cancel("X", at(1))
	_, err = c.Lock(1, at(3), at(4))
	wantErr(t, err, shared.ErrIllegalTransition)
	rel := mustRelease(t)
	_, _ = rel.Cancel("X", at(3))
	_, err = rel.Lock(1, at(3), at(4))
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestMarkShipped(t *testing.T) {
	a := mustLock(t)
	events, err := a.MarkShipped([]string{"T1", "T2"}, at(5), at(6))
	wantNoErr(t, err)
	wantState(t, a, StateShipped, 4)
	wantEvents(t, events, "FulfillmentShipped")
	wantEventVersions(t, a, events)
	e := events[0].(FulfillmentShipped)
	if e.NodeID != "node-a" || len(e.TrackingNumbers) != 2 || !e.ShippedAt.Equal(at(5)) || e.OrderID != "ord-1" || e.GroupNo != 1 {
		t.Fatalf("shipped = %+v", e)
	}
	if len(a.TrackingNumbers()) != 2 || !a.ShippedAt().Equal(at(5)) {
		t.Fatal("shipment not recorded")
	}
}

func TestMarkShippedDefaultsAndRepeats(t *testing.T) {
	a := mustLock(t)
	_, err := a.MarkShipped([]string{"T1"}, time.Time{}, at(6))
	wantNoErr(t, err)
	if !a.ShippedAt().Equal(at(6)) {
		t.Fatalf("shippedAt = %v", a.ShippedAt())
	}
	events, err := a.MarkShipped([]string{"T1"}, at(7), at(8))
	wantNoErr(t, err)
	wantEvents(t, events)
	wantState(t, a, StateShipped, 4)
}

func TestMarkShippedErrors(t *testing.T) {
	_, err := mustRelease(t).MarkShipped([]string{"T1"}, at(5), at(6))
	wantErr(t, err, shared.ErrIllegalTransition)
	a := mustLock(t)
	many := make([]string, MaxTrackingNos+1)
	for i := range many {
		many[i] = "T"
	}
	for _, tn := range [][]string{nil, many, {" "}, {strings.Repeat("x", MaxTrackingLen+1)}} {
		_, err := a.MarkShipped(tn, at(5), at(6))
		wantErr(t, err, shared.ErrValidation)
	}
	ok := many[:MaxTrackingNos]
	if _, err := a.MarkShipped(ok, at(5), at(6)); err != nil {
		t.Fatalf("MaxTrackingNos tracking numbers are allowed: %v", err)
	}
	b := mustLock(t)
	if _, err := b.MarkShipped([]string{strings.Repeat("x", MaxTrackingLen)}, at(5), at(6)); err != nil {
		t.Fatalf("a tracking number of MaxTrackingLen is allowed: %v", err)
	}
}

func TestMarkDeliveredWaitsForTheLastPackage(t *testing.T) {
	a := mustShip(t, "T1", "T2")
	events, err := a.MarkDelivered("T1", at(10), at(11))
	wantNoErr(t, err)
	wantEvents(t, events)
	wantState(t, a, StateShipped, 5)
	if len(a.DeliveredPackages()) != 1 || !a.DeliveredAt().IsZero() {
		t.Fatal("partial delivery is recorded without deliveredAt")
	}
	events, err = a.MarkDelivered("T1", at(12), at(12))
	wantNoErr(t, err)
	wantEvents(t, events)
	wantState(t, a, StateShipped, 5)
	events, err = a.MarkDelivered("T2", at(13), at(14))
	wantNoErr(t, err)
	wantState(t, a, StateDelivered, 6)
	wantEvents(t, events, "FulfillmentDelivered")
	wantEventVersions(t, a, events)
	d := events[0].(FulfillmentDelivered)
	if !d.DeliveredAt.Equal(at(13)) || !a.DeliveredAt().Equal(at(13)) || events[0].Stream() != shared.StreamAnalytics || d.OrderID != "ord-1" || d.GroupNo != 1 {
		t.Fatalf("delivered = %+v", d)
	}
	events, err = a.MarkDelivered("T2", at(15), at(15))
	wantNoErr(t, err)
	wantEvents(t, events)
	wantState(t, a, StateDelivered, 6)
}

func TestMarkDeliveredDefaultsToNowAndRejects(t *testing.T) {
	a := mustShip(t, "T1")
	_, err := a.MarkDelivered("T9", at(10), at(11))
	wantErr(t, err, shared.ErrValidation)
	_, err = a.MarkDelivered("T1", time.Time{}, at(11))
	wantNoErr(t, err)
	if !a.DeliveredAt().Equal(at(11)) {
		t.Fatalf("deliveredAt = %v", a.DeliveredAt())
	}
	_, err = mustLock(t).MarkDelivered("T1", at(10), at(11))
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestCancel(t *testing.T) {
	for name, a := range map[string]*Assignment{
		"queued": mustQueue(t), "released": mustRelease(t),
		"unsourceable": func() *Assignment { a := mustQueue(t); _, _ = a.Reoptimise(nil, time.Time{}, 0, at(1)); return a }(),
	} {
		t.Run(name, func(t *testing.T) {
			v := a.Version()
			events, err := a.Cancel("CUSTOMER_REQUEST", at(3))
			wantNoErr(t, err)
			wantState(t, a, StateCancelled, v+1)
			wantEvents(t, events, "FulfillmentCancelled")
			wantEventVersions(t, a, events)
			if c := events[0].(FulfillmentCancelled); c.Reason != "CUSTOMER_REQUEST" || c.OrderID != "ord-1" || c.GroupNo != 1 {
				t.Fatalf("cancelled = %+v", c)
			}
			if _, ok := a.Candidate(); ok || !a.ReleaseAt().IsZero() || a.CancelReason() != "CUSTOMER_REQUEST" {
				t.Fatal("cancel clears the candidate and keeps the reason")
			}
			events, err = a.Cancel("AGAIN", at(4))
			wantNoErr(t, err)
			wantEvents(t, events)
			wantState(t, a, StateCancelled, v+1)
		})
	}
}

func TestCancelAfterLockAndRefusal(t *testing.T) {
	for name, a := range map[string]*Assignment{"locked": mustLock(t), "shipped": mustShip(t, "T1")} {
		t.Run(name, func(t *testing.T) {
			_, err := a.Cancel("X", at(9))
			wantErr(t, err, ErrCancelAfterLock)
			v := a.Version()
			events, err := a.RefuseCancel(at(9))
			wantNoErr(t, err)
			wantState(t, a, a.State(), v+1)
			wantEvents(t, events, "FulfillmentCancelRefused")
			wantEventVersions(t, a, events)
			if r := events[0].(FulfillmentCancelRefused); r.Reason != ReasonCancelAfterLock || r.OrderID != "ord-1" || r.GroupNo != 1 {
				t.Fatalf("refused = %+v", r)
			}
		})
	}
	d := mustShip(t, "T1")
	_, _ = d.MarkDelivered("T1", at(9), at(9))
	_, err := d.Cancel("X", at(10))
	wantErr(t, err, ErrCancelAfterLock)
	if _, err := d.RefuseCancel(at(10)); err != nil {
		t.Fatalf("a delivered group refuses a cancel too: %v", err)
	}
	_, err = mustQueue(t).RefuseCancel(at(1))
	wantErr(t, err, shared.ErrIllegalTransition)
	_, err = mustQueue(t).Cancel("", at(1))
	wantErr(t, err, shared.ErrValidation)
	_, err = mustQueue(t).Cancel(strings.Repeat("x", MaxReason+1), at(1))
	wantErr(t, err, shared.ErrValidation)
	if _, err := mustQueue(t).Cancel(strings.Repeat("x", MaxReason), at(1)); err != nil {
		t.Fatalf("a reason of MaxReason is allowed: %v", err)
	}
}

func TestMovePickup(t *testing.T) {
	a := mustLock(t)
	wantNoErr(t, a.MovePickup("dep-node-a", "dep-next", at(5)))
	wantState(t, a, StateLocked, 4)
	if a.PickupDepartureID() != "dep-next" {
		t.Fatalf("pickup = %s", a.PickupDepartureID())
	}
	if c, _ := a.Candidate(); c.PickupDepartureID != "dep-next" {
		t.Fatal("the candidate follows the booking")
	}
	// To the current one, or from a stale one: no change.
	wantNoErr(t, a.MovePickup("dep-node-a", "dep-next", at(6)))
	wantNoErr(t, a.MovePickup("dep-old", "dep-other", at(6)))
	wantState(t, a, StateLocked, 4)
	// Without from: moves.
	wantNoErr(t, a.MovePickup("", "dep-last", at(7)))
	wantState(t, a, StateLocked, 5)
	wantErr(t, a.MovePickup("", "", at(7)), shared.ErrValidation)
	wantErr(t, a.MovePickup("-x", "dep-y", at(7)), shared.ErrValidation)
	wantErr(t, mustRelease(t).MovePickup("", "dep-y", at(7)), shared.ErrIllegalTransition)
}

func TestCurrentNodeID(t *testing.T) {
	if got := mustQueue(t).CurrentNodeID(); got != "node-a" {
		t.Fatalf("queued: %q", got)
	}
	if got := mustLock(t).CurrentNodeID(); got != "node-a" {
		t.Fatalf("locked: %q", got)
	}
	u := unsourceable(t)
	if got := u.CurrentNodeID(); got != "node-a" {
		t.Fatalf("unsourceable after a release keeps the last released node: %q", got)
	}
	fresh, _, err := Queue(newInput(), nil, time.Time{}, t0)
	wantNoErr(t, err)
	if got := fresh.CurrentNodeID(); got != "" {
		t.Fatalf("unsourceable without a release: %q", got)
	}
}
