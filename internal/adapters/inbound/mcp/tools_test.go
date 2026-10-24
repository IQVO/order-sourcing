package mcp_test

import (
	"testing"
)

func TestListAssignments(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	h.saveReleased(t, "fa-b", "ord-1", 2)

	got := h.ok(t, "list_assignments", map[string]any{})
	items, _ := got["assignments"].([]any)
	if len(items) != 2 {
		t.Fatalf("list_assignments = %d items, want 2: %v", len(items), got)
	}
	first, _ := items[0].(map[string]any)
	for k, v := range map[string]any{
		"assignmentId": "fa-a", "orderId": "ord-1", "groupNo": 1.0,
		"destinationPostalPrefix": "30301", "state": "QUEUED", "assignmentVersion": 1.0,
	} {
		if first[k] != v {
			t.Errorf("first.%s = %v, want %v", k, first[k], v)
		}
	}
	promise, _ := first["promise"].(map[string]any)
	if promise["promiseId"] != "prm-fa-a" || promise["tier"] != "TWO_DAY" || promise["witnessNodeId"] != "node-a" {
		t.Fatalf("promise = %v", promise)
	}
	lines, _ := first["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one", first["lines"])
	}
	cand, _ := first["candidate"].(map[string]any)
	if cand == nil || cand["nodeId"] != "node-a" || cand["cost"] != 130.0 {
		t.Fatalf("candidate = %v", first["candidate"])
	}
}

func TestListAssignmentsFilters(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	h.saveReleased(t, "fa-b", "ord-2", 1)

	if got := h.ok(t, "list_assignments", map[string]any{"state": "QUEUED"}); len(got["assignments"].([]any)) != 1 {
		t.Fatalf("state filter = %v", got)
	}
	if got := h.ok(t, "list_assignments", map[string]any{"orderId": "ord-2"}); len(got["assignments"].([]any)) != 1 {
		t.Fatalf("orderId filter = %v", got)
	}
	// QUEUED reports the candidate's node; RELEASED reports the latest release's node.
	if got := h.ok(t, "list_assignments", map[string]any{"nodeId": "node-a"}); len(got["assignments"].([]any)) != 2 {
		t.Fatalf("nodeId filter = %v", got)
	}
	if got := h.ok(t, "list_assignments", map[string]any{"limit": 1}); len(got["assignments"].([]any)) != 1 {
		t.Fatalf("limit = %v", got)
	}
	if got := h.ok(t, "list_assignments", map[string]any{"state": "UNSOURCEABLE"}); len(got["assignments"].([]any)) != 0 {
		t.Fatalf("state=UNSOURCEABLE = %v", got)
	}
}

func TestListAssignmentsErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "list_assignments", map[string]any{"state": "Bogus"}, "validation-failed")
	failWith(t, h.session, "list_assignments", map[string]any{"limit": 9999}, "validation-failed")
}

func TestGetAssignment(t *testing.T) {
	h := newHarness(t)
	h.saveReleased(t, "fa-a", "ord-1", 1)

	got := h.ok(t, "get_assignment", map[string]any{"assignmentId": "fa-a"})
	if got["state"] != "RELEASED" || got["assignmentVersion"] != 2.0 {
		t.Fatalf("get_assignment = %v", got)
	}
	releases, _ := got["releases"].([]any)
	if len(releases) != 1 {
		t.Fatalf("releases = %v, want one", got["releases"])
	}
	rel, _ := releases[0].(map[string]any)
	if rel["releaseNo"] != 1.0 || rel["nodeId"] != "node-a" || rel["outcome"] != "PENDING" {
		t.Fatalf("release = %v", rel)
	}
	for _, absent := range []string{"lockedAt", "shippedAt", "deliveredAt", "cancelReason"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s must be absent on a RELEASED assignment: %v", absent, got)
		}
	}
}

func TestGetAssignmentQueuedKeepsCandidate(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	got := h.ok(t, "get_assignment", map[string]any{"assignmentId": "fa-a"})
	cand, _ := got["candidate"].(map[string]any)
	if cand == nil || cand["nodeId"] != "node-a" {
		t.Fatalf("candidate = %v", got["candidate"])
	}
	if got["releaseAt"] == "" {
		t.Fatalf("releaseAt must be present while QUEUED: %v", got)
	}
}

func TestGetAssignmentErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_assignment", map[string]any{"assignmentId": "fa-nope"}, "not-found")
}

// TestReadsEmitNothing pins the read-only promise at the store level: no tool
// call appends anything to the transactional outbox.
func TestReadsEmitNothing(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	before := len(h.store.Outbox().Events())
	h.ok(t, "list_assignments", map[string]any{})
	h.ok(t, "get_assignment", map[string]any{"assignmentId": "fa-a"})
	if after := len(h.store.Outbox().Events()); after != before {
		t.Fatalf("outbox %d -> %d: a read tool emitted an event", before, after)
	}
}
