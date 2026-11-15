package http

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
)

// Wire shapes of apis/openapi.yaml (camelCase). Optional timestamps are
// pointers so an unset one is absent, not "0001-01-01".

type promiseDTO struct {
	PromiseID         string     `json:"promiseId"`
	Tier              string     `json:"tier"`
	DeliverByEarliest *time.Time `json:"deliverByEarliest,omitempty"`
	DeliverByLatest   time.Time  `json:"deliverByLatest"`
	WitnessNodeID     string     `json:"witnessNodeId"`
	RevisionNo        int        `json:"revisionNo"`
}

type lineDTO struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	ListingID     string `json:"listingId,omitempty"`
	OwnerRef      string `json:"ownerRef,omitempty"`
	Quantity      int    `json:"quantity"`
}

type candidateDTO struct {
	NodeID             string    `json:"nodeId"`
	PickupDepartureID  string    `json:"pickupDepartureId"`
	PickupCutoffAt     time.Time `json:"pickupCutoffAt"`
	Legs               []string  `json:"legs"`
	DeliveryWaveID     string    `json:"deliveryWaveId"`
	ETA                time.Time `json:"eta"`
	Cost               int64     `json:"cost"`
	EstimatedCostCents *int64    `json:"estimatedCostCents,omitempty"`
	// PolicyVersion is omitted when no sourcing policy was published and
	// EffectiveCostCents unless a surcharge changed the cost (ADR 0014).
	PolicyVersion      int    `json:"policyVersion,omitempty"`
	EffectiveCostCents *int64 `json:"effectiveCostCents,omitempty"`
	// EstimatedCo2eGrams is the carbon estimate of the path in planner-declared
	// grams of CO2e, omitted when incomplete (ADR 0017).
	EstimatedCo2eGrams *int64 `json:"estimatedCo2eGrams,omitempty"`
}

type releaseDTO struct {
	ReleaseNo      int       `json:"releaseNo"`
	NodeID         string    `json:"nodeId"`
	ReleasedAt     time.Time `json:"releasedAt"`
	RequiredShipBy time.Time `json:"requiredShipBy"`
	Outcome        string    `json:"outcome"`
	ReasonCode     string    `json:"reasonCode,omitempty"`
}

type excludedDTO struct {
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}

type assignmentDTO struct {
	AssignmentID            string        `json:"assignmentId"`
	OrderID                 string        `json:"orderId"`
	GroupNo                 int           `json:"groupNo"`
	DestinationPostalPrefix string        `json:"destinationPostalPrefix"`
	State                   string        `json:"state"`
	AssignmentVersion       int64         `json:"assignmentVersion"`
	Promise                 promiseDTO    `json:"promise"`
	Lines                   []lineDTO     `json:"lines"`
	Candidate               *candidateDTO `json:"candidate,omitempty"`
	ReleaseAt               *time.Time    `json:"releaseAt,omitempty"`
	Releases                []releaseDTO  `json:"releases"`
	ExcludedNodes           []excludedDTO `json:"excludedNodes"`
	LockedAt                *time.Time    `json:"lockedAt,omitempty"`
	LockReason              string        `json:"lockReason,omitempty"`
	PickupDepartureID       string        `json:"pickupDepartureId,omitempty"`
	TrackingNumbers         []string      `json:"trackingNumbers"`
	ShippedAt               *time.Time    `json:"shippedAt,omitempty"`
	DeliveredAt             *time.Time    `json:"deliveredAt,omitempty"`
	CancelReason            string        `json:"cancelReason,omitempty"`
	// ConsolidatedInto names the carrier of a follower, ConsolidatedWith the
	// followers of a carrier; both are omitted when not merged (ADR 0015).
	ConsolidatedInto string   `json:"consolidatedInto,omitempty"`
	ConsolidatedWith []string `json:"consolidatedWith,omitempty"`
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// estimateCentsOf is the wire value of a candidate's money-denominated
// estimate: the cents when complete, absent when incomplete (ADR 0011).
func estimateCentsOf(c *views.Candidate) *int64 {
	if !c.CostComplete {
		return nil
	}
	cents := c.EstimatedCostCents
	return &cents
}

// estimateGramsOf is the wire value of a candidate's carbon estimate: the
// grams of CO2e when complete, absent when incomplete (ADR 0017).
func estimateGramsOf(c *views.Candidate) *int64 {
	if !c.EmissionComplete {
		return nil
	}
	grams := c.EstimatedCo2eGrams
	return &grams
}

// effectiveCentsOf is the wire value of a candidate's effective cost: present
// only when a surcharge changed the cost (ADR 0014).
func effectiveCentsOf(c *views.Candidate) *int64 {
	if c.SurchargeCents == 0 {
		return nil
	}
	cents := c.EffectiveCostCents()
	return &cents
}

func toDTO(v views.Assignment) assignmentDTO {
	d := assignmentDTO{
		AssignmentID: v.AssignmentID, OrderID: v.OrderID, GroupNo: v.GroupNo, DestinationPostalPrefix: v.DestinationPostalPrefix,
		State: v.State, AssignmentVersion: v.AssignmentVersion,
		Promise:          promiseDTO{v.Promise.PromiseID, v.Promise.Tier, optTime(v.Promise.DeliverByEarliest), v.Promise.DeliverByLatest.UTC(), v.Promise.WitnessNodeID, v.Promise.RevisionNo},
		ConsolidatedInto: v.ConsolidatedInto, ConsolidatedWith: v.ConsolidatedWith,
		Lines: make([]lineDTO, 0, len(v.Lines)), Releases: make([]releaseDTO, 0, len(v.Releases)), ExcludedNodes: make([]excludedDTO, 0, len(v.ExcludedNodes)),
		ReleaseAt: optTime(v.ReleaseAt), LockedAt: optTime(v.LockedAt), LockReason: v.LockReason, PickupDepartureID: v.PickupDepartureID,
		TrackingNumbers: append([]string{}, v.TrackingNumbers...), ShippedAt: optTime(v.ShippedAt), DeliveredAt: optTime(v.DeliveredAt),
		CancelReason: v.CancelReason,
	}
	for _, l := range v.Lines {
		d.Lines = append(d.Lines, lineDTO{l.LineNo, l.CatalogItemID, l.ListingID, l.OwnerRef, l.Quantity})
	}
	if c := v.Candidate; c != nil {
		d.Candidate = &candidateDTO{c.NodeID, c.PickupDepartureID, c.PickupCutoffAt.UTC(), append([]string{}, c.Legs...), c.DeliveryWaveID, c.ETA.UTC(), c.Cost,
			estimateCentsOf(c), c.PolicyVersion, effectiveCentsOf(c), estimateGramsOf(c)}
	}
	for _, r := range v.Releases {
		d.Releases = append(d.Releases, releaseDTO{r.ReleaseNo, r.NodeID, r.ReleasedAt.UTC(), r.RequiredShipBy.UTC(), r.Outcome, r.ReasonCode})
	}
	for _, e := range v.ExcludedNodes {
		d.ExcludedNodes = append(d.ExcludedNodes, excludedDTO{e.NodeID, e.Reason})
	}
	return d
}
