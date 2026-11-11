package repocontract

import (
	"reflect"
	"strings"
	"testing"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// fixtureKey is a consolidation key (64 lower-case hexadecimal characters).
var fixtureKey = strings.Repeat("ab", 32)

// keyed builds a QUEUED CONSOLIDATED assignment of the given key whose window
// is [hours(24), hours(latest)] (ADR 0015).
func keyed(t *testing.T, id, order, key string, latest int) *assignment.Assignment {
	t.Helper()
	in := input(id, order, 1)
	in.ConsolidationKey = key
	in.Promise.Tier = assignment.TierConsolidated
	in.Promise.DeliverByEarliest, in.Promise.DeliverByLatest = hours(24), hours(latest)
	a, _, err := assignment.Queue(in, candidate("node-a"), hours(2), T0)
	if err != nil {
		t.Fatalf("Queue %s: %v", id, err)
	}
	return a
}

// mergedPair releases keyed carrier and follower at node-a (version 2 each).
func mergedPair(t *testing.T, carrierID, followerID string) (carrier, follower *assignment.Assignment) {
	t.Helper()
	carrier, follower = keyed(t, carrierID, "ord-"+carrierID, fixtureKey, 60), keyed(t, followerID, "ord-"+followerID, fixtureKey, 60)
	facts := assignment.SourcingFacts{NodeRegionID: "r1", DestinationRegionID: "r1", CandidateCount: 1}
	member := assignment.MergedMember{ID: follower.ID(), OrderID: follower.OrderID(), GroupNo: 1, PromiseID: "prm-" + followerID,
		DeliverByLatest: hours(60), Lines: releaseLines()}
	mustDo(t, func() error {
		_, err := carrier.ReleaseConsolidated(candidate("node-a"), releaseLines(), []assignment.MergedMember{member}, facts, hours(2))
		return err
	})
	mustDo(t, func() error {
		_, err := follower.ReleaseAsFollower(carrier.ID(), candidate("node-a"), releaseLines(), 2, facts, hours(2))
		return err
	})
	return carrier, follower
}

// mergeRoundTrip: every merge field persists (the four places of the
// aggregate): the key, the window start, the carrier link and the members, in a
// live merge, after the lock and after a dissolution that left a WITHDRAWN release.
func mergeRoundTrip(t *testing.T, fx Fixture) {
	carrier, follower := mergedPair(t, "asg-c", "asg-f")
	locked, lockedFollower := mergedPair(t, "asg-lc", "asg-lf")
	mustDo(t, func() error { _, err := locked.Lock(1, hours(3), hours(3)); return err })
	mustDo(t, func() error {
		_, err := lockedFollower.LockFollower(locked.MergedAssignmentIDs(), hours(3), hours(3))
		return err
	})
	withdrawn, other := mergedPair(t, "asg-wc", "asg-wf")
	d := assignment.Dissolution{Reason: assignment.ReasonPromiseRevised, TriggerID: other.ID(), MemberIDs: []assignment.ID{withdrawn.ID(), other.ID()}}
	mustDo(t, func() error { _, err := withdrawn.Requeue(candidate("node-b"), hours(6), d, hours(3)); return err })
	for _, a := range []*assignment.Assignment{carrier, follower, locked, lockedFollower, withdrawn} {
		saveHistory(t, fx, a)
		got, err := fx.Assignments.Get(ctx(), a.ID())
		if err != nil {
			t.Fatalf("Get %s: %v", a.ID(), err)
		}
		if !reflect.DeepEqual(got.Snapshot(), a.Snapshot()) {
			t.Fatalf("round trip of %s differs:\n got %+v\nwant %+v", a.ID(), got.Snapshot(), a.Snapshot())
		}
	}
	got, _ := fx.Assignments.Get(ctx(), "asg-c")
	s, f := got.Snapshot(), follower.Snapshot()
	if s.ConsolidationKey != fixtureKey || len(s.Members) != 1 || s.Members[0] != "asg-f" || !s.Promise.DeliverByEarliest.Equal(hours(24)) {
		t.Fatalf("carrier = %+v", s)
	}
	if f.ConsolidatedInto != "asg-c" || len(f.Members) != 0 {
		t.Fatalf("follower = %+v", f)
	}
	again, _ := fx.Assignments.Get(ctx(), "asg-wc")
	if rels := again.Releases(); rels[0].Outcome != assignment.OutcomeWithdrawn || again.Merged() {
		t.Fatalf("withdrawn carrier = %+v merged %v", rels, again.Merged())
	}
}

// queuedByKey: only QUEUED rows of the key, never the excluded one, ordered by
// deliverByLatest then id, at most limit; an empty key matches nothing.
func queuedByKey(t *testing.T, fx Fixture) {
	other := strings.Repeat("cd", 32)
	for _, a := range []*assignment.Assignment{
		keyed(t, "asg-a", "ord-a", fixtureKey, 60), keyed(t, "asg-b", "ord-b", fixtureKey, 50), keyed(t, "asg-c", "ord-c", fixtureKey, 50),
		keyed(t, "asg-d", "ord-d", other, 40), queued(t, "asg-e", "ord-e", 1, hours(2)),
	} {
		save(t, fx, a)
	}
	rel := keyed(t, "asg-r", "ord-r", fixtureKey, 30)
	mustDo(t, func() error {
		_, err := rel.Release(candidate("node-a"), releaseLines(), assignment.SourcingFacts{CandidateCount: 1}, hours(2))
		return err
	})
	saveHistory(t, fx, rel)
	get := func(key string, exclude assignment.ID, limit int) []*assignment.Assignment {
		got, err := fx.Assignments.QueuedByKey(ctx(), key, exclude, limit)
		if err != nil {
			t.Fatalf("QueuedByKey: %v", err)
		}
		return got
	}
	wantIDs(t, get(fixtureKey, "asg-a", 10), "asg-b", "asg-c")
	wantIDs(t, get(fixtureKey, "", 10), "asg-b", "asg-c", "asg-a")
	wantIDs(t, get(fixtureKey, "", 2), "asg-b", "asg-c")
	wantIDs(t, get(fixtureKey, "asg-b", 10), "asg-c", "asg-a")
	wantIDs(t, get(other, "", 10), "asg-d")
	wantIDs(t, get("", "", 10))
	wantIDs(t, get(strings.Repeat("ef", 32), "", 10))
	if got, err := fx.Assignments.QueuedByKey(ctx(), fixtureKey, "", 0); err != nil || len(got) != 0 {
		t.Fatalf("a zero limit = %v %v", got, err)
	}
}
