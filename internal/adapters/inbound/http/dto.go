package http

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
)

// Wire shapes of apis/openapi.yaml (camelCase). Optional timestamps are
// pointers so an unset one is absent, not "0001-01-01".

type promiseDTO struct {
	PromiseID       string    `json:"promiseId"`
	Tier            string    `json:"tier"`
	DeliverByLatest time.Time `json:"deliverByLatest"`
	WitnessNodeID   string    `json:"witnessNodeId"`
	RevisionNo      int       `json:"revisionNo"`
}

type lineDTO struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	ListingID     string `json:"listingId,omitempty"`
	Quantity      int    `json:"quantity"`
}

type candidateDTO struct {
	NodeID            string    `json:"nodeId"`
	PickupDepartureID string    `json:"pickupDepartureId"`
	PickupCutoffAt    time.Time `json:"pickupCutoffAt"`
	Legs              []string  `json:"legs"`
	DeliveryWaveID    string    `json:"deliveryWaveId"`
	ETA               time.Time `json:"eta"`
	Cost              int64     `json:"cost"`
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
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func toDTO(v views.Assignment) assignmentDTO {
	d := assignmentDTO{
		AssignmentID: v.AssignmentID, OrderID: v.OrderID, GroupNo: v.GroupNo, DestinationPostalPrefix: v.DestinationPostalPrefix,
		State: v.State, AssignmentVersion: v.AssignmentVersion,
		Promise: promiseDTO{v.Promise.PromiseID, v.Promise.Tier, v.Promise.DeliverByLatest.UTC(), v.Promise.WitnessNodeID, v.Promise.RevisionNo},
		Lines:   make([]lineDTO, 0, len(v.Lines)), Releases: make([]releaseDTO, 0, len(v.Releases)), ExcludedNodes: make([]excludedDTO, 0, len(v.ExcludedNodes)),
		ReleaseAt: optTime(v.ReleaseAt), LockedAt: optTime(v.LockedAt), LockReason: v.LockReason, PickupDepartureID: v.PickupDepartureID,
		TrackingNumbers: append([]string{}, v.TrackingNumbers...), ShippedAt: optTime(v.ShippedAt), DeliveredAt: optTime(v.DeliveredAt),
		CancelReason: v.CancelReason,
	}
	for _, l := range v.Lines {
		d.Lines = append(d.Lines, lineDTO{l.LineNo, l.CatalogItemID, l.ListingID, l.Quantity})
	}
	if c := v.Candidate; c != nil {
		d.Candidate = &candidateDTO{c.NodeID, c.PickupDepartureID, c.PickupCutoffAt.UTC(), append([]string{}, c.Legs...), c.DeliveryWaveID, c.ETA.UTC(), c.Cost}
	}
	for _, r := range v.Releases {
		d.Releases = append(d.Releases, releaseDTO{r.ReleaseNo, r.NodeID, r.ReleasedAt.UTC(), r.RequiredShipBy.UTC(), r.Outcome, r.ReasonCode})
	}
	for _, e := range v.ExcludedNodes {
		d.ExcludedNodes = append(d.ExcludedNodes, excludedDTO{e.NodeID, e.Reason})
	}
	return d
}
