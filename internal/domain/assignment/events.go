package assignment

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

const entity = "assignment"

// Base is embedded in every event: the aggregate version and instant, and the
// assignment id (the CloudEvents subject and Kafka key).
type Base struct {
	shared.Header
	AssignmentID ID
}

// Entity returns "assignment".
func (Base) Entity() string { return entity }

// SubjectID returns the assignmentId.
func (b Base) SubjectID() string { return string(b.AssignmentID) }

// Stream returns the events stream; the two analytics-only events override it.
func (Base) Stream() string { return shared.StreamEvents }

func newBase(id ID, version int64, at time.Time) Base {
	return Base{Header: shared.Header{Version: version, At: shared.UTC(at)}, AssignmentID: id}
}

// FulfillmentQueued is raised by Queue. Analytics only (ADR 0002, A2).
type FulfillmentQueued struct {
	Base
	OrderID         string
	GroupNo         int
	PromiseID       string
	DeliverByLatest time.Time
	Lines           []Line
}

// EventName returns "FulfillmentQueued".
func (FulfillmentQueued) EventName() string { return "FulfillmentQueued" }

// Stream returns the analytics stream.
func (FulfillmentQueued) Stream() string { return shared.StreamAnalytics }

// FulfillmentReleasedToNode is raised by Release.
type FulfillmentReleasedToNode struct {
	Base
	ReleaseNo               int
	OrderID                 string
	GroupNo                 int
	NodeID                  string
	PickupDepartureID       string
	RequiredShipBy          time.Time
	PromisedDeliverByLatest time.Time
	Tier                    Tier
	Lines                   []Line
}

// EventName returns "FulfillmentReleasedToNode".
func (FulfillmentReleasedToNode) EventName() string { return "FulfillmentReleasedToNode" }

// FulfillmentReassigned is raised when the candidate node changes.
type FulfillmentReassigned struct {
	Base
	FromNodeID string
	ToNodeID   string
	Reason     string
}

// EventName returns "FulfillmentReassigned".
func (FulfillmentReassigned) EventName() string { return "FulfillmentReassigned" }

// FulfillmentLocked is raised by Lock.
type FulfillmentLocked struct {
	Base
	OrderID           string
	GroupNo           int
	PromiseID         string
	NodeID            string
	PickupDepartureID string
	LockedAt          time.Time
	LockReason        string
}

// EventName returns "FulfillmentLocked".
func (FulfillmentLocked) EventName() string { return "FulfillmentLocked" }

// FulfillmentShipped is raised by MarkShipped.
type FulfillmentShipped struct {
	Base
	OrderID         string
	GroupNo         int
	NodeID          string
	TrackingNumbers []string
	ShippedAt       time.Time
}

// EventName returns "FulfillmentShipped".
func (FulfillmentShipped) EventName() string { return "FulfillmentShipped" }

// FulfillmentDelivered is raised by MarkDelivered. Analytics only (ADR 0002, A2).
type FulfillmentDelivered struct {
	Base
	OrderID     string
	GroupNo     int
	DeliveredAt time.Time
}

// EventName returns "FulfillmentDelivered".
func (FulfillmentDelivered) EventName() string { return "FulfillmentDelivered" }

// Stream returns the analytics stream.
func (FulfillmentDelivered) Stream() string { return shared.StreamAnalytics }

// FulfillmentUnsourceable is raised when the candidate set is empty.
type FulfillmentUnsourceable struct {
	Base
	OrderID       string
	GroupNo       int
	PromiseID     string
	ExcludedNodes []ExcludedNode
	Reason        string
}

// EventName returns "FulfillmentUnsourceable".
func (FulfillmentUnsourceable) EventName() string { return "FulfillmentUnsourceable" }

// FulfillmentCancelled is raised by Cancel.
type FulfillmentCancelled struct {
	Base
	OrderID string
	GroupNo int
	Reason  string
}

// EventName returns "FulfillmentCancelled".
func (FulfillmentCancelled) EventName() string { return "FulfillmentCancelled" }

// FulfillmentCancelRefused is raised by RefuseCancel (contracts-delta E11).
type FulfillmentCancelRefused struct {
	Base
	OrderID string
	GroupNo int
	Reason  string
}

// EventName returns "FulfillmentCancelRefused".
func (FulfillmentCancelRefused) EventName() string { return "FulfillmentCancelRefused" }

// FulfillmentConstraintRevised is raised by ApplyRevisedPromise (contracts-delta E12).
type FulfillmentConstraintRevised struct {
	Base
	RevisionNo      int
	DeliverByLatest time.Time
}

// EventName returns "FulfillmentConstraintRevised".
func (FulfillmentConstraintRevised) EventName() string { return "FulfillmentConstraintRevised" }

var (
	_ shared.Event = FulfillmentQueued{}
	_ shared.Event = FulfillmentReleasedToNode{}
	_ shared.Event = FulfillmentReassigned{}
	_ shared.Event = FulfillmentLocked{}
	_ shared.Event = FulfillmentShipped{}
	_ shared.Event = FulfillmentDelivered{}
	_ shared.Event = FulfillmentUnsourceable{}
	_ shared.Event = FulfillmentCancelled{}
	_ shared.Event = FulfillmentCancelRefused{}
	_ shared.Event = FulfillmentConstraintRevised{}
)
