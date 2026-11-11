package usecases_test

import (
	"context"
	"strings"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

var shopperKey = strings.Repeat("ab", 32)

// recordedConsolidation records the consolidation counters.
type recordedConsolidation struct {
	consolidated, followers, cancelRefused, stale int
	withdrawn                                     []string
}

func (r *recordedConsolidation) Consolidated(_ context.Context, followers int) {
	r.consolidated++
	r.followers += followers
}
func (r *recordedConsolidation) Withdrawn(_ context.Context, reason string) {
	r.withdrawn = append(r.withdrawn, reason)
}
func (r *recordedConsolidation) CancelRefused(context.Context) { r.cancelRefused++ }
func (r *recordedConsolidation) StaleAccept(context.Context)   { r.stale++ }

var _ ports.ConsolidationMetrics = (*recordedConsolidation)(nil)

// merging turns the policy on with the given cap and wires the counters.
func (e *env) merging(maxAssignments int) *recordedConsolidation {
	e.t.Helper()
	e.withPolicies()
	_, err := usecases.NewSourcingPolicies(e.deps).Publish(context.Background(), policy.Input{ConsolidationMaxAssignments: maxAssignments})
	e.must(err)
	m := &recordedConsolidation{}
	e.deps.ConsolidationMetrics = m
	return m
}

// queueFor queues one group of the shopper's key as order orderID.
func (e *env) queueFor(orderID, promise, key string, tier assignment.Tier) assignment.ID {
	e.t.Helper()
	cmd := placed("", group(1, promise, tier))
	cmd.OrderID, cmd.ConsolidationKey = orderID, key
	got, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), cmd)
	e.must(err)
	return assignment.ID(got[0].AssignmentID)
}

// pair queues two CONSOLIDATED groups of one shopper and releases the first, which merges both.
func (e *env) pair() (carrier, follower assignment.ID) {
	e.t.Helper()
	carrier = e.queueFor("ord-1", "prm-1", shopperKey, assignment.TierConsolidated)
	follower = e.queueFor("ord-2", "prm-2", shopperKey, assignment.TierConsolidated)
	e.release(carrier)
	return carrier, follower
}

func (e *env) count(name string) int {
	n := 0
	for _, x := range e.eventNames() {
		if x == name {
			n++
		}
	}
	return n
}

func (e *env) acceptance(id assignment.ID, evt string) usecases.LockAssignmentCommand {
	return usecases.LockAssignmentCommand{Meta: usecases.Meta{EventID: evt, EventType: "x"}, AssignmentID: id, ReleaseNo: 1, AcceptedAt: h(1)}
}

func TestReleaseMergesTheQueuedGroupsOfOneKey(t *testing.T) {
	e := newEnv(t)
	m := e.merging(4)
	carrier, follower := e.pair()
	c, f := e.get(carrier), e.get(follower)
	if c.State() != assignment.StateReleased || f.State() != assignment.StateReleased || f.ConsolidatedInto() != carrier ||
		len(c.Members()) != 1 || c.Members()[0] != follower {
		t.Fatalf("carrier %s members %v, follower %s into %q", c.State(), c.Members(), f.State(), f.ConsolidatedInto())
	}
	for name, want := range map[string]int{"FulfillmentReleasedToNode": 1, "FulfillmentSourcingDecided": 2, "AssignmentsConsolidated": 1} {
		if got := e.count(name); got != want {
			t.Errorf("%s raised %d times, want %d (events %v)", name, got, want, e.eventNames())
		}
	}
	if m.consolidated != 1 || m.followers != 1 {
		t.Fatalf("counters = %+v", m)
	}
	before := e.store.Counts() // a second timer firing for the follower finds it RELEASED
	e.release(follower)
	e.wantUnchanged(before)
}

func TestReleaseDoesNotMergeWhenARuleIsNotMet(t *testing.T) {
	pairOf := func(e *env, keyA, keyB string, tierB assignment.Tier) (assignment.ID, assignment.ID) {
		return e.queueFor("ord-1", "prm-1", keyA, assignment.TierConsolidated), e.queueFor("ord-2", "prm-2", keyB, tierB)
	}
	other := strings.Repeat("cd", 32)
	cases := map[string]func(e *env) (assignment.ID, assignment.ID){
		"policy 1 is off": func(e *env) (assignment.ID, assignment.ID) {
			e.merging(1)
			return pairOf(e, shopperKey, shopperKey, assignment.TierConsolidated)
		},
		"no policy published": func(e *env) (assignment.ID, assignment.ID) {
			return pairOf(e, shopperKey, shopperKey, assignment.TierConsolidated)
		},
		"no key": func(e *env) (assignment.ID, assignment.ID) {
			e.merging(4)
			return pairOf(e, "", "", assignment.TierConsolidated)
		},
		"a malformed key is dropped": func(e *env) (assignment.ID, assignment.ID) {
			e.merging(4)
			return pairOf(e, "NOT-A-KEY", "NOT-A-KEY", assignment.TierConsolidated)
		},
		"another key": func(e *env) (assignment.ID, assignment.ID) {
			e.merging(4)
			return pairOf(e, shopperKey, other, assignment.TierConsolidated)
		},
		"a follower of another tier": func(e *env) (assignment.ID, assignment.ID) {
			e.merging(4)
			return pairOf(e, shopperKey, shopperKey, assignment.TierStandard)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			carrier, follower := setup(e)
			e.release(carrier)
			if c, f := e.get(carrier), e.get(follower); c.Merged() || f.State() != assignment.StateQueued || c.State() != assignment.StateReleased {
				t.Fatalf("carrier %s merged=%v, follower %s", c.State(), c.Merged(), f.State())
			}
			if e.count("AssignmentsConsolidated") != 0 {
				t.Fatal("nothing merged, nothing consolidated")
			}
		})
	}
}

func TestOnlyAConsolidatedCarrierLooksForFollowers(t *testing.T) { // Q2
	for _, tier := range []assignment.Tier{assignment.TierSameDay, assignment.TierNextDay, assignment.TierTwoDay, assignment.TierStandard} {
		t.Run(string(tier), func(t *testing.T) {
			e := newEnv(t)
			e.merging(4)
			carrier := e.queueFor("ord-1", "prm-1", shopperKey, tier)
			follower := e.queueFor("ord-2", "prm-2", shopperKey, assignment.TierConsolidated)
			e.release(carrier)
			if e.get(carrier).Merged() || e.get(follower).State() != assignment.StateQueued {
				t.Fatalf("a %s carrier must not merge", tier)
			}
		})
	}
}

func TestTheCapBoundsTheMergedRelease(t *testing.T) {
	e := newEnv(t)
	e.merging(2)
	carrier := e.queueFor("ord-1", "prm-1", shopperKey, assignment.TierConsolidated)
	f1 := e.queueFor("ord-2", "prm-2", shopperKey, assignment.TierConsolidated)
	f2 := e.queueFor("ord-3", "prm-3", shopperKey, assignment.TierConsolidated)
	e.release(carrier)
	if got := e.get(carrier).Members(); len(got) != 1 || got[0] != f1 {
		t.Fatalf("a cap of 2 merges the carrier and one follower: %v", got)
	}
	if e.get(f2).State() != assignment.StateQueued {
		t.Fatalf("the third group stays QUEUED: %s", e.get(f2).State())
	}
}

func TestOneAcceptanceLocksShipsAndDeliversEveryMember(t *testing.T) {
	e := newEnv(t)
	e.merging(4)
	carrier, follower := e.pair()
	got, err := usecases.NewLockAssignment(e.deps).Execute(context.Background(), e.acceptance(carrier, "evt-l"))
	e.must(err)
	f := e.get(follower)
	if got.State != "LOCKED" || f.State() != assignment.StateLocked || f.PickupDepartureID() != got.PickupDepartureID {
		t.Fatalf("carrier %s, follower %s on %q", got.State, f.State(), f.PickupDepartureID())
	}
	locked := 0
	for _, ev := range e.store.Outbox().Events() {
		if l, ok := ev.(assignment.FulfillmentLocked); ok {
			locked++
			if len(l.MergedAssignmentIDs) != 2 || (l.SubjectID() == string(follower)) != (l.ConsolidatedInto == carrier) {
				t.Errorf("lock event = %+v", l)
			}
		}
	}
	if locked != 2 {
		t.Fatalf("FulfillmentLocked raised %d times, want 2", locked)
	}
	before := e.store.Counts()
	_, err = usecases.NewLockAssignment(e.deps).Execute(context.Background(), e.acceptance(carrier, "evt-l2"))
	e.must(err)
	if after := e.store.Counts(); after.OutboxEvents != before.OutboxEvents {
		t.Fatal("a repeated acceptance raises nothing")
	}
	_, err = usecases.NewMarkShipped(e.deps).Execute(context.Background(),
		usecases.MarkShippedCommand{Meta: usecases.Meta{EventID: "evt-s", EventType: "x"}, AssignmentID: carrier, TrackingNumbers: []string{"T1"}, ShippedAt: h(2)})
	e.must(err)
	if f := e.get(follower); f.State() != assignment.StateShipped || len(f.TrackingNumbers()) != 1 || f.TrackingNumbers()[0] != "T1" {
		t.Fatalf("follower after the carrier's shipment: %s %v", f.State(), f.TrackingNumbers())
	}
	_, err = usecases.NewMarkDelivered(e.deps).Execute(context.Background(),
		usecases.MarkDeliveredCommand{Meta: usecases.Meta{EventID: "evt-d", EventType: "x"}, AssignmentID: carrier, TrackingNumber: "T1", DeliveredAt: h(30)})
	e.must(err)
	if e.get(carrier).State() != assignment.StateDelivered || e.get(follower).State() != assignment.StateDelivered {
		t.Fatal("one PackageDelivered delivers both groups")
	}
}

func TestAFollowerNeverLocksOnItsOwnID(t *testing.T) {
	e := newEnv(t)
	e.merging(4)
	_, follower := e.pair()
	_, err := usecases.NewLockAssignment(e.deps).Execute(context.Background(), e.acceptance(follower, "evt-l"))
	wantErr(t, err, assignment.ErrReleaseNotPending)
}

func TestARefusedCarrierTakesEveryFollowerBackToTheQueue(t *testing.T) {
	e := newEnv(t)
	e.merging(4)
	carrier, follower := e.pair()
	got, err := usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), refusal(carrier, 1, assignment.OutcomeRejected))
	e.must(err)
	f := e.get(follower)
	if got.State != "QUEUED" || f.State() != assignment.StateQueued || f.ConsolidatedInto() != "" || len(e.get(carrier).Members()) != 0 {
		t.Fatalf("carrier %s, follower %s into %q", got.State, f.State(), f.ConsolidatedInto())
	}
	if got.Candidate == nil || got.Candidate.NodeID != "fc-b" || len(got.ExcludedNodes) != 1 || got.ExcludedNodes[0].NodeID != "fc-a" {
		t.Fatalf("the carrier excludes the refused node and searches again: %+v", got)
	}
	if ex := f.ExcludedNodes(); len(ex) != 1 || ex[0].NodeID != "fc-a" {
		t.Fatalf("the follower excludes it too: %v", ex)
	}
	if e.count("AssignmentsDeconsolidated") != 1 {
		t.Fatal("the dissolution is an analytics fact")
	}
}

func cancelOrder(t *testing.T, e *env, order string) assignment.State {
	t.Helper()
	cmd := cancelCmd("evt-c", "CUSTOMER_REQUEST", 1)
	cmd.OrderID = order
	got, err := usecases.NewCancelAssignment(e.deps).Execute(context.Background(), cmd)
	e.must(err)
	return assignment.State(got[0].State)
}

func lastRefusal(e *env) (reason string) {
	for _, ev := range e.store.Outbox().Events() {
		if r, ok := ev.(assignment.FulfillmentCancelRefused); ok {
			reason = r.Reason
		}
	}
	return reason
}

func TestCancelOfAMergedMemberIsRefused(t *testing.T) {
	for _, order := range []string{"ord-1", "ord-2"} { // the carrier, then the follower
		t.Run(order, func(t *testing.T) {
			e := newEnv(t)
			m := e.merging(4)
			carrier, follower := e.pair()
			if got := cancelOrder(t, e, order); got != assignment.StateReleased {
				t.Fatalf("state after the cancel = %s", got)
			}
			if lastRefusal(e) != assignment.ReasonCancelWhileMerged || m.cancelRefused != 1 {
				t.Fatalf("reason %q, counter %d", lastRefusal(e), m.cancelRefused)
			}
			if !e.get(carrier).Merged() || e.get(follower).ConsolidatedInto() != carrier {
				t.Fatal("the merge stays intact")
			}
			if e.count("FulfillmentCancelled") != 0 || e.count("FulfillmentReleaseWithdrawn") != 0 {
				t.Fatalf("nothing is cancelled or withdrawn: %v", e.eventNames())
			}
		})
	}
}

func TestCancelOfAMergedMemberIsAllowedAfterADissolution(t *testing.T) {
	e := newEnv(t)
	e.merging(4)
	carrier, _ := e.pair()
	_, err := usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), refusal(carrier, 1, assignment.OutcomeExpired))
	e.must(err)
	if got := cancelOrder(t, e, "ord-2"); got != assignment.StateCancelled {
		t.Fatalf("a dissolved member cancels as any QUEUED group: %s", got)
	}
}

func TestCancelAfterTheCarrierLockIsTheOldRefusal(t *testing.T) {
	e := newEnv(t)
	e.merging(4)
	carrier, _ := e.pair()
	_, err := usecases.NewLockAssignment(e.deps).Execute(context.Background(), e.acceptance(carrier, "evt-l"))
	e.must(err)
	if got := cancelOrder(t, e, "ord-2"); got != assignment.StateLocked || lastRefusal(e) != assignment.ReasonCancelAfterLock {
		t.Fatalf("state %s reason %q", got, lastRefusal(e))
	}
}

func TestCancelOfAnUnmergedReleasedGroupIsUnchanged(t *testing.T) {
	e := newEnv(t)
	e.merging(4)
	e.release(e.queueFor("ord-1", "prm-1", shopperKey, assignment.TierConsolidated))
	if got := cancelOrder(t, e, "ord-1"); got != assignment.StateCancelled {
		t.Fatalf("state = %s", got)
	}
}

func TestARevisionThatBreaksTheMergedWindowWithdrawsTheRelease(t *testing.T) {
	e := newEnv(t)
	m := e.merging(4)
	carrier, follower := e.pair()
	revise := usecases.ApplyRevisedPromiseCommand{Meta: usecases.Meta{EventID: "evt-rev", EventType: "x"}, PromiseID: "prm-2", RevisionNo: 1,
		DeliverByLatest: h(10), WitnessNodeID: "fc-a"} // ends before the wave the package rides
	got, err := usecases.NewApplyRevisedPromise(e.deps).Execute(context.Background(), revise)
	e.must(err)
	if got.State != "QUEUED" && got.State != "UNSOURCEABLE" {
		t.Fatalf("the revised member is searched again: %s", got.State)
	}
	if c := e.get(carrier); c.Merged() || c.State() != assignment.StateQueued {
		t.Fatalf("the carrier returns to QUEUED: %s", c.State())
	}
	if f := e.get(follower); f.ConsolidatedInto() != "" || f.State() == assignment.StateReleased {
		t.Fatalf("the follower leaves the merge: %s", f.State())
	}
	for _, want := range []string{"FulfillmentReleaseWithdrawn", "AssignmentsDeconsolidated"} {
		if e.count(want) != 1 {
			t.Errorf("%s raised %d times: %v", want, e.count(want), e.eventNames())
		}
	}
	if len(m.withdrawn) != 1 || m.withdrawn[0] != assignment.ReasonPromiseRevised {
		t.Fatalf("withdrawn = %v", m.withdrawn)
	}
	// The vendor's late acceptance of the withdrawn release finds nothing pending.
	_, err = usecases.NewLockAssignment(e.deps).Execute(context.Background(), e.acceptance(carrier, "evt-stale"))
	wantErr(t, err, assignment.ErrReleaseNotPending)
}

func TestARevisionThatKeepsTheMergedWindowKeepsTheMerge(t *testing.T) {
	e := newEnv(t)
	e.merging(4)
	carrier, _ := e.pair()
	_, err := usecases.NewApplyRevisedPromise(e.deps).Execute(context.Background(), usecases.ApplyRevisedPromiseCommand{
		Meta: usecases.Meta{EventID: "evt-rev", EventType: "x"}, PromiseID: "prm-2", RevisionNo: 1, DeliverByLatest: h(72), WitnessNodeID: "fc-a"})
	e.must(err)
	if !e.get(carrier).Merged() {
		t.Fatal("a looser promise keeps the merge")
	}
}
