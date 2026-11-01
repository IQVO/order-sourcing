package kafka

import (
	"context"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// LastMileDelivery handles the delivery-wave events.
func (h Handlers) LastMileDelivery() map[string]HandlerFunc {
	const p = "com.retailnetwork.logistics.last-mile-delivery.wave."
	m := map[string]HandlerFunc{}
	typed(m, p+"DeliveryWaveScheduled", func(ctx context.Context, e ce.Event, d struct {
		WaveID         string    `json:"waveId"`
		StationNodeID  string    `json:"stationNodeId"`
		InductCutoffAt time.Time `json:"inductCutoffAt"`
		WindowStart    time.Time `json:"windowStart"`
		WindowEnd      time.Time `json:"windowEnd"`
		StopCapacity   int       `json:"stopCapacity"`
	}) error {
		return h.UC.Project.WaveScheduled(ctx, meta(e), network.Wave{ID: d.WaveID, StationID: d.StationNodeID, InductCutoffAt: d.InductCutoffAt,
			WindowStart: d.WindowStart, WindowEnd: d.WindowEnd, Capacity: d.StopCapacity})
	})
	typed(m, p+"DeliveryWaveCapacityChanged", func(ctx context.Context, e ce.Event, d struct {
		WaveID         string `json:"waveId"`
		RemainingStops int    `json:"remainingStops"`
	}) error {
		return h.UC.Project.WaveCapacityChanged(ctx, meta(e), d.WaveID, d.RemainingStops)
	})
	typed(m, p+"DeliveryWaveCancelled", func(ctx context.Context, e ce.Event, d struct {
		WaveID string `json:"waveId"`
	}) error {
		return h.UC.Project.WaveCancelled(ctx, meta(e), d.WaveID)
	})
	return m
}

// LinehaulTransportation handles departures and booking outcomes.
func (h Handlers) LinehaulTransportation() map[string]HandlerFunc {
	const p = "com.retailnetwork.logistics.linehaul-transportation.departure."
	m := map[string]HandlerFunc{}
	typed(m, p+"DepartureScheduled", func(ctx context.Context, e ce.Event, d struct {
		DepartureID       string    `json:"departureId"`
		LaneID            string    `json:"laneId"`
		Kind              string    `json:"kind"`
		OriginNodeID      string    `json:"originNodeId"`
		DestinationNodeID string    `json:"destinationNodeId"`
		CutoffAt          time.Time `json:"cutoffAt"`
		DepartsAt         time.Time `json:"departsAt"`
		ArrivesAt         time.Time `json:"arrivesAt"`
		CapacityPackages  int       `json:"capacityPackages"`
	}) error {
		return h.UC.Project.DepartureScheduled(ctx, meta(e), network.Departure{ID: d.DepartureID, LaneID: d.LaneID, Kind: d.Kind, Origin: d.OriginNodeID,
			Destination: d.DestinationNodeID, CutoffAt: d.CutoffAt, DepartsAt: d.DepartsAt, ArrivesAt: d.ArrivesAt, Capacity: d.CapacityPackages})
	})
	typed(m, p+"DepartureCapacityChanged", func(ctx context.Context, e ce.Event, d struct {
		DepartureID       string `json:"departureId"`
		RemainingPackages int    `json:"remainingPackages"`
	}) error {
		return h.UC.Project.DepartureCapacityChanged(ctx, meta(e), d.DepartureID, d.RemainingPackages)
	})
	typed(m, p+"DepartureCancelled", func(ctx context.Context, e ce.Event, d struct {
		DepartureID string `json:"departureId"`
	}) error {
		return h.UC.Project.DepartureCancelled(ctx, meta(e), d.DepartureID)
	})
	typed(m, p+"BookingMoved", func(ctx context.Context, e ce.Event, d struct {
		AssignmentID    string `json:"assignmentId"`
		FromDepartureID string `json:"fromDepartureId"`
		ToDepartureID   string `json:"toDepartureId"`
	}) error {
		return discard(h.UC.PickupMoved.Execute(ctx, usecases.RecordPickupMovedCommand{Meta: meta(e), AssignmentID: assignment.ID(d.AssignmentID),
			FromDeparture: d.FromDepartureID, ToDeparture: d.ToDepartureID}))
	})
	// BookingFailed needs no state change here (ADR 0002, A19): the departure's own
	// cancellation or capacity events drive re-optimisation. It is only recorded as processed.
	typed(m, p+"BookingFailed", func(ctx context.Context, e ce.Event, _ struct {
		AssignmentID string `json:"assignmentId"`
	}) error {
		return discard(h.UC.Processed.Execute(ctx, meta(e)))
	})
	return m
}

// NodeTopology handles node, lane, service-area and region events.
func (h Handlers) NodeTopology() map[string]HandlerFunc {
	const p = "com.retailnetwork.logistics.node-topology."
	m := map[string]HandlerFunc{}
	typed(m, p+"node.NodeRegistered", func(ctx context.Context, e ce.Event, d struct {
		NodeID   string `json:"nodeId"`
		NodeType string `json:"nodeType"`
		Subtype  string `json:"subtype"`
		Operator string `json:"operator"`
		RegionID string `json:"regionId"`
	}) error {
		return h.UC.Project.NodeRegistered(ctx, meta(e), d.NodeID, network.Registered{Type: d.NodeType, Subtype: d.Subtype, Operator: d.Operator, RegionID: d.RegionID, At: e.Time()})
	})
	typed(m, p+"node.NodeOperationalProfileDeclared", func(ctx context.Context, e ce.Event, d struct {
		NodeID              string `json:"nodeId"`
		ProfileRevision     int    `json:"profileRevision"`
		HandlingTimeMinutes int    `json:"handlingTimeMinutes"`
	}) error {
		return h.UC.Project.NodeProfileDeclared(ctx, meta(e), d.NodeID, d.ProfileRevision, d.HandlingTimeMinutes, 0)
	})
	status := func(name, state, atField string) {
		typed(m, p+"node."+name, func(ctx context.Context, e ce.Event, d map[string]any) error {
			id, _ := d["nodeId"].(string)
			at := e.Time()
			if s, ok := d[atField].(string); ok {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					at = t
				}
			}
			return h.UC.Project.NodeStatusChanged(ctx, meta(e), id, state, at)
		})
	}
	status("NodeActivated", network.StatusActive, "activatedAt")
	status("NodeSuspended", network.StatusSuspended, "suspendedAt")
	status("NodeRetired", network.StatusRetired, "retiredAt")
	typed(m, p+"lane.LaneOpened", func(ctx context.Context, e ce.Event, d struct {
		LaneID            string `json:"laneId"`
		OriginNodeID      string `json:"originNodeId"`
		DestinationNodeID string `json:"destinationNodeId"`
		Mode              string `json:"mode"`
	}) error {
		return h.UC.Project.LaneOpened(ctx, meta(e), network.Lane{ID: d.LaneID, Origin: d.OriginNodeID, Destination: d.DestinationNodeID, Mode: d.Mode})
	})
	typed(m, p+"lane.LaneClosed", func(ctx context.Context, e ce.Event, d struct {
		LaneID string `json:"laneId"`
	}) error {
		return h.UC.Project.LaneClosed(ctx, meta(e), d.LaneID)
	})
	typed(m, p+"servicearea.ServiceAreaAssigned", func(ctx context.Context, e ce.Event, d struct {
		PostalPrefix          string `json:"postalPrefix"`
		DeliveryStationNodeID string `json:"deliveryStationNodeId"`
		RegionID              string `json:"regionId"`
		AssignmentVersion     int    `json:"assignmentVersion"`
	}) error {
		return h.UC.Project.ServiceAreaAssigned(ctx, meta(e), network.ServiceArea{PostalPrefix: d.PostalPrefix, StationID: d.DeliveryStationNodeID, RegionID: d.RegionID, Version: d.AssignmentVersion})
	})
	typed(m, p+"region.RegionDefined", func(ctx context.Context, e ce.Event, d struct {
		RegionID string `json:"regionId"`
		Code     string `json:"code"`
		Name     string `json:"name"`
	}) error {
		return h.UC.Project.RegionDefined(ctx, meta(e), network.Region{ID: d.RegionID, Code: d.Code, Name: d.Name})
	})
	return m
}

// ShippingAndTracking handles PackageDelivered.
func (h Handlers) ShippingAndTracking() map[string]HandlerFunc {
	m := map[string]HandlerFunc{}
	typed(m, "com.retailnetwork.logistics.shipping-and-tracking.journey.PackageDelivered", func(ctx context.Context, e ce.Event, d struct {
		TrackingNumber string    `json:"trackingNumber"`
		AssignmentID   string    `json:"assignmentId"`
		DeliveredAt    time.Time `json:"deliveredAt"`
	}) error {
		return discard(h.UC.Deliver.Execute(ctx, usecases.MarkDeliveredCommand{Meta: meta(e), AssignmentID: assignment.ID(d.AssignmentID),
			TrackingNumber: d.TrackingNumber, DeliveredAt: d.DeliveredAt}))
	})
	return m
}
