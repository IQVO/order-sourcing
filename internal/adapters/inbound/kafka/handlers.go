package kafka

import (
	"context"
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
