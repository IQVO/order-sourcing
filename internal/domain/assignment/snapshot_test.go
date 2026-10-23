package assignment

import (
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func everyState(t *testing.T) map[State]*Assignment {
	t.Helper()
	deliveredPartial := mustShip(t, "T1", "T2")
	_, _ = deliveredPartial.MarkDelivered("T1", at(8), at(8))
	delivered := mustShip(t, "T1")
	_, _ = delivered.MarkDelivered("T1", at(8), at(8))
	cancelled := mustQueue(t)
	_, _ = cancelled.Cancel("X", at(1))
	return map[State]*Assignment{
		StateQueued: mustQueue(t), StateReleased: mustRelease(t), StateLocked: mustLock(t), StateShipped: deliveredPartial,
		StateDelivered: delivered, StateUnsourceable: unsourceable(t), StateCancelled: cancelled,
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	for state, a := range everyState(t) {
		t.Run(string(state), func(t *testing.T) {
			if a.State() != state && state != StateShipped {
				t.Fatalf("setup produced %s", a.State())
			}
			s := a.Snapshot()
			back, err := Restore(s)
			wantNoErr(t, err)
			if !reflect.DeepEqual(back.Snapshot(), s) {
				t.Fatalf("round trip differs:\n%+v\n%+v", back.Snapshot(), s)
			}
		})
	}
}

func TestSnapshotIsADeepCopy(t *testing.T) {
	a := mustRelease(t)
	s := a.Snapshot()
	s.Lines[0].Quantity = 99
	s.Releases[0].NodeID = "x"
	s.Candidate.Legs[0] = "x"
	if a.Lines()[0].Quantity == 99 || a.Releases()[0].NodeID == "x" {
		t.Fatal("snapshot aliases the aggregate")
	}
	if c, _ := a.Candidate(); c.Legs[0] == "x" {
		t.Fatal("snapshot aliases the candidate")
	}
	back, _ := Restore(a.Snapshot())
	s2 := a.Snapshot()
	s2.Candidate.Legs[0] = "y"
	if c, _ := back.Candidate(); c.Legs[0] == "y" {
		t.Fatal("restore aliases the snapshot")
	}
}

func TestRestoreRejectsCorruptSnapshots(t *testing.T) {
	type mut func(*Snapshot)
	snap := func(a *Assignment, f mut) Snapshot { s := a.Snapshot(); f(&s); return s }
	q, r, l := mustQueue(t), mustRelease(t), mustLock(t)
	sh := mustShip(t, "T1", "T2")
	_, _ = sh.MarkDelivered("T1", at(8), at(8))
	un := unsourceable(t)
	ca := mustQueue(t)
	_, _ = ca.Cancel("X", at(1))
	del := mustShip(t, "T1")
	_, _ = del.MarkDelivered("T1", at(8), at(8))
	cases := map[string]Snapshot{
		"bad input":           snap(q, func(s *Snapshot) { s.OrderID = "" }),
		"bad state":           snap(q, func(s *Snapshot) { s.State = "NOPE" }),
		"version zero":        snap(q, func(s *Snapshot) { s.Version = 0 }),
		"no queuedAt":         snap(q, func(s *Snapshot) { s.QueuedAt = time.Time{} }),
		"no updatedAt":        snap(q, func(s *Snapshot) { s.UpdatedAt = time.Time{} }),
		"release misnumbered": snap(r, func(s *Snapshot) { s.Releases[0].No = 2 }),
		"release no node":     snap(r, func(s *Snapshot) { s.Releases[0].NodeID = "" }),
		"release no instant":  snap(r, func(s *Snapshot) { s.Releases[0].ReleasedAt = time.Time{} }),
		"release no ship by":  snap(r, func(s *Snapshot) { s.Releases[0].RequiredShipBy = time.Time{} }),
		"release bad outcome": snap(r, func(s *Snapshot) { s.Releases[0].Outcome = "MAYBE" }),
		"pending not last": snap(r, func(s *Snapshot) {
			s.Releases = append(s.Releases, Release{No: 2, NodeID: "n", ReleasedAt: t0, RequiredShipBy: t0, Outcome: OutcomeAccepted})
		}),
		"excluded bad":       snap(q, func(s *Snapshot) { s.ExcludedNodes = []ExcludedNode{{NodeID: "", Reason: "X"}} }),
		"excluded no reason": snap(q, func(s *Snapshot) { s.ExcludedNodes = []ExcludedNode{{NodeID: "n", Reason: ""}} }),
		"excluded twice": snap(q, func(s *Snapshot) {
			s.ExcludedNodes = []ExcludedNode{{NodeID: "n", Reason: "X"}, {NodeID: "n", Reason: "Y"}}
		}),
		"queued no candidate":   snap(q, func(s *Snapshot) { s.Candidate = nil }),
		"queued no releaseAt":   snap(q, func(s *Snapshot) { s.ReleaseAt = time.Time{} }),
		"queued pending":        snap(r, func(s *Snapshot) { s.State = StateQueued; s.ReleaseAt = at(2) }),
		"queued eta late":       snap(q, func(s *Snapshot) { s.Candidate.ETA = deadline.Add(time.Second) }),
		"queued excluded cand":  snap(q, func(s *Snapshot) { s.ExcludedNodes = []ExcludedNode{{NodeID: "node-a", Reason: "X"}} }),
		"released no candidate": snap(r, func(s *Snapshot) { s.Candidate = nil }),
		"released releaseAt":    snap(r, func(s *Snapshot) { s.ReleaseAt = at(1) }),
		"released not pending":  snap(r, func(s *Snapshot) { s.Releases[0].Outcome = OutcomeAccepted }),
		"released other node":   snap(r, func(s *Snapshot) { s.Candidate.NodeID = "node-z" }),
		"locked not accepted":   snap(l, func(s *Snapshot) { s.Releases[0].Outcome = OutcomePending }),
		"locked no candidate":   snap(l, func(s *Snapshot) { s.Candidate = nil }),
		"locked no lockedAt":    snap(l, func(s *Snapshot) { s.LockedAt = time.Time{} }),
		"locked no reason":      snap(l, func(s *Snapshot) { s.LockReason = "" }),
		"locked no pickup":      snap(l, func(s *Snapshot) { s.PickupDepartureID = "" }),
		"locked releaseAt":      snap(l, func(s *Snapshot) { s.ReleaseAt = at(1) }),
		"locked tracking":       snap(l, func(s *Snapshot) { s.TrackingNumbers = []string{"T"} }),
		"locked shippedAt":      snap(l, func(s *Snapshot) { s.ShippedAt = at(1) }),
		"locked delivered":      snap(l, func(s *Snapshot) { s.Delivered = []DeliveredPackage{{TrackingNumber: "T", DeliveredAt: at(1)}} }),
		"locked deliveredAt":    snap(l, func(s *Snapshot) { s.DeliveredAt = at(1) }),
		"shipped no tracking":   snap(sh, func(s *Snapshot) { s.TrackingNumbers = nil }),
		"shipped no shippedAt":  snap(sh, func(s *Snapshot) { s.ShippedAt = time.Time{} }),
		"delivered foreign":     snap(sh, func(s *Snapshot) { s.Delivered[0].TrackingNumber = "T9" }),
		"delivered no instant":  snap(sh, func(s *Snapshot) { s.Delivered[0].DeliveredAt = time.Time{} }),
		"shipped all delivered": snap(sh, func(s *Snapshot) {
			s.Delivered = append(s.Delivered, DeliveredPackage{TrackingNumber: "T2", DeliveredAt: at(9)})
		}),
		"shipped has deliveredAt": snap(sh, func(s *Snapshot) { s.DeliveredAt = at(9) }),
		"delivered partial":       snap(del, func(s *Snapshot) { s.TrackingNumbers = []string{"T1", "T2"} }),
		"delivered no instant at": snap(del, func(s *Snapshot) { s.DeliveredAt = time.Time{} }),
		"unsourceable candidate":  snap(un, func(s *Snapshot) { s.Candidate = cand("node-a") }),
		"unsourceable releaseAt":  snap(un, func(s *Snapshot) { s.ReleaseAt = at(1) }),
		"cancelled candidate":     snap(ca, func(s *Snapshot) { s.Candidate = cand("node-a") }),
		"cancelled releaseAt":     snap(ca, func(s *Snapshot) { s.ReleaseAt = at(1) }),
		"cancelled no reason":     snap(ca, func(s *Snapshot) { s.CancelReason = "" }),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Restore(s)
			wantErr(t, err, shared.ErrInvalidSnapshot)
		})
	}
}
