package kafka

import (
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Wire payloads of the consolidation (ADR 0015), exactly the ones pinned in
// apis/asyncapi.yaml: camelCase, arrays always present where listed.

// releasedMember is one member of a merged release.
type releasedMember struct {
	AssignmentID string `json:"assignmentId"`
	OrderID      string `json:"orderId"`
	GroupNo      int    `json:"groupNo"`
	PromiseID    string `json:"promiseId"`
}

// consolidatedMember is one member of AssignmentsConsolidated: the release
// member plus the member's own units.
type consolidatedMember struct {
	AssignmentID string `json:"assignmentId"`
	OrderID      string `json:"orderId"`
	GroupNo      int    `json:"groupNo"`
	PromiseID    string `json:"promiseId"`
	Units        int    `json:"units"`
}

type consolidatedData struct {
	CarrierAssignmentID string               `json:"carrierAssignmentId"`
	ReleaseNo           int                  `json:"releaseNo"`
	NodeID              string               `json:"nodeId"`
	MemberCount         int                  `json:"memberCount"`
	Units               int                  `json:"units"`
	Members             []consolidatedMember `json:"members"`
	ConsolidatedAt      time.Time            `json:"consolidatedAt"`
}

type deconsolidatedData struct {
	CarrierAssignmentID string    `json:"carrierAssignmentId"`
	ReleaseNo           int       `json:"releaseNo"`
	Reason              string    `json:"reason"`
	TriggerAssignmentID string    `json:"triggerAssignmentId,omitempty"`
	MemberAssignmentIDs []string  `json:"memberAssignmentIds"`
	DeconsolidatedAt    time.Time `json:"deconsolidatedAt"`
}

type withdrawnData struct {
	AssignmentID string    `json:"assignmentId"`
	ReleaseNo    int       `json:"releaseNo"`
	OrderID      string    `json:"orderId"`
	GroupNo      int       `json:"groupNo"`
	Reason       string    `json:"reason"`
	WithdrawnAt  time.Time `json:"withdrawnAt"`
}

// idStrings is the wire form of a list of assignment ids; nil stays nil so the
// field is omitted when the assignment is not merged.
func idStrings(in []assignment.ID) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, id := range in {
		out = append(out, string(id))
	}
	return out
}

// released is the wire form of FulfillmentReleasedToNode: a merged release also
// lists its members and names the member of each line.
func released(e assignment.FulfillmentReleasedToNode) releasedData {
	d := releasedData{
		AssignmentID: e.SubjectID(), ReleaseNo: e.ReleaseNo, OrderID: e.OrderID, GroupNo: e.GroupNo, NodeID: e.NodeID,
		PickupDepartureID: e.PickupDepartureID, RequiredShipBy: utc(e.RequiredShipBy), PromisedDeliverByLatest: utc(e.PromisedDeliverByLatest),
		Tier: string(e.Tier), Lines: releasedLines(e.Lines), EstimatedCostCents: e.EstimatedCostCents, PolicyVersion: e.PolicyVersion,
		EffectiveCostCents: e.EffectiveCostCents,
		EstimatedCo2eGrams: e.EstimatedCo2eGrams, EmissionComplete: e.EmissionComplete,
	}
	for i := range d.Lines {
		if i < len(e.LineMembers) {
			d.Lines[i].AssignmentID = string(e.LineMembers[i])
		}
	}
	for _, m := range e.Members {
		d.Members = append(d.Members, releasedMember{string(m.ID), m.OrderID, m.GroupNo, m.PromiseID})
	}
	return d
}

// consolidationPayload maps the three consolidation events to their wire data.
func consolidationPayload(ev shared.Event) (any, error) {
	id := ev.SubjectID()
	switch e := ev.(type) {
	case assignment.AssignmentsConsolidated:
		members := make([]consolidatedMember, 0, len(e.Members))
		for _, m := range e.Members {
			members = append(members, consolidatedMember{string(m.ID), m.OrderID, m.GroupNo, m.PromiseID, m.Units()})
		}
		return consolidatedData{id, e.ReleaseNo, e.NodeID, len(e.Members), e.Units(), members, utc(e.ConsolidatedAt)}, nil
	case assignment.AssignmentsDeconsolidated:
		return deconsolidatedData{id, e.ReleaseNo, e.Reason, string(e.TriggerAssignmentID), append([]string{}, idStrings(e.MemberAssignmentIDs)...),
			utc(e.DeconsolidatedAt)}, nil
	case assignment.FulfillmentReleaseWithdrawn:
		return withdrawnData{id, e.ReleaseNo, e.OrderID, e.GroupNo, e.Reason, utc(e.WithdrawnAt)}, nil
	default:
		return nil, fmt.Errorf("kafka encoder: no consolidation wire form for %T", ev)
	}
}
