package kafka

import (
	"fmt"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// entityAssignment is the CloudEvents entity segment of the assignment
// aggregate; entityPolicy that of the SourcingPolicy (ADR 0014).
const (
	entityAssignment = "assignment"
	entityPolicy     = "policy"
)

// projectedKinds maps the FULL CloudEvents type of each projected event
// (ADR 0013) to its report.Kind.
func projectedKinds() map[string]report.Kind {
	return map[string]report.Kind{
		cloudevents.Type(entityAssignment, string(report.KindFulfillmentQueued)):       report.KindFulfillmentQueued,
		cloudevents.Type(entityAssignment, string(report.KindFulfillmentDecided)):      report.KindFulfillmentDecided,
		cloudevents.Type(entityAssignment, string(report.KindFulfillmentUnsourceable)): report.KindFulfillmentUnsourceable,
	}
}

// acknowledgedTypes are this context's other published types: committed past
// without a warning, because no report needs them (ADR 0013).
func acknowledgedTypes() map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{
		"FulfillmentReleasedToNode", "FulfillmentReassigned", "FulfillmentLocked", "FulfillmentShipped",
		"FulfillmentDelivered", "FulfillmentCancelled", "FulfillmentCancelRefused", "FulfillmentConstraintRevised",
		// The consolidation facts (ADR 0015): the packages-per-order reports are a later slice.
		"FulfillmentReleaseWithdrawn", "AssignmentsConsolidated", "AssignmentsDeconsolidated",
	} {
		out[cloudevents.Type(entityAssignment, name)] = true
	}
	// The analytics-only policy fact (ADR 0014): no report needs it yet.
	out[cloudevents.Type(entityPolicy, "SourcingPolicyPublished")] = true
	return out
}

// analyticsData is the union of the payload fields the projection reads,
// hand-mirrored from apis/asyncapi.yaml (never imported from the encoder).
// Unknown fields are ignored; an absent number stays zero, an absent
// timestamp zero, an absent optional field nil.
type analyticsData struct {
	AssignmentID        string    `json:"assignmentId"`
	OrderID             string    `json:"orderId"`
	GroupNo             int       `json:"groupNo"`
	ReleaseNo           int       `json:"releaseNo"`
	NodeID              string    `json:"nodeId"`
	NodeRegionID        string    `json:"nodeRegionId"`
	DestinationRegionID string    `json:"destinationRegionId"`
	InRegion            *bool     `json:"inRegion"`
	Legs                int       `json:"legs"`
	EstimatedCostCents  *int64    `json:"estimatedCostCents"`
	CostComplete        bool      `json:"costComplete"`
	Units               int       `json:"units"`
	CandidateCount      int       `json:"candidateCount"`
	DecidedAt           time.Time `json:"decidedAt"`
	// CarrierAssignmentID is set on the fact of a follower of a merged release (ADR 0015).
	CarrierAssignmentID string `json:"carrierAssignmentId"`
}

// followerCost is the estimate a decision adds to the cost totals. A merged
// package ships once, so the carrier's fact holds its cost and a follower's
// fact (it names its carrier) books 0: cost-to-serve counts the package once
// while the follower's units still count (ADR 0015).
func followerCost(d analyticsData) *int64 {
	if d.CarrierAssignmentID == "" || d.EstimatedCostCents == nil {
		return d.EstimatedCostCents
	}
	zero := int64(0)
	return &zero
}

// toEvent decodes and validates a CloudEvent of a projected kind. Every error
// is deterministic: the same bytes can never pass.
func toEvent(kind report.Kind, evt ce.Event) (report.Event, error) {
	var d analyticsData
	if err := evt.DataAs(&d); err != nil {
		return report.Event{}, fmt.Errorf("decode %s %s data: %w", kind, evt.ID(), err)
	}
	if d.AssignmentID == "" || d.AssignmentID != evt.Subject() {
		return report.Event{}, fmt.Errorf("%s %s: the aggregate id %q does not match the subject %q", kind, evt.ID(), d.AssignmentID, evt.Subject())
	}
	occurredAt := evt.Time()
	if kind == report.KindFulfillmentDecided {
		occurredAt = d.DecidedAt
	}
	e := report.Event{
		Kind: kind, EventID: evt.ID(), At: evt.Time().UTC(), AssignmentID: d.AssignmentID, OrderID: d.OrderID, GroupNo: d.GroupNo,
		OccurredAt: occurredAt.UTC(),
	}
	if kind == report.KindFulfillmentDecided {
		e.Decision = report.Decision{
			ReleaseNo: d.ReleaseNo, NodeID: d.NodeID, NodeRegionID: d.NodeRegionID, DestinationRegionID: d.DestinationRegionID,
			InRegion: d.InRegion, Legs: d.Legs, EstimatedCostCents: followerCost(d), CostComplete: d.CostComplete,
			Units: d.Units, CandidateCount: d.CandidateCount,
		}
	}
	if err := e.Validate(); err != nil {
		return report.Event{}, err
	}
	return e, nil
}
