package kafka

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/application/outbox"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Topics of this service (retailnetwork.order-sourcing.<stream>).
const (
	TopicEvents    = "retailnetwork.order-sourcing.events"
	TopicAnalytics = "retailnetwork.order-sourcing.analytics"
)

// schemaVersion is the dataschema version of every payload below.
const schemaVersion = 1

// Wire payloads (CloudEvents `data`, camelCase JSON), exactly the ones pinned
// in apis/asyncapi.yaml. Arrays are always present; timestamps are RFC 3339 UTC.

type queuedLine struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	Quantity      int    `json:"quantity"`
}

type releasedLine struct {
	CatalogItemID string `json:"catalogItemId"`
	ListingID     string `json:"listingId"`
	// OwnerRef is the seller whose stock the line takes, omitted when
	// operator-owned (ADR 0012).
	OwnerRef string `json:"ownerRef,omitempty"`
	Quantity int    `json:"quantity"`
}

type excludedNode struct {
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}

type queuedData struct {
	AssignmentID    string       `json:"assignmentId"`
	OrderID         string       `json:"orderId"`
	GroupNo         int          `json:"groupNo"`
	PromiseID       string       `json:"promiseId"`
	DeliverByLatest time.Time    `json:"deliverByLatest"`
	Lines           []queuedLine `json:"lines"`
}

type releasedData struct {
	AssignmentID            string         `json:"assignmentId"`
	ReleaseNo               int            `json:"releaseNo"`
	OrderID                 string         `json:"orderId"`
	GroupNo                 int            `json:"groupNo"`
	NodeID                  string         `json:"nodeId"`
	PickupDepartureID       string         `json:"pickupDepartureId"`
	RequiredShipBy          time.Time      `json:"requiredShipBy"`
	PromisedDeliverByLatest time.Time      `json:"promisedDeliverByLatest"`
	Tier                    string         `json:"tier"`
	Lines                   []releasedLine `json:"lines"`
	// EstimatedCostCents is omitted when the estimate is incomplete (ADR 0011).
	EstimatedCostCents *int64 `json:"estimatedCostCents,omitempty"`
	// PolicyVersion is omitted when no sourcing policy was published, and
	// EffectiveCostCents unless a surcharge changed the cost (ADR 0014).
	PolicyVersion      int    `json:"policyVersion,omitempty"`
	EffectiveCostCents *int64 `json:"effectiveCostCents,omitempty"`
}

// decidedData is the analytics-only sourcing decision (ADR 0013). The regions
// and inRegion are omitted when unknown; estimatedCostCents is omitted when the
// estimate is incomplete, exactly when costComplete is false (ADR 0011).
type decidedData struct {
	AssignmentID        string    `json:"assignmentId"`
	OrderID             string    `json:"orderId"`
	GroupNo             int       `json:"groupNo,omitempty"`
	ReleaseNo           int       `json:"releaseNo"`
	NodeID              string    `json:"nodeId"`
	NodeRegionID        string    `json:"nodeRegionId,omitempty"`
	DestinationRegionID string    `json:"destinationRegionId,omitempty"`
	InRegion            *bool     `json:"inRegion,omitempty"`
	Legs                int       `json:"legs"`
	EstimatedCostCents  *int64    `json:"estimatedCostCents,omitempty"`
	CostComplete        bool      `json:"costComplete"`
	Units               int       `json:"units"`
	CandidateCount      int       `json:"candidateCount"`
	DecidedAt           time.Time `json:"decidedAt"`
	PolicyVersion       int       `json:"policyVersion,omitempty"`
	EffectiveCostCents  *int64    `json:"effectiveCostCents,omitempty"`
}

func decided(e assignment.FulfillmentSourcingDecided) decidedData {
	var inRegion *bool
	if in, known := e.InRegion(); known {
		inRegion = &in
	}
	return decidedData{
		AssignmentID: e.SubjectID(), OrderID: e.OrderID, GroupNo: e.GroupNo, ReleaseNo: e.ReleaseNo, NodeID: e.NodeID,
		NodeRegionID: e.NodeRegionID, DestinationRegionID: e.DestinationRegionID, InRegion: inRegion, Legs: e.Legs,
		EstimatedCostCents: e.EstimatedCostCents, CostComplete: e.CostComplete(), Units: e.Units, CandidateCount: e.CandidateCount,
		DecidedAt: utc(e.DecidedAt), PolicyVersion: e.PolicyVersion, EffectiveCostCents: e.EffectiveCostCents,
	}
}

type reassignedData struct {
	AssignmentID      string `json:"assignmentId"`
	FromNodeID        string `json:"fromNodeId"`
	ToNodeID          string `json:"toNodeId"`
	Reason            string `json:"reason"`
	AssignmentVersion int64  `json:"assignmentVersion"`
}

type lockedData struct {
	AssignmentID      string    `json:"assignmentId"`
	OrderID           string    `json:"orderId"`
	GroupNo           int       `json:"groupNo"`
	PromiseID         string    `json:"promiseId"`
	NodeID            string    `json:"nodeId"`
	PickupDepartureID string    `json:"pickupDepartureId"`
	LockedAt          time.Time `json:"lockedAt"`
	LockReason        string    `json:"lockReason"`
}

type shippedData struct {
	AssignmentID    string    `json:"assignmentId"`
	OrderID         string    `json:"orderId"`
	GroupNo         int       `json:"groupNo"`
	NodeID          string    `json:"nodeId"`
	TrackingNumbers []string  `json:"trackingNumbers"`
	ShippedAt       time.Time `json:"shippedAt"`
}

type deliveredData struct {
	AssignmentID string    `json:"assignmentId"`
	OrderID      string    `json:"orderId"`
	GroupNo      int       `json:"groupNo"`
	DeliveredAt  time.Time `json:"deliveredAt"`
}

type unsourceableData struct {
	AssignmentID  string         `json:"assignmentId"`
	OrderID       string         `json:"orderId"`
	GroupNo       int            `json:"groupNo"`
	PromiseID     string         `json:"promiseId"`
	ExcludedNodes []excludedNode `json:"excludedNodes"`
	Reason        string         `json:"reason"`
}

type cancelledData struct {
	AssignmentID string `json:"assignmentId"`
	OrderID      string `json:"orderId"`
	GroupNo      int    `json:"groupNo"`
	Reason       string `json:"reason"`
}

type revisedData struct {
	AssignmentID    string    `json:"assignmentId"`
	RevisionNo      int       `json:"revisionNo"`
	DeliverByLatest time.Time `json:"deliverByLatest"`
}

// policyPublishedData is the analytics-only SourcingPolicyPublished payload
// (ADR 0014): subject and Kafka key are the constant "policy".
type policyPublishedData struct {
	Version                   int       `json:"version"`
	CrossRegionSurchargeCents int       `json:"crossRegionSurchargeCents"`
	MaxCostDeviationPercent   int       `json:"maxCostDeviationPercent"`
	PublishedAt               time.Time `json:"publishedAt"`
}

// Encoder turns a domain event into one outbox.Message holding the CloudEvents
// bytes, the Kafka key (the assignment id), the topic of the event's stream and
// the content-type header. The CloudEvents `id` is minted HERE, once, and
// persisted with the outbox row, so a relay retry republishes the same id.
type Encoder struct {
	// NewID mints a CloudEvents id; uuid.NewString when nil.
	NewID func() string
}

// Encode implements the outbox encoder contract of the Postgres outbox.
func (e Encoder) Encode(ev shared.Event) (outbox.Message, error) {
	data, err := payload(ev)
	if err != nil {
		return outbox.Message{}, err
	}
	topic := TopicEvents
	if ev.Stream() == shared.StreamAnalytics {
		topic = TopicAnalytics
	}
	newID := e.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	id := newID()
	value, err := cloudevents.New(cloudevents.Spec{
		ID: id, Entity: ev.Entity(), EventName: ev.EventName(), Subject: ev.SubjectID(), Time: ev.OccurredAt(),
		Stream: ev.Stream(), Version: schemaVersion, Data: data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode %s: %w", ev.EventName(), err)
	}
	h := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID: id, Topic: topic, EventType: cloudevents.Type(ev.Entity(), ev.EventName()), Subject: ev.SubjectID(),
		Key: []byte(ev.SubjectID()), DataSchema: cloudevents.DataSchema(ev.Stream(), ev.EventName(), schemaVersion), Value: value,
		Headers: []outbox.Header{{Key: h.Key, Value: string(h.Value)}},
	}, nil
}

func utc(t time.Time) time.Time { return t.UTC() }

func queuedLines(in []assignment.Line) []queuedLine {
	out := make([]queuedLine, 0, len(in))
	for _, l := range in {
		out = append(out, queuedLine{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity})
	}
	return out
}

func releasedLines(in []assignment.Line) []releasedLine {
	out := make([]releasedLine, 0, len(in))
	for _, l := range in {
		out = append(out, releasedLine{CatalogItemID: l.CatalogItemID, ListingID: l.ListingID, OwnerRef: l.OwnerRef, Quantity: l.Quantity})
	}
	return out
}

func excludedNodes(in []assignment.ExcludedNode) []excludedNode {
	out := make([]excludedNode, 0, len(in))
	for _, n := range in {
		out = append(out, excludedNode{NodeID: n.NodeID, Reason: n.Reason})
	}
	return out
}

func tracking(in []string) []string { return append([]string{}, in...) }

// payload maps one domain event to its wire data.
func payload(ev shared.Event) (any, error) {
	id := ev.SubjectID()
	switch e := ev.(type) {
	case assignment.FulfillmentQueued:
		return queuedData{id, e.OrderID, e.GroupNo, e.PromiseID, utc(e.DeliverByLatest), queuedLines(e.Lines)}, nil
	case assignment.FulfillmentReleasedToNode:
		return releasedData{id, e.ReleaseNo, e.OrderID, e.GroupNo, e.NodeID, e.PickupDepartureID, utc(e.RequiredShipBy),
			utc(e.PromisedDeliverByLatest), string(e.Tier), releasedLines(e.Lines), e.EstimatedCostCents, e.PolicyVersion, e.EffectiveCostCents}, nil
	case assignment.FulfillmentSourcingDecided:
		return decided(e), nil
	case assignment.FulfillmentReassigned:
		return reassignedData{id, e.FromNodeID, e.ToNodeID, e.Reason, e.AggregateVersion()}, nil
	case assignment.FulfillmentLocked:
		return lockedData{id, e.OrderID, e.GroupNo, e.PromiseID, e.NodeID, e.PickupDepartureID, utc(e.LockedAt), e.LockReason}, nil
	case assignment.FulfillmentShipped:
		return shippedData{id, e.OrderID, e.GroupNo, e.NodeID, tracking(e.TrackingNumbers), utc(e.ShippedAt)}, nil
	case assignment.FulfillmentDelivered:
		return deliveredData{id, e.OrderID, e.GroupNo, utc(e.DeliveredAt)}, nil
	case assignment.FulfillmentUnsourceable:
		return unsourceableData{id, e.OrderID, e.GroupNo, e.PromiseID, excludedNodes(e.ExcludedNodes), e.Reason}, nil
	case assignment.FulfillmentCancelled:
		return cancelledData{id, e.OrderID, e.GroupNo, e.Reason}, nil
	case assignment.FulfillmentCancelRefused:
		return cancelledData{id, e.OrderID, e.GroupNo, e.Reason}, nil
	case assignment.FulfillmentConstraintRevised:
		return revisedData{id, e.RevisionNo, utc(e.DeliverByLatest)}, nil
	case policy.SourcingPolicyPublished:
		return policyPublishedData{e.Number, e.CrossRegionSurchargeCents, e.MaxCostDeviationPercent, utc(e.PublishedAt)}, nil
	default:
		return nil, fmt.Errorf("kafka encoder: no wire form for %T", ev)
	}
}
