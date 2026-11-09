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

// Stream returns the events stream; the three analytics-only events override it.
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

// FulfillmentReleasedToNode is raised by Release. EstimatedCostCents carries
// the money-denominated estimate of the released path in planner-declared
// cents and is nil when the estimate is incomplete (a component had no
// declared cost); consumers decode it tolerantly (ADR 0011).
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
	EstimatedCostCents      *int64
}

// EventName returns "FulfillmentReleasedToNode".
func (FulfillmentReleasedToNode) EventName() string { return "FulfillmentReleasedToNode" }

// FulfillmentSourcingDecided is raised by Release next to
// FulfillmentReleasedToNode: the analytics-only fact of the sourcing decision
// (ADR 0013). The regions are "" when the read model did not know them; Legs
// are the departures after the pickup; EstimatedCostCents follows ADR 0011
// (nil when incomplete); Units is the sum of the line quantities and
// CandidateCount the size of the candidate set the release chose from.
type FulfillmentSourcingDecided struct {
	Base
	OrderID             string
	GroupNo             int
	ReleaseNo           int
	NodeID              string
	NodeRegionID        string
	DestinationRegionID string
	Legs                int
	EstimatedCostCents  *int64
	Units               int
	CandidateCount      int
	DecidedAt           time.Time
}

// EventName returns "FulfillmentSourcingDecided".
func (FulfillmentSourcingDecided) EventName() string { return "FulfillmentSourcingDecided" }

// Stream returns the analytics stream.
func (FulfillmentSourcingDecided) Stream() string { return shared.StreamAnalytics }

// InRegion reports whether the node is in the destination's region; known is
// false when either region is unknown.
func (e FulfillmentSourcingDecided) InRegion() (inRegion, known bool) {
	if e.NodeRegionID == "" || e.DestinationRegionID == "" {
		return false, false
	}
	return e.NodeRegionID == e.DestinationRegionID, true
}

// CostComplete reports whether the estimate is complete (ADR 0011).
func (e FulfillmentSourcingDecided) CostComplete() bool { return e.EstimatedCostCents != nil }

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
	_ shared.Event = FulfillmentSourcingDecided{}
	_ shared.Event = FulfillmentReassigned{}
	_ shared.Event = FulfillmentLocked{}
	_ shared.Event = FulfillmentShipped{}
	_ shared.Event = FulfillmentDelivered{}
	_ shared.Event = FulfillmentUnsourceable{}
	_ shared.Event = FulfillmentCancelled{}
	_ shared.Event = FulfillmentCancelRefused{}
	_ shared.Event = FulfillmentConstraintRevised{}
)
