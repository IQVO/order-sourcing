package kafka

import (
	"context"
	"strings"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// NodePerformance handles NodeScorecardUpdated.
func (h Handlers) NodePerformance() map[string]HandlerFunc {
	m := map[string]HandlerFunc{}
	typed(m, "com.retailnetwork.partner.node-performance.scorecard.NodeScorecardUpdated", func(ctx context.Context, e ce.Event, d struct {
		NodeID     string    `json:"nodeId"`
		Window     string    `json:"window"`
		Grade      string    `json:"grade"`
		ComputedAt time.Time `json:"computedAt"`
	}) error {
		return h.UC.Project.ScorecardUpdated(ctx, meta(e), network.Scorecard{NodeID: d.NodeID, Window: d.Window, Grade: d.Grade, ComputedAt: d.ComputedAt})
	})
	return m
}

// VendorConnectivity handles the purchase-order outcomes and the shipment confirmation.
func (h Handlers) VendorConnectivity() map[string]HandlerFunc {
	const p = "com.retailnetwork.partner.vendor-connectivity.purchaseorder."
	m := map[string]HandlerFunc{}
	type po struct {
		AssignmentID string    `json:"assignmentId"`
		ReleaseNo    int       `json:"releaseNo"`
		AcceptedAt   time.Time `json:"acceptedAt"`
		ReasonCode   string    `json:"reasonCode"`
	}
	typed(m, p+"PurchaseOrderAccepted", func(ctx context.Context, e ce.Event, d po) error {
		return discard(h.UC.Lock.Execute(ctx, usecases.LockAssignmentCommand{Meta: meta(e), AssignmentID: assignment.ID(d.AssignmentID), ReleaseNo: d.ReleaseNo, AcceptedAt: d.AcceptedAt}))
	})
	refusal := func(name string, outcome assignment.Outcome) {
		typed(m, p+name, func(ctx context.Context, e ce.Event, d po) error {
			return discard(h.UC.Refusal.Execute(ctx, usecases.HandleNodeRefusalCommand{Meta: meta(e), AssignmentID: assignment.ID(d.AssignmentID),
				ReleaseNo: d.ReleaseNo, Outcome: outcome, ReasonCode: strings.TrimSpace(d.ReasonCode)}))
		})
	}
	refusal("PurchaseOrderRejected", assignment.OutcomeRejected)
	refusal("PurchaseOrderExpired", assignment.OutcomeExpired)
	typed(m, p+"VendorShipmentConfirmed", func(ctx context.Context, e ce.Event, d struct {
		AssignmentID    string    `json:"assignmentId"`
		TrackingNumbers []string  `json:"trackingNumbers"`
		ShippedAt       time.Time `json:"shippedAt"`
	}) error {
		return discard(h.UC.Ship.Execute(ctx, usecases.MarkShippedCommand{Meta: meta(e), AssignmentID: assignment.ID(d.AssignmentID), TrackingNumbers: d.TrackingNumbers, ShippedAt: d.ShippedAt}))
	})
	return m
}
