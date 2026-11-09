package report

import (
	"errors"
	"fmt"
)

// The KPI rules of ADR 0013. They are functions, not package variables, so a
// caller cannot mutate them and mutation testing reaches them.

// FirstReleaseNo is the number of a group's first release; any later release
// is a reassignment.
func FirstReleaseNo() int { return 1 }

// IsReassignment reports whether a release is not the group's first.
func IsReassignment(releaseNo int) bool { return releaseNo > FirstReleaseNo() }

// SingleGroup is the group count of an order that is not split.
func SingleGroup() int { return 1 }

// IsSplit reports whether an order with this many groups is split.
func IsSplit(groups int) bool { return groups > SingleGroup() }

// CostP50 and CostP90 are the continuous-percentile ranks of the cost per
// group distribution.
func CostP50() float64 { return 0.5 }

// CostP90 is the 90th-percentile rank.
func CostP90() float64 { return 0.9 }

// ErrInvalidEvent marks a fact that can never be projected: a required field
// is missing or contradicts another. It is deterministic, so the projector
// dead-letters it.
var ErrInvalidEvent = errors.New("report: invalid analytics event")

// Validate checks the fields every fact and the fields its Kind needs.
func (e Event) Validate() error {
	missing := firstMissing(
		required{"id", e.EventID != ""},
		required{"time", !e.At.IsZero()},
		required{"assignmentId", e.AssignmentID != ""},
		required{"orderId", e.OrderID != ""},
		required{"the business instant", !e.OccurredAt.IsZero()},
	)
	if missing == "" {
		missing = e.kindMissing()
	}
	if missing != "" {
		return fmt.Errorf("%w: %s %s: %s is required", ErrInvalidEvent, e.Kind, e.EventID, missing)
	}
	return nil
}

// kindMissing names the first missing or inconsistent field of the Kind's own
// payload, or rejects a Kind the projection does not know.
func (e Event) kindMissing() string {
	if e.Kind == KindFulfillmentQueued {
		return firstMissing(required{"groupNo >= 1", e.GroupNo >= 1})
	}
	if e.Kind == KindFulfillmentDecided {
		return e.Decision.missing()
	}
	if e.Kind == KindFulfillmentUnsourceable {
		return ""
	}
	return "a known kind"
}

// missing names the first absent or inconsistent field of a decision.
func (d Decision) missing() string {
	return firstMissing(
		required{"releaseNo >= 1", d.ReleaseNo >= 1},
		required{"nodeId", d.NodeID != ""},
		required{"legs >= 0", d.Legs >= 0},
		required{"units >= 1", d.Units >= 1},
		required{"candidateCount >= 1", d.CandidateCount >= 1},
		required{"estimatedCostCents exactly when costComplete", d.CostComplete == (d.EstimatedCostCents != nil)},
		required{"estimatedCostCents >= 0", d.EstimatedCostCents == nil || *d.EstimatedCostCents >= 0},
	)
}

// required is one field check: name and whether it is present.
type required struct {
	name    string
	present bool
}

// firstMissing names the first absent field, or "" when every one is present.
func firstMissing(fields ...required) string {
	for _, f := range fields {
		if !f.present {
			return f.name
		}
	}
	return ""
}
