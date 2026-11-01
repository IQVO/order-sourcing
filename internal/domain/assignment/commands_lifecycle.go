package assignment

import (
	"fmt"
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Lock applies the node's acceptance of release no (LockAssignment). A repeat
// on an assignment already locked by that release returns no events.
func (a *Assignment) Lock(no int, acceptedAt, now time.Time) ([]shared.Event, error) {
	if a.state.Committed() {
		if last, ok := a.lastRelease(); ok && last.No == no && last.Outcome == OutcomeAccepted {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: assignment %s is %s", ErrAssignmentLocked, a.id, a.state)
	}
	if a.state == StateCancelled {
		return nil, fmt.Errorf("%w: assignment %s is CANCELLED", shared.ErrIllegalTransition, a.id)
	}
	rel, err := a.pendingRelease(no)
	if err != nil {
		return nil, err
	}
	rel.Outcome = OutcomeAccepted
	a.state = StateLocked
	a.lockedAt = shared.UTC(acceptedAt)
	if a.lockedAt.IsZero() {
		a.lockedAt = shared.UTC(now)
	}
	a.lockReason = LockReasonAccepted
	a.pickupDeparture = a.candidate.PickupDepartureID
	a.touch(now)
	return []shared.Event{FulfillmentLocked{
		Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, PromiseID: a.promise.PromiseID, NodeID: rel.NodeID,
		PickupDepartureID: a.pickupDeparture, LockedAt: a.lockedAt, LockReason: a.lockReason,
	}}, nil
}

// MarkShipped records the vendor's shipment (LOCKED to SHIPPED). A repeat on a
// SHIPPED or DELIVERED assignment returns no events.
func (a *Assignment) MarkShipped(trackingNumbers []string, shippedAt, now time.Time) ([]shared.Event, error) {
	switch a.state {
	case StateShipped, StateDelivered:
		return nil, nil
	case StateLocked:
	default:
		return nil, fmt.Errorf("%w: assignment %s is %s, not LOCKED", shared.ErrIllegalTransition, a.id, a.state)
	}
	if len(trackingNumbers) < 1 || len(trackingNumbers) > MaxTrackingNos {
		return nil, shared.Invalid("a shipment has 1 to %d tracking numbers", MaxTrackingNos)
	}
	for _, t := range trackingNumbers {
		if !shared.ValidTextN(t, MaxTrackingLen) {
			return nil, shared.Invalid("tracking number %q is not valid", t)
		}
	}
	a.state = StateShipped
	a.trackingNumbers = slices.Clone(trackingNumbers)
	a.shippedAt = shared.UTC(shippedAt)
	if a.shippedAt.IsZero() {
		a.shippedAt = shared.UTC(now)
	}
	a.touch(now)
	return []shared.Event{FulfillmentShipped{
		Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, NodeID: a.releases[len(a.releases)-1].NodeID,
		TrackingNumbers: slices.Clone(a.trackingNumbers), ShippedAt: a.shippedAt,
	}}, nil
}

func (a *Assignment) deliveredTracking(tn string) bool {
	return slices.ContainsFunc(a.delivered, func(p DeliveredPackage) bool { return p.TrackingNumber == tn })
}

// MarkDelivered records the delivery of one package of a SHIPPED group. The
// group is DELIVERED when its last package is delivered (ADR 0002 A8); before
// that only the progress is saved. A repeat returns no events.
func (a *Assignment) MarkDelivered(trackingNumber string, deliveredAt, now time.Time) ([]shared.Event, error) {
	switch a.state {
	case StateDelivered:
		return nil, nil
	case StateShipped:
	default:
		return nil, fmt.Errorf("%w: assignment %s is %s, not SHIPPED", shared.ErrIllegalTransition, a.id, a.state)
	}
	if !slices.Contains(a.trackingNumbers, trackingNumber) {
		return nil, shared.Invalid("tracking number %q is not on the shipment of %s", trackingNumber, a.id)
	}
	if a.deliveredTracking(trackingNumber) {
		return nil, nil
	}
	at := shared.UTC(deliveredAt)
	if at.IsZero() {
		at = shared.UTC(now)
	}
	a.delivered = append(a.delivered, DeliveredPackage{TrackingNumber: trackingNumber, DeliveredAt: at})
	a.touch(now)
	if len(a.delivered) < len(a.trackingNumbers) {
		return nil, nil
	}
	a.state = StateDelivered
	a.deliveredAt = at
	return []shared.Event{FulfillmentDelivered{Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, DeliveredAt: at}}, nil
}

// Cancel cancels a QUEUED, RELEASED or UNSOURCEABLE assignment
// (CancelAssignment; UNSOURCEABLE since G10). LOCKED and later is
// ErrCancelAfterLock; a repeat on CANCELLED returns no events. A PENDING
// release stays as it is: vendor-connectivity cancels the purchase order.
func (a *Assignment) Cancel(reason string, now time.Time) ([]shared.Event, error) {
	switch a.state {
	case StateCancelled:
		return nil, nil
	case StateLocked, StateShipped, StateDelivered:
		return nil, fmt.Errorf("%w: assignment %s is %s", ErrCancelAfterLock, a.id, a.state)
	}
	if !shared.ValidTextN(reason, MaxReason) {
		return nil, shared.Invalid("reason must be 1 to %d printable characters", MaxReason)
	}
	a.state = StateCancelled
	a.cancelReason = reason
	a.candidate = nil
	a.releaseAt = time.Time{}
	a.touch(now)
	return []shared.Event{FulfillmentCancelled{Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, Reason: reason}}, nil
}

// RefuseCancel answers a cancel request that came after the lock
// (contracts-delta E11): the state does not change, the version does.
func (a *Assignment) RefuseCancel(now time.Time) ([]shared.Event, error) {
	if !a.state.Committed() {
		return nil, fmt.Errorf("%w: assignment %s is %s, a cancel is not refused", shared.ErrIllegalTransition, a.id, a.state)
	}
	a.touch(now)
	return []shared.Event{FulfillmentCancelRefused{Base: a.base(), OrderID: a.orderID, GroupNo: a.groupNo, Reason: ReasonCancelAfterLock}}, nil
}
