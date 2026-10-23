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

// Topics this service consumes (apis/asyncapi.yaml).
const (
	CustomerOrderingTopic       = "retailnetwork.customer-ordering.events"
	DeliveryPromiseTopic        = "retailnetwork.delivery-promise.events"
	LastMileDeliveryTopic       = "retailnetwork.last-mile-delivery.events"
	LinehaulTransportationTopic = "retailnetwork.linehaul-transportation.events"
	NodePerformanceTopic        = "retailnetwork.node-performance.events"
	NodeTopologyTopic           = "retailnetwork.node-topology.events"
	OfferAvailabilityTopic      = "retailnetwork.offer-availability.events"
	ShippingAndTrackingTopic    = "retailnetwork.shipping-and-tracking.events"
	VendorConnectivityTopic     = "retailnetwork.vendor-connectivity.events"
)

// UseCases is every use case a consumed event can reach.
type UseCases struct {
	Queue       *usecases.QueueAssignment
	Cancel      *usecases.CancelAssignment
	Revise      *usecases.ApplyRevisedPromise
	Refusal     *usecases.HandleNodeRefusal
	Lock        *usecases.LockAssignment
	Ship        *usecases.MarkShipped
	Deliver     *usecases.MarkDelivered
	PickupMoved *usecases.RecordPickupMoved
	Processed   *usecases.RecordProcessed
	Project     *usecases.Projector
}

// Handlers builds the type -> handler maps of each consumed topic.
type Handlers struct{ UC UseCases }

func meta(e ce.Event) usecases.Meta { return usecases.Meta{EventID: e.ID(), EventType: e.Type()} }

// typed registers fn under the full type `com.retailnetwork.<tier>.<ctx>.<entity>.<Name>`.
func typed[T any](m map[string]HandlerFunc, fullType string, fn func(ctx context.Context, e ce.Event, d T) error) {
	m[fullType] = func(ctx context.Context, e ce.Event) error {
		d, err := decodeData[T](e)
		if err != nil {
			return err
		}
		return fn(ctx, e, d)
	}
}

func discard[A any](_ A, err error) error { return err }

// CustomerOrdering handles CustomerOrderPlaced and CustomerOrderCancelled.
func (h Handlers) CustomerOrdering() map[string]HandlerFunc {
	const p = "com.retailnetwork.commerce.customer-ordering.customerorder."
	m := map[string]HandlerFunc{}
	type line struct {
		LineNo        int    `json:"lineNo"`
		CatalogItemID string `json:"catalogItemId"`
		Quantity      int    `json:"quantity"`
	}
	type group struct {
		GroupNo         int       `json:"groupNo"`
		PromiseID       string    `json:"promiseId"`
		Tier            string    `json:"tier"`
		WitnessNodeID   string    `json:"witnessNodeId"`
		DeliverByLatest time.Time `json:"deliverByLatest"`
		Lines           []line    `json:"lines"`
	}
	type placed struct {
		OrderID                 string  `json:"orderId"`
		DestinationPostalPrefix string  `json:"destinationPostalPrefix"`
		Groups                  []group `json:"groups"`
	}
	typed(m, p+"CustomerOrderPlaced", func(ctx context.Context, e ce.Event, d placed) error {
		cmd := usecases.QueueAssignmentCommand{Meta: meta(e), OrderID: d.OrderID, DestinationPostalPrefix: d.DestinationPostalPrefix}
		for _, g := range d.Groups {
			gc := usecases.GroupCommand{GroupNo: g.GroupNo, PromiseID: g.PromiseID, Tier: assignment.Tier(g.Tier), WitnessNodeID: g.WitnessNodeID, DeliverByLatest: g.DeliverByLatest}
			for _, l := range g.Lines {
				gc.Lines = append(gc.Lines, assignment.Line{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity})
			}
			cmd.Groups = append(cmd.Groups, gc)
		}
		return discard(h.UC.Queue.Execute(ctx, cmd))
	})
	typed(m, p+"CustomerOrderCancelled", func(ctx context.Context, e ce.Event, d struct {
		OrderID  string `json:"orderId"`
		Reason   string `json:"reason"`
		GroupNos []int  `json:"groupNos"`
	}) error {
		return discard(h.UC.Cancel.Execute(ctx, usecases.CancelAssignmentCommand{Meta: meta(e), OrderID: d.OrderID, GroupNos: d.GroupNos, Reason: d.Reason}))
	})
	return m
}

// DeliveryPromise handles PromiseRevised and PromiseBookExhausted.
func (h Handlers) DeliveryPromise() map[string]HandlerFunc {
	m := map[string]HandlerFunc{}
	typed(m, "com.retailnetwork.commerce.delivery-promise.promise.PromiseRevised", func(ctx context.Context, e ce.Event, d struct {
		PromiseID       string    `json:"promiseId"`
		RevisionNo      int       `json:"revisionNo"`
		DeliverByLatest time.Time `json:"deliverByLatest"`
		NodeID          string    `json:"nodeId"`
	}) error {
		return discard(h.UC.Revise.Execute(ctx, usecases.ApplyRevisedPromiseCommand{Meta: meta(e), PromiseID: d.PromiseID, RevisionNo: d.RevisionNo,
			DeliverByLatest: d.DeliverByLatest, WitnessNodeID: d.NodeID}))
	})
	typed(m, "com.retailnetwork.commerce.delivery-promise.promisebook.PromiseBookExhausted", func(ctx context.Context, e ce.Event, d struct {
		BookID            string `json:"bookId"`
		NodeID            string `json:"nodeId"`
		PickupDepartureID string `json:"pickupDepartureId"`
	}) error {
		return h.UC.Project.BookExhausted(ctx, meta(e), network.Pressure{BookID: d.BookID, NodeID: d.NodeID, PickupDepartureID: d.PickupDepartureID})
	})
	return m
}

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

// OfferAvailability handles listings and discontinued catalog items.
func (h Handlers) OfferAvailability() map[string]HandlerFunc {
	const p = "com.retailnetwork.commerce.offer-availability."
	m := map[string]HandlerFunc{}
	typed(m, p+"listing.ListingCreated", func(ctx context.Context, e ce.Event, d struct {
		ListingID     string `json:"listingId"`
		CatalogItemID string `json:"catalogItemId"`
		NodeID        string `json:"nodeId"`
	}) error {
		return h.UC.Project.ListingCreated(ctx, meta(e), d.ListingID, d.CatalogItemID, d.NodeID)
	})
	typed(m, p+"listing.ListingAvailabilityChanged", func(ctx context.Context, e ce.Event, d struct {
		ListingID     string    `json:"listingId"`
		CatalogItemID string    `json:"catalogItemId"`
		NodeID        string    `json:"nodeId"`
		AvailableQty  int       `json:"availableQty"`
		AsOf          time.Time `json:"asOf"`
	}) error {
		return h.UC.Project.ListingAvailabilityChanged(ctx, meta(e), d.ListingID, d.CatalogItemID, d.NodeID, d.AvailableQty, d.AsOf)
	})
	for name, suppressed := range map[string]bool{"ListingSuppressed": true, "ListingRestored": false} {
		typed(m, p+"listing."+name, func(ctx context.Context, e ce.Event, d struct {
			ListingID string `json:"listingId"`
		}) error {
			return h.UC.Project.ListingSuppressionChanged(ctx, meta(e), d.ListingID, suppressed)
		})
	}
	typed(m, p+"catalogitem.CatalogItemDiscontinued", func(ctx context.Context, e ce.Event, d struct {
		CatalogItemID string `json:"catalogItemId"`
	}) error {
		return h.UC.Project.CatalogItemDiscontinued(ctx, meta(e), d.CatalogItemID)
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

// Topics pairs each consumed topic with its handlers, keyed by the consumer-group
// environment variable named in apis/asyncapi.yaml.
func (h Handlers) Topics() []TopicHandlers {
	return []TopicHandlers{
		{"customer-ordering", CustomerOrderingTopic, "CUSTOMER_ORDERING_CONSUMER_GROUP", h.CustomerOrdering()},
		{"delivery-promise", DeliveryPromiseTopic, "DELIVERY_PROMISE_CONSUMER_GROUP", h.DeliveryPromise()},
		{"last-mile-delivery", LastMileDeliveryTopic, "LAST_MILE_DELIVERY_CONSUMER_GROUP", h.LastMileDelivery()},
		{"linehaul-transportation", LinehaulTransportationTopic, "LINEHAUL_TRANSPORTATION_CONSUMER_GROUP", h.LinehaulTransportation()},
		{"node-performance", NodePerformanceTopic, "NODE_PERFORMANCE_CONSUMER_GROUP", h.NodePerformance()},
		{"node-topology", NodeTopologyTopic, "NODE_TOPOLOGY_CONSUMER_GROUP", h.NodeTopology()},
		{"offer-availability", OfferAvailabilityTopic, "OFFER_AVAILABILITY_CONSUMER_GROUP", h.OfferAvailability()},
		{"shipping-and-tracking", ShippingAndTrackingTopic, "SHIPPING_AND_TRACKING_CONSUMER_GROUP", h.ShippingAndTracking()},
		{"vendor-connectivity", VendorConnectivityTopic, "VENDOR_CONNECTIVITY_CONSUMER_GROUP", h.VendorConnectivity()},
	}
}

// TopicHandlers is one consumed topic.
type TopicHandlers struct {
	Name, Topic, GroupEnv string
	Handlers              map[string]HandlerFunc
}
