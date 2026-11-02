package repocontract

import (
	"errors"
	"reflect"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

func runAssignments(t *testing.T, f Factory) {
	t.Run("a miss is ErrNotFound", func(t *testing.T) { missIsNotFound(t, f(t)) })
	t.Run("save and get round trip every state", func(t *testing.T) { roundTrip(t, f(t)) })
	t.Run("the version rule", func(t *testing.T) { versionRule(t, f(t)) })
	t.Run("one assignment per order group", func(t *testing.T) { oneAssignmentPerGroup(t, f(t)) })
	t.Run("find by group and by promise", func(t *testing.T) { findBy(t, f(t)) })
	t.Run("list filters, order and limit", func(t *testing.T) { listFilters(t, f(t)) })
	t.Run("due and queued", func(t *testing.T) { dueAndQueued(t, f(t)) })
}

func missIsNotFound(t *testing.T, fx Fixture) {
	if _, err := fx.Assignments.Get(ctx(), "asg-none"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Get = %v", err)
	}
	if _, err := fx.Assignments.FindByGroup(ctx(), "ord-none", 1); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("FindByGroup = %v", err)
	}
	if _, err := fx.Assignments.FindByPromise(ctx(), "prm-none"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("FindByPromise = %v", err)
	}
	if list, err := fx.Assignments.List(ctx(), repository.AssignmentFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("List = %v %v", list, err)
	}
}

func save(t *testing.T, fx Fixture, a *assignment.Assignment) {
	t.Helper()
	if err := fx.Assignments.Save(ctx(), a); err != nil {
		t.Fatalf("Save %s v%d: %v", a.ID(), a.Version(), err)
	}
}

func roundTrip(t *testing.T, fx Fixture) {
	// Every state, built through the domain.
	shipped := released(t, "asg-shipped", "ord-s", 1)
	mustDo(t, func() error { _, err := shipped.Lock(1, hours(3), hours(3)); return err })
	mustDo(t, func() error { _, err := shipped.MarkShipped([]string{"T1", "T2"}, hours(4), hours(4)); return err })
	mustDo(t, func() error { _, err := shipped.MarkDelivered("T1", hours(8), hours(8)); return err })
	refused := released(t, "asg-refused", "ord-r", 1)
	mustDo(t, func() error {
		_, err := refused.RefuseRelease(1, assignment.OutcomeRejected, "OOS", candidate("node-b"), hours(6), hours(3))
		return err
	})
	unsourceable := released(t, "asg-un", "ord-u", 1)
	mustDo(t, func() error {
		_, err := unsourceable.RefuseRelease(1, assignment.OutcomeExpired, "", nil, hours(0), hours(3))
		return err
	})
	cancelled := queued(t, "asg-c", "ord-c", 1, hours(2))
	mustDo(t, func() error { _, err := cancelled.Cancel("CUSTOMER_REQUEST", hours(1)); return err })
	noCandidate, _, err := assignment.Queue(input("asg-n", "ord-n", 1), nil, hours(0), T0)
	if err != nil {
		t.Fatal(err)
	}
	all := []*assignment.Assignment{queued(t, "asg-q", "ord-q", 1, hours(2)), shipped, refused, unsourceable, cancelled, noCandidate}
	for _, a := range all {
		saveHistory(t, fx, a)
		got, err := fx.Assignments.Get(ctx(), a.ID())
		if err != nil {
			t.Fatalf("Get %s: %v", a.ID(), err)
		}
		if !reflect.DeepEqual(got.Snapshot(), a.Snapshot()) {
			t.Fatalf("round trip of %s differs:\n got %+v\nwant %+v", a.ID(), got.Snapshot(), a.Snapshot())
		}
	}
}

// saveHistory stores an assignment that is already past version 1: it saves a
// version 1 twin first (the repository expects consecutive versions), then the
// final state through each intermediate step is not needed: a jump is a stale save.
func saveHistory(t *testing.T, fx Fixture, a *assignment.Assignment) {
	t.Helper()
	snap := a.Snapshot()
	if snap.Version == 1 {
		save(t, fx, a)
		return
	}
	// Replay the version chain by restoring copies at each version: only the last one differs, so
	// store a version-1 stand-in with the same id, then the final aggregate one version at a time.
	for v := int64(1); v < snap.Version; v++ {
		stepAgg, err := assignment.Restore(stepSnapshot(snap, v))
		if err != nil {
			t.Fatalf("stand-in v%d of %s: %v", v, a.ID(), err)
		}
		save(t, fx, stepAgg)
	}
	save(t, fx, a)
}

func stepSnapshot(final assignment.Snapshot, v int64) assignment.Snapshot {
	// A QUEUED stand-in with the same identity: valid at any version.
	s := assignment.Snapshot{
		ID: final.ID, OrderID: final.OrderID, GroupNo: final.GroupNo, DestinationPostalPrefix: final.DestinationPostalPrefix,
		Promise: final.Promise, Lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
		State: assignment.StateQueued, Candidate: candidate("node-a"), ReleaseAt: hours(2), Version: v,
		QueuedAt: final.QueuedAt, UpdatedAt: final.UpdatedAt,
	}
	return s
}

func mustDo(t *testing.T, f func() error) {
	t.Helper()
	if err := f(); err != nil {
		t.Fatal(err)
	}
}

func versionRule(t *testing.T, fx Fixture) {
	a := queued(t, "asg-1", "ord-1", 1, hours(2))
	save(t, fx, a)
	if err := fx.Assignments.Save(ctx(), a); !errors.Is(err, ports.ErrConcurrentUpdate) {
		t.Fatalf("re-saving a create = %v, want ErrConcurrentUpdate", err)
	}
	loaded, _ := fx.Assignments.Get(ctx(), "asg-1")
	other, _ := fx.Assignments.Get(ctx(), "asg-1")
	if _, err := loaded.Reoptimise(candidate("node-b"), hours(3), 0, hours(1)); err != nil {
		t.Fatal(err)
	}
	save(t, fx, loaded)
	if _, err := other.Reoptimise(candidate("node-c"), hours(3), 0, hours(1)); err != nil {
		t.Fatal(err)
	}
	if err := fx.Assignments.Save(ctx(), other); !errors.Is(err, ports.ErrConcurrentUpdate) {
		t.Fatalf("a stale save = %v, want ErrConcurrentUpdate", err)
	}
	got, _ := fx.Assignments.Get(ctx(), "asg-1")
	if c, _ := got.Candidate(); got.Version() != 2 || c.NodeID != "node-b" {
		t.Fatalf("stored = v%d on %s: the stale save must change nothing", got.Version(), c.NodeID)
	}
	// An update of a row that does not exist is a conflict too.
	ghost := queued(t, "asg-ghost", "ord-g", 1, hours(2))
	if _, err := ghost.Reoptimise(candidate("node-b"), hours(3), 0, hours(1)); err != nil {
		t.Fatal(err)
	}
	if err := fx.Assignments.Save(ctx(), ghost); !errors.Is(err, ports.ErrConcurrentUpdate) {
		t.Fatalf("an update of a missing row = %v, want ErrConcurrentUpdate", err)
	}
}

func oneAssignmentPerGroup(t *testing.T, fx Fixture) {
	save(t, fx, queued(t, "asg-1", "ord-1", 1, hours(2)))
	if err := fx.Assignments.Save(ctx(), queued(t, "asg-2", "ord-1", 1, hours(2))); !errors.Is(err, ports.ErrDuplicateGroup) {
		t.Fatalf("a second assignment of one group = %v, want ErrDuplicateGroup", err)
	}
	save(t, fx, queued(t, "asg-3", "ord-1", 2, hours(2)))
	save(t, fx, queued(t, "asg-4", "ord-2", 1, hours(2)))
}

func findBy(t *testing.T, fx Fixture) {
	save(t, fx, queued(t, "asg-1", "ord-1", 1, hours(2)))
	save(t, fx, queued(t, "asg-2", "ord-1", 2, hours(2)))
	g, err := fx.Assignments.FindByGroup(ctx(), "ord-1", 2)
	if err != nil || g.ID() != "asg-2" {
		t.Fatalf("FindByGroup = %v %v", g, err)
	}
	p, err := fx.Assignments.FindByPromise(ctx(), "prm-asg-1")
	if err != nil || p.ID() != "asg-1" {
		t.Fatalf("FindByPromise = %v %v", p, err)
	}
	if _, err := fx.Assignments.FindByGroup(ctx(), "ord-1", 3); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("FindByGroup miss = %v", err)
	}
}

func ids(list []*assignment.Assignment) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, string(a.ID()))
	}
	return out
}

func wantIDs(t *testing.T, got []*assignment.Assignment, want ...string) {
	t.Helper()
	g := ids(got)
	if (len(g) != 0 || len(want) != 0) && !reflect.DeepEqual(g, append([]string{}, want...)) {
		t.Fatalf("ids = %v, want %v", g, want)
	}
}

func listFilters(t *testing.T, fx Fixture) {
	save(t, fx, queued(t, "asg-b", "ord-1", 1, hours(2)))
	save(t, fx, queued(t, "asg-a", "ord-1", 2, hours(2)))
	rel := released(t, "asg-c", "ord-2", 1)
	saveHistory(t, fx, rel)
	un := released(t, "asg-d", "ord-3", 1)
	mustDo(t, func() error {
		_, err := un.RefuseRelease(1, assignment.OutcomeRejected, "X", nil, hours(0), hours(3))
		return err
	})
	saveHistory(t, fx, un)
	list := func(f repository.AssignmentFilter) []*assignment.Assignment {
		got, err := fx.Assignments.List(ctx(), f)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	wantIDs(t, list(repository.AssignmentFilter{}), "asg-a", "asg-b", "asg-c", "asg-d")
	wantIDs(t, list(repository.AssignmentFilter{State: assignment.StateQueued}), "asg-a", "asg-b")
	wantIDs(t, list(repository.AssignmentFilter{State: assignment.StateReleased}), "asg-c")
	wantIDs(t, list(repository.AssignmentFilter{OrderID: "ord-1"}), "asg-a", "asg-b")
	wantIDs(t, list(repository.AssignmentFilter{OrderID: "ord-1", State: assignment.StateReleased}))
	// nodeId: the candidate node while there is one, else the node of the latest release.
	wantIDs(t, list(repository.AssignmentFilter{NodeID: "node-a"}), "asg-a", "asg-b", "asg-c", "asg-d")
	wantIDs(t, list(repository.AssignmentFilter{NodeID: "node-zzz"}))
	wantIDs(t, list(repository.AssignmentFilter{Limit: 2}), "asg-a", "asg-b")
	// keyset: strictly after the position, still ordered by id, filters combine.
	wantIDs(t, list(repository.AssignmentFilter{After: "asg-b", Limit: 2}), "asg-c", "asg-d")
	wantIDs(t, list(repository.AssignmentFilter{After: "asg-a", State: assignment.StateQueued}), "asg-b")
	wantIDs(t, list(repository.AssignmentFilter{After: "asg-d"}))
}

func dueAndQueued(t *testing.T, fx Fixture) {
	save(t, fx, queued(t, "asg-1", "ord-1", 1, hours(5)))
	save(t, fx, queued(t, "asg-2", "ord-2", 1, hours(1)))
	save(t, fx, queued(t, "asg-3", "ord-3", 1, hours(1)))
	rel := released(t, "asg-4", "ord-4", 1)
	saveHistory(t, fx, rel)
	due, err := fx.Assignments.Due(ctx(), hours(1), 10)
	if err != nil || !reflect.DeepEqual(due, []assignment.ID{"asg-2", "asg-3"}) {
		t.Fatalf("Due = %v %v: only QUEUED rows with releaseAt <= now, by releaseAt then id", due, err)
	}
	due, _ = fx.Assignments.Due(ctx(), hours(1), 1)
	if len(due) != 1 || due[0] != "asg-2" {
		t.Fatalf("Due limit = %v", due)
	}
	due, _ = fx.Assignments.Due(ctx(), hours(0), 10)
	if len(due) != 0 {
		t.Fatalf("nothing is due before %v: %v", hours(1), due)
	}
	page, err := fx.Assignments.Queued(ctx(), "", 2)
	if err != nil || !reflect.DeepEqual(page, []assignment.ID{"asg-1", "asg-2"}) {
		t.Fatalf("Queued = %v %v", page, err)
	}
	page, _ = fx.Assignments.Queued(ctx(), "asg-2", 2)
	if !reflect.DeepEqual(page, []assignment.ID{"asg-3"}) {
		t.Fatalf("Queued after asg-2 = %v", page)
	}
	page, _ = fx.Assignments.Queued(ctx(), "asg-3", 2)
	if len(page) != 0 {
		t.Fatalf("Queued after the last = %v", page)
	}
}
