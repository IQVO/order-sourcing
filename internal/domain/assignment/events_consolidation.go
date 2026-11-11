package assignment

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// MergedMember is one member of a merged release as the events list it: the
// ids of its group and promise, its promised latest delivery and its own
// (listing-resolved) lines. The carrier is always the first member.
type MergedMember struct {
	ID              ID
	OrderID         string
	GroupNo         int
	PromiseID       string
	DeliverByLatest time.Time
	Lines           []Line
}

// Units is the sum of the member's line quantities.
func (m MergedMember) Units() int { return unitsOf(m.Lines) }

// AssignmentsConsolidated is raised by the carrier's ReleaseConsolidated.
// Analytics only (ADR 0015): the units-per-package and packages-per-order
// reports read it; nothing consumes it.
type AssignmentsConsolidated struct {
	Base
	ReleaseNo      int
	NodeID         string
	Members        []MergedMember
	ConsolidatedAt time.Time
}

// EventName returns "AssignmentsConsolidated".
func (AssignmentsConsolidated) EventName() string { return "AssignmentsConsolidated" }

// Stream returns the analytics stream.
func (AssignmentsConsolidated) Stream() string { return shared.StreamAnalytics }

// Units is the sum of every member line quantity.
func (e AssignmentsConsolidated) Units() int {
	units := 0
	for _, m := range e.Members {
		units += m.Units()
	}
	return units
}

// AssignmentsDeconsolidated is raised by the carrier when its merge is
// dissolved: reason is PROMISE_REVISED, PURCHASE_ORDER_REJECTED or
// PURCHASE_ORDER_EXPIRED; TriggerAssignmentID is the revised member (empty for
// a purchase order refusal). Analytics only (ADR 0015).
type AssignmentsDeconsolidated struct {
	Base
	ReleaseNo           int
	Reason              string
	TriggerAssignmentID ID
	MemberAssignmentIDs []ID
	DeconsolidatedAt    time.Time
}

// EventName returns "AssignmentsDeconsolidated".
func (AssignmentsDeconsolidated) EventName() string { return "AssignmentsDeconsolidated" }

// Stream returns the analytics stream.
func (AssignmentsDeconsolidated) Stream() string { return shared.StreamAnalytics }

// FulfillmentReleaseWithdrawn is raised by the carrier when order-sourcing
// takes back a published release (a promise revision excluded the merged
// delivery); vendor connectivity cancels the purchase order. Integration event
// (ADR 0015).
type FulfillmentReleaseWithdrawn struct {
	Base
	ReleaseNo   int
	OrderID     string
	GroupNo     int
	Reason      string
	WithdrawnAt time.Time
}

// EventName returns "FulfillmentReleaseWithdrawn".
func (FulfillmentReleaseWithdrawn) EventName() string { return "FulfillmentReleaseWithdrawn" }

var (
	_ shared.Event = AssignmentsConsolidated{}
	_ shared.Event = AssignmentsDeconsolidated{}
	_ shared.Event = FulfillmentReleaseWithdrawn{}
)
