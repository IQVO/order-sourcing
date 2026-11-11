package assignment

import (
	"strings"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var mergeKey = strings.Repeat("ab", 32)

// queueMember queues a CONSOLIDATED assignment of the shared key whose
// delivery window is [at(24), deadline].
func queueMember(t *testing.T, id ID, group int, lines []Line) *Assignment {
	t.Helper()
	in := newInput()
	in.ID, in.GroupNo, in.Lines, in.ConsolidationKey = id, group, lines, mergeKey
	in.Promise = PromiseConstraint{
		PromiseID: "prm-" + string(id), Tier: TierConsolidated, DeliverByEarliest: at(24), DeliverByLatest: deadline, WitnessNodeID: "node-a",
	}
	a, _, err := Queue(in, cand("node-a"), at(2), t0)
	wantNoErr(t, err)
	return a
}

func resolved(lines []Line, listing string) []Line {
	out := make([]Line, len(lines))
	for i, l := range lines {
		l.ListingID = listing
		out[i] = l
	}
	return out
}

func oneLine(item string, qty int) []Line {
	return []Line{{LineNo: 1, CatalogItemID: item, Quantity: qty}}
}

// mergedPair releases a carrier (asg-1) with one follower (asg-2) and returns both.
func mergedPair(t *testing.T) (carrier, follower *Assignment, events []shared.Event) {
	t.Helper()
	carrier = queueMember(t, "asg-1", 1, oneLine("cat-1", 2))
	follower = queueMember(t, "asg-2", 1, oneLine("cat-1", 3))
	fm := MergedMember{ID: "asg-2", OrderID: "ord-1", GroupNo: 1, PromiseID: "prm-asg-2", DeliverByLatest: deadline.Add(-time.Hour),
		Lines: resolved(follower.Lines(), "lst-1")}
	events, err := carrier.ReleaseConsolidated(cand("node-a"), resolved(carrier.Lines(), "lst-1"), []MergedMember{fm}, facts(), at(2))
	wantNoErr(t, err)
	fe, err := follower.ReleaseAsFollower("asg-1", cand("node-a"), fm.Lines, 2, facts(), at(2))
	wantNoErr(t, err)
	wantEvents(t, fe, "FulfillmentSourcingDecided")
	return carrier, follower, events
}

func TestReleaseConsolidatedRaisesTheMergedFacts(t *testing.T) {
	carrier, follower, events := mergedPair(t)
	wantEvents(t, events, "FulfillmentReleasedToNode", "FulfillmentSourcingDecided", "AssignmentsConsolidated")
	wantState(t, carrier, StateReleased, 2)
	wantState(t, follower, StateReleased, 2)
	if got := carrier.Members(); len(got) != 1 || got[0] != "asg-2" || follower.ConsolidatedInto() != "asg-1" || len(follower.Members()) != 0 {
		t.Fatalf("members %v, follower of %q", got, follower.ConsolidatedInto())
	}
	wantEventVersions(t, carrier, events)
	if got := events[1].(FulfillmentSourcingDecided); got.ConsolidatedCount != 2 || got.CarrierAssignmentID != "" || got.Units != 2 {
		t.Fatalf("carrier decision %+v", got)
	}
	if got := events[2].(AssignmentsConsolidated); got.Units() != 5 || got.NodeID != "node-a" || got.ReleaseNo != 1 {
		t.Fatalf("consolidated %+v", got)
	}
}

func TestMergedReleaseCarriesTheUnionOfTheLines(t *testing.T) {
	_, _, events := mergedPair(t)
	rel := events[0].(FulfillmentReleasedToNode)
	if rel.Tier != TierStandard || len(rel.Members) != 2 || rel.Members[0].ID != "asg-1" || rel.Members[1].ID != "asg-2" {
		t.Fatalf("tier %s members %+v", rel.Tier, rel.Members)
	}
	if len(rel.Lines) != 2 || len(rel.LineMembers) != 2 || rel.LineMembers[0] != "asg-1" || rel.LineMembers[1] != "asg-2" {
		t.Fatalf("lines %+v owners %v", rel.Lines, rel.LineMembers)
	}
	if !rel.PromisedDeliverByLatest.Equal(deadline.Add(-time.Hour)) {
		t.Fatalf("promisedDeliverByLatest = %v, want the minimum over the members", rel.PromisedDeliverByLatest)
	}
}

func TestFollowerDecisionNamesTheCarrier(t *testing.T) {
	_, follower, _ := mergedPair(t)
	rels := follower.Releases()
	if len(rels) != 1 || rels[0].Outcome != OutcomePending {
		t.Fatalf("follower releases = %+v", rels)
	}
	other := queueMember(t, "asg-3", 2, oneLine("cat-1", 1))
	events, err := other.ReleaseAsFollower("asg-1", cand("node-a"), resolved(other.Lines(), "lst-1"), 3, facts(), at(2))
	wantNoErr(t, err)
	got := events[0].(FulfillmentSourcingDecided)
	if got.CarrierAssignmentID != "asg-1" || got.ConsolidatedCount != 3 {
		t.Fatalf("decision = %+v", got)
	}
}

func TestReleaseConsolidatedRefusesBadInput(t *testing.T) {
	ok := MergedMember{ID: "asg-2", Lines: resolved(oneLine("cat-1", 1), "lst-1")}
	many := make([]Line, MaxLines)
	for i := range many {
		many[i] = Line{LineNo: i, CatalogItemID: "cat-1", Quantity: 1}
	}
	cases := map[string][]MergedMember{
		"no follower":     nil,
		"the carrier":     {{ID: "asg-1", Lines: ok.Lines}},
		"a duplicate":     {ok, ok},
		"a bad id":        {{ID: "", Lines: ok.Lines}},
		"too many lines":  {{ID: "asg-2", Lines: many}},
		"a second follow": {ok, {ID: "asg-3", Lines: many}},
	}
	for name, followers := range cases {
		t.Run(name, func(t *testing.T) {
			a := queueMember(t, "asg-1", 1, oneLine("cat-1", 2))
			_, err := a.ReleaseConsolidated(cand("node-a"), resolved(a.Lines(), "lst-1"), followers, facts(), at(2))
			wantErr(t, err, shared.ErrValidation)
			wantState(t, a, StateQueued, 1)
		})
	}
	t.Run("no key", func(t *testing.T) {
		a := mustQueue(t)
		_, err := a.ReleaseConsolidated(cand("node-a"), releaseLines(), []MergedMember{ok}, facts(), at(2))
		wantErr(t, err, shared.ErrValidation)
	})
	t.Run("no candidate", func(t *testing.T) {
		a := queueMember(t, "asg-1", 1, oneLine("cat-1", 2))
		_, err := a.ReleaseConsolidated(nil, nil, []MergedMember{ok}, facts(), at(2))
		wantErr(t, err, shared.ErrValidation)
	})
}

func TestReleaseAsFollowerRefusesBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		carrier ID
		count   int
	}{"itself": {"asg-2", 2}, "a bad carrier": {"", 2}, "one member": {"asg-1", 1}} {
		t.Run(name, func(t *testing.T) {
			f := queueMember(t, "asg-2", 1, oneLine("cat-1", 1))
			_, err := f.ReleaseAsFollower(tc.carrier, cand("node-a"), resolved(f.Lines(), "lst-1"), tc.count, facts(), at(2))
			wantErr(t, err, shared.ErrValidation)
		})
	}
	t.Run("no key", func(t *testing.T) {
		a := mustQueue(t)
		_, err := a.ReleaseAsFollower("asg-9", cand("node-a"), releaseLines(), 2, facts(), at(2))
		wantErr(t, err, shared.ErrValidation)
	})
}

func TestMergedLockShipDeliverEveryMember(t *testing.T) {
	carrier, follower, _ := mergedPair(t)
	ids := carrier.MergedAssignmentIDs()
	if len(ids) != 2 || ids[0] != "asg-1" || ids[1] != "asg-2" {
		t.Fatalf("merged ids = %v", ids)
	}
	ce, err := carrier.Lock(1, at(3), at(3))
	wantNoErr(t, err)
	fe, err := follower.LockFollower(ids, at(3), at(3))
	wantNoErr(t, err)
	if got := ce[0].(FulfillmentLocked); got.ConsolidatedInto != "" || len(got.MergedAssignmentIDs) != 2 {
		t.Fatalf("carrier lock = %+v", got)
	}
	if got := fe[0].(FulfillmentLocked); got.ConsolidatedInto != "asg-1" || len(got.MergedAssignmentIDs) != 2 || got.NodeID != "node-a" || got.PickupDepartureID != "dep-node-a" {
		t.Fatalf("follower lock = %+v", got)
	}
	again, err := follower.LockFollower(ids, at(3), at(4))
	wantNoErr(t, err)
	wantEvents(t, again)
	for _, a := range []*Assignment{carrier, follower} {
		se, err := a.MarkShipped([]string{"trk-1"}, at(4), at(4))
		wantNoErr(t, err)
		wantEvents(t, se, "FulfillmentShipped")
		de, err := a.MarkDelivered("trk-1", at(30), at(30))
		wantNoErr(t, err)
		wantEvents(t, de, "FulfillmentDelivered")
		wantState(t, a, StateDelivered, a.Version())
	}
}

func TestFollowerLocksOnlyWithItsCarrier(t *testing.T) {
	_, follower, _ := mergedPair(t)
	_, err := follower.Lock(1, at(3), at(3))
	wantErr(t, err, ErrReleaseNotPending)
	other := mustLock(t)
	_, err = other.LockFollower(nil, at(3), at(3))
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestLockFollowerNeedsAPendingRelease(t *testing.T) {
	_, follower, _ := mergedPair(t)
	f2 := queueMember(t, "asg-5", 1, oneLine("cat-1", 1))
	_, err := f2.LockFollower([]ID{"asg-1", "asg-5"}, at(3), at(3))
	wantErr(t, err, shared.ErrIllegalTransition) // not a follower yet
	if _, err := follower.LockFollower([]ID{"asg-1", "asg-2"}, at(3), at(3)); err != nil {
		t.Fatalf("follower lock: %v", err)
	}
	if _, err := follower.MarkShipped([]string{"t"}, at(4), at(4)); err != nil {
		t.Fatal(err)
	}
	_, err = follower.LockFollower([]ID{"asg-1", "asg-2"}, at(3), at(5))
	wantNoErr(t, err)
}

func TestCancelOfAMergedMemberIsRefusedAndTheMergeStays(t *testing.T) {
	for _, name := range []string{"carrier", "follower"} {
		t.Run(name, func(t *testing.T) {
			carrier, follower, _ := mergedPair(t)
			member := map[string]*Assignment{"carrier": carrier, "follower": follower}[name]
			_, err := member.Cancel("customer", at(3))
			wantErr(t, err, ErrCancelWhileMerged)
			wantState(t, member, StateReleased, 2)
			events, err := member.RefuseCancel(at(3))
			wantNoErr(t, err)
			wantEvents(t, events, "FulfillmentCancelRefused")
			if got := events[0].(FulfillmentCancelRefused); got.Reason != ReasonCancelWhileMerged {
				t.Fatalf("reason = %s", got.Reason)
			}
			wantState(t, member, StateReleased, 3)
			if !member.Merged() {
				t.Fatal("the merge must stay after a refused cancel")
			}
		})
	}
}

func TestCancelAfterTheLockStillRefusesAsBefore(t *testing.T) {
	carrier, _, _ := mergedPair(t)
	_, err := carrier.Lock(1, at(3), at(3))
	wantNoErr(t, err)
	_, err = carrier.Cancel("customer", at(4))
	wantErr(t, err, ErrCancelAfterLock)
	events, err := carrier.RefuseCancel(at(4))
	wantNoErr(t, err)
	if got := events[0].(FulfillmentCancelRefused); got.Reason != ReasonCancelAfterLock {
		t.Fatalf("reason = %s", got.Reason)
	}
}

func TestCancelOfAnUnmergedReleasedGroupIsUnchanged(t *testing.T) {
	a := mustRelease(t)
	events, err := a.Cancel("customer", at(3))
	wantNoErr(t, err)
	wantEvents(t, events, "FulfillmentCancelled")
	r := mustRelease(t)
	_, err = r.RefuseCancel(at(3))
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestCancelIsAllowedAfterTheMergeDissolved(t *testing.T) {
	carrier, follower, _ := mergedPair(t)
	d := Dissolution{Reason: ReasonPromiseRevised, TriggerID: "asg-2", MemberIDs: []ID{"asg-1", "asg-2"}}
	_, err := carrier.Requeue(cand("node-a"), at(40), d, at(5))
	wantNoErr(t, err)
	_, err = follower.Requeue(cand("node-a"), at(40), d, at(5))
	wantNoErr(t, err)
	for _, a := range []*Assignment{carrier, follower} {
		events, err := a.Cancel("customer", at(6))
		wantNoErr(t, err)
		wantEvents(t, events, "FulfillmentCancelled")
	}
}

func TestRequeueWithdrawsTheMerge(t *testing.T) {
	carrier, follower, _ := mergedPair(t)
	d := Dissolution{Reason: ReasonPromiseRevised, TriggerID: "asg-2", MemberIDs: []ID{"asg-1", "asg-2"}}
	moved := cand("node-b")
	events, err := carrier.Requeue(moved, at(40), d, at(5))
	wantNoErr(t, err)
	wantEvents(t, events, "FulfillmentReleaseWithdrawn", "FulfillmentReassigned", "AssignmentsDeconsolidated")
	wantState(t, carrier, StateQueued, 3)
	wantEventVersions(t, carrier, events)
	if rels := carrier.Releases(); rels[0].Outcome != OutcomeWithdrawn {
		t.Fatalf("release outcome = %s", rels[0].Outcome)
	}
	w := events[0].(FulfillmentReleaseWithdrawn)
	if w.ReleaseNo != 1 || w.Reason != ReasonPromiseRevised || w.OrderID != "ord-1" {
		t.Fatalf("withdrawn = %+v", w)
	}
	if r := events[1].(FulfillmentReassigned); r.Reason != ReasonConsolidationWithdrawn || r.FromNodeID != "node-a" || r.ToNodeID != "node-b" {
		t.Fatalf("reassigned = %+v", r)
	}
	if got := events[2].(AssignmentsDeconsolidated); got.TriggerAssignmentID != "asg-2" || len(got.MemberAssignmentIDs) != 2 || got.Reason != ReasonPromiseRevised {
		t.Fatalf("deconsolidated = %+v", got)
	}
	fe, err := follower.Requeue(cand("node-a"), at(40), d, at(5))
	wantNoErr(t, err)
	wantEvents(t, fe) // same node, and only the carrier raises the merge facts
	if follower.Merged() || carrier.Merged() {
		t.Fatal("the merge links must be cleared")
	}
}

func TestRequeueWithNoCandidateIsUnsourceable(t *testing.T) {
	carrier, _, _ := mergedPair(t)
	events, err := carrier.Requeue(nil, time.Time{}, Dissolution{Reason: ReasonPromiseRevised, MemberIDs: []ID{"asg-1", "asg-2"}}, at(5))
	wantNoErr(t, err)
	wantEvents(t, events, "FulfillmentReleaseWithdrawn", "FulfillmentUnsourceable", "AssignmentsDeconsolidated")
	wantState(t, carrier, StateUnsourceable, 3)
}

func TestRequeueRefusesWhatIsNotAReleasedMember(t *testing.T) {
	_, err := mustRelease(t).Requeue(cand("node-a"), at(40), Dissolution{}, at(5))
	wantErr(t, err, shared.ErrIllegalTransition)
	_, err = mustQueue(t).Requeue(cand("node-a"), at(40), Dissolution{}, at(5))
	wantErr(t, err, shared.ErrIllegalTransition)
	carrier, _, _ := mergedPair(t)
	late := cand("node-a")
	late.ETA = deadline.Add(time.Hour)
	_, err = carrier.Requeue(late, at(40), Dissolution{}, at(5))
	wantErr(t, err, ErrCandidateBreaksPromise)
	wantState(t, carrier, StateReleased, 2)
}

func TestRefusalDissolvesTheMerge(t *testing.T) {
	carrier, follower, _ := mergedPair(t)
	events, err := carrier.RefuseRelease(1, OutcomeRejected, "STOCKOUT", cand("node-b"), at(40), at(5))
	wantNoErr(t, err)
	wantEvents(t, events, "FulfillmentReassigned", "AssignmentsDeconsolidated")
	got := events[1].(AssignmentsDeconsolidated)
	if got.Reason != ReasonPurchaseOrderRejected || got.TriggerAssignmentID != "" || len(got.MemberAssignmentIDs) != 2 || got.MemberAssignmentIDs[0] != "asg-1" {
		t.Fatalf("deconsolidated = %+v", got)
	}
	fe, err := follower.RefuseRelease(1, OutcomeRejected, "STOCKOUT", cand("node-b"), at(40), at(5))
	wantNoErr(t, err)
	wantEvents(t, fe, "FulfillmentReassigned")
	if carrier.Merged() || follower.Merged() {
		t.Fatal("a refusal must clear the merge links")
	}
	wantState(t, follower, StateQueued, 3)
}

func TestRefusalWithNoCandidateLeftStillEndsTheMerge(t *testing.T) {
	carrier, _, _ := mergedPair(t)
	events, err := carrier.RefuseRelease(1, OutcomeExpired, "", nil, time.Time{}, at(5))
	wantNoErr(t, err)
	wantEvents(t, events, "FulfillmentUnsourceable", "AssignmentsDeconsolidated")
	if got := events[1].(AssignmentsDeconsolidated); got.Reason != ReasonPurchaseOrderExpired {
		t.Fatalf("reason = %s", got.Reason)
	}
}

func TestMergeFieldsRoundTripThroughASnapshot(t *testing.T) {
	carrier, follower, _ := mergedPair(t)
	for _, a := range []*Assignment{carrier, follower} {
		back, err := Restore(a.Snapshot())
		wantNoErr(t, err)
		if back.ConsolidationKey() != mergeKey || back.ConsolidatedInto() != a.ConsolidatedInto() || len(back.Members()) != len(a.Members()) {
			t.Fatalf("round trip lost the merge fields of %s", a.ID())
		}
		if !back.Promise().DeliverByEarliest.Equal(at(24)) {
			t.Fatalf("deliverByEarliest = %v", back.Promise().DeliverByEarliest)
		}
	}
}

func TestRestoreRefusesBrokenMergeSnapshots(t *testing.T) {
	cases := map[string]func(c, f *Snapshot){
		"a bad key":                   func(c, _ *Snapshot) { c.ConsolidationKey = "XYZ" },
		"an upper-case key":           func(c, _ *Snapshot) { c.ConsolidationKey = strings.Repeat("AB", 32) },
		"a follower with members":     func(_, f *Snapshot) { f.Members = []ID{"asg-7"} },
		"a follower of itself":        func(_, f *Snapshot) { f.ConsolidatedInto = f.ID },
		"a follower with no key":      func(_, f *Snapshot) { f.ConsolidationKey = "" },
		"a carrier with no key":       func(c, _ *Snapshot) { c.ConsolidationKey = "" },
		"members not ascending":       func(c, _ *Snapshot) { c.Members = []ID{"asg-3", "asg-2"} },
		"members with the carrier":    func(c, _ *Snapshot) { c.Members = []ID{"asg-1"} },
		"a bad member id":             func(c, _ *Snapshot) { c.Members = []ID{""} },
		"earliest after latest":       func(c, _ *Snapshot) { c.Promise.DeliverByEarliest = c.Promise.DeliverByLatest.Add(time.Hour) },
		"a queued carrier with links": func(c, _ *Snapshot) { c.State = StateQueued },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			carrier, follower, _ := mergedPair(t)
			c, f := carrier.Snapshot(), follower.Snapshot()
			mutate(&c, &f)
			if _, err := Restore(c); err == nil {
				if _, err := Restore(f); err == nil {
					t.Fatal("a broken snapshot was restored")
				}
			}
		})
	}
}

func TestTierConsolidatedIsReleasedAsStandard(t *testing.T) {
	if !TierConsolidated.Valid() || TierConsolidated.FulfilmentClass() != TierStandard || Tier("OTHER").Valid() {
		t.Fatal("CONSOLIDATED is a valid tier whose fulfilment class is STANDARD")
	}
	for _, tier := range []Tier{TierSameDay, TierNextDay, TierTwoDay, TierStandard} {
		if tier.FulfilmentClass() != tier {
			t.Fatalf("%s must keep its class", tier)
		}
	}
	if !OutcomeWithdrawn.Valid() {
		t.Fatal("WITHDRAWN is a release outcome")
	}
}

func TestWindowBrokenAfterARevision(t *testing.T) {
	cases := map[string]struct {
		earliest, latest time.Time
		broken           bool
	}{
		"inside":        {at(24), at(40), false},
		"eta at latest": {at(24), at(30), false},
		"eta at start":  {at(30), at(40), false},
		"after latest":  {at(24), at(29), true},
		"before start":  {at(31), at(40), true},
		"start unknown": {time.Time{}, at(40), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			carrier, _, _ := mergedPair(t) // RELEASED, candidate ETA at(30)
			s := carrier.Snapshot()
			s.Promise.DeliverByEarliest, s.Promise.DeliverByLatest = tc.earliest, tc.latest
			a, err := Restore(s)
			wantNoErr(t, err)
			if a.WindowBroken() != tc.broken {
				t.Fatalf("WindowBroken = %v, want %v", a.WindowBroken(), tc.broken)
			}
		})
	}
	if mustQueue(t).WindowBroken() != false {
		t.Fatal("a QUEUED group with an in-window candidate is not broken")
	}
}
