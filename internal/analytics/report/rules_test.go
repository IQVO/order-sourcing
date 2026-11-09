package report_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

var nov3 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

func cents(v int64) *int64 { return &v }

func decided() report.Event {
	return report.Event{Kind: report.KindFulfillmentDecided, EventID: "e1", At: nov3, AssignmentID: "asg-1", OrderID: "ord-1", GroupNo: 1,
		OccurredAt: nov3, Decision: report.Decision{ReleaseNo: 1, NodeID: "n1", Legs: 0, Units: 1, CandidateCount: 1, CostComplete: true,
			EstimatedCostCents: cents(0)}}
}

func TestValidateAcceptsEveryKind(t *testing.T) {
	queued := report.Event{Kind: report.KindFulfillmentQueued, EventID: "e1", At: nov3, AssignmentID: "a", OrderID: "o", GroupNo: 1, OccurredAt: nov3}
	unsourceable := report.Event{Kind: report.KindFulfillmentUnsourceable, EventID: "e2", At: nov3, AssignmentID: "a", OrderID: "o", OccurredAt: nov3}
	incomplete := decided()
	incomplete.Decision.CostComplete, incomplete.Decision.EstimatedCostCents = false, nil
	incomplete.GroupNo = 0 // optional on a decision
	for _, e := range []report.Event{queued, unsourceable, decided(), incomplete} {
		if err := e.Validate(); err != nil {
			t.Errorf("%s: %v", e.Kind, err)
		}
	}
}

// invalid is one mutation of a valid event and the field the error must name.
type invalid struct {
	name   string
	mutate func(*report.Event)
}

var invalidEvents = []invalid{
	{"id", func(e *report.Event) { e.EventID = "" }},
	{"time", func(e *report.Event) { e.At = time.Time{} }},
	{"assignmentId", func(e *report.Event) { e.AssignmentID = "" }},
	{"orderId", func(e *report.Event) { e.OrderID = "" }},
	{"the business instant", func(e *report.Event) { e.OccurredAt = time.Time{} }},
	{"releaseNo >= 1", func(e *report.Event) { e.Decision.ReleaseNo = 0 }},
	{"nodeId", func(e *report.Event) { e.Decision.NodeID = "" }},
	{"legs >= 0", func(e *report.Event) { e.Decision.Legs = -1 }},
	{"units >= 1", func(e *report.Event) { e.Decision.Units = 0 }},
	{"candidateCount >= 1", func(e *report.Event) { e.Decision.CandidateCount = 0 }},
	{"estimatedCostCents exactly when costComplete", func(e *report.Event) { e.Decision.EstimatedCostCents = nil }},
	{"estimatedCostCents exactly when costComplete", func(e *report.Event) { e.Decision.CostComplete = false }},
	{"estimatedCostCents >= 0", func(e *report.Event) { e.Decision.EstimatedCostCents = cents(-1) }},
	{"a known kind", func(e *report.Event) { e.Kind = "FulfillmentLocked" }},
}

func TestValidateRejectsAnIncompleteDecision(t *testing.T) {
	for _, c := range invalidEvents {
		e := decided()
		c.mutate(&e)
		err := e.Validate()
		if !errors.Is(err, report.ErrInvalidEvent) || !strings.Contains(err.Error(), c.name+" is required") {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func TestValidateRejectsAQueuedGroupWithoutANumber(t *testing.T) {
	e := report.Event{Kind: report.KindFulfillmentQueued, EventID: "e1", At: nov3, AssignmentID: "a", OrderID: "o", OccurredAt: nov3}
	if err := e.Validate(); !errors.Is(err, report.ErrInvalidEvent) || !strings.Contains(err.Error(), "groupNo >= 1") {
		t.Fatalf("err = %v", err)
	}
	e.GroupNo = 1
	if err := e.Validate(); err != nil {
		t.Fatalf("group 1 is valid: %v", err)
	}
}

func TestReassignmentAndSplitRules(t *testing.T) {
	if report.FirstReleaseNo() != 1 || report.SingleGroup() != 1 {
		t.Fatalf("first release %d, single group %d", report.FirstReleaseNo(), report.SingleGroup())
	}
	for no, want := range map[int]bool{0: false, 1: false, 2: true, 7: true} {
		if got := report.IsReassignment(no); got != want {
			t.Errorf("IsReassignment(%d) = %v, want %v", no, got, want)
		}
	}
	for groups, want := range map[int]bool{0: false, 1: false, 2: true, 5: true} {
		if got := report.IsSplit(groups); got != want {
			t.Errorf("IsSplit(%d) = %v, want %v", groups, got, want)
		}
	}
}
