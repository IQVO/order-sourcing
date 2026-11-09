// Package report is the self-contained read-model region of the analytics read
// side (ADR 0013): the facts the projector folds in, the shapes of the
// sourcing reports, the KPI rules (shares, rates, means, percentile ranks),
// the date-range rules, the freshness lag, and the writer/reader ports the
// analytical store implements. It imports no other internal package (arch
// test): the payload vocabulary below is hand-mirrored from apis/asyncapi.yaml,
// never imported from the domain, so the OLTP model can never leak into the
// projection.
package report

import (
	"context"
	"errors"
	"time"
)

// Kind is which of the three projected events a fact came from. The names are
// the CloudEvents event names (the last segment of `type`).
type Kind string

// The three projected events of this context's own topics.
const (
	KindFulfillmentQueued       Kind = "FulfillmentQueued"
	KindFulfillmentDecided      Kind = "FulfillmentSourcingDecided"
	KindFulfillmentUnsourceable Kind = "FulfillmentUnsourceable"
)

// Event is one decoded analytics fact ready to project. It is the union of the
// three payloads; only the fields of its Kind are set.
type Event struct {
	Kind    Kind
	EventID string    // CloudEvents id: the idempotency key
	At      time.Time // CloudEvents time: when the use case committed it (freshness)

	// AssignmentID (= subject) and OrderID are on every projected payload.
	AssignmentID string
	OrderID      string
	// GroupNo is the shipment group (0 = omitted on a decision).
	GroupNo int
	// OccurredAt is the business instant of the fact: the decision's
	// `decidedAt`, otherwise the CloudEvents time (FulfillmentQueued and
	// FulfillmentUnsourceable carry no instant of their own). The reports
	// bucket days on it.
	OccurredAt time.Time

	// FulfillmentSourcingDecided.
	Decision Decision
}

// Decision is the payload of FulfillmentSourcingDecided beyond the ids. The
// regions are "" and InRegion nil when unknown; EstimatedCostCents is nil
// exactly when CostComplete is false (ADR 0011).
type Decision struct {
	ReleaseNo           int
	NodeID              string
	NodeRegionID        string
	DestinationRegionID string
	InRegion            *bool
	Legs                int
	EstimatedCostCents  *int64
	CostComplete        bool
	Units               int
	CandidateCount      int
}

// Projection is the WRITER port: Apply records the event id and projects the
// fact in ONE transaction. applied is false when the id was already recorded
// (a replay): nothing changed. An error wrapping ErrRejected is deterministic
// (the store can never accept this event: retry is pointless); any other error
// is transient and the same event may be retried.
type Projection interface {
	Apply(ctx context.Context, e Event) (applied bool, err error)
}

// ErrRejected marks an event the analytical store deterministically refuses
// (a data-exception or integrity-violation class error).
var ErrRejected = errors.New("report: event rejected by the analytical store")

// Reader is the READER port. Every method answers for the half-open instant
// range [r.From, r.To) of the business instants, bucketed by UTC day. Results
// are ordered by day and never nil.
type Reader interface {
	SourcingMix(ctx context.Context, r Range) ([]MixDay, error)
	OrderSplits(ctx context.Context, r Range) ([]SplitDay, error)
	CostToServe(ctx context.Context, r Range) ([]CostDay, error)
	// LastEventAt is the CloudEvents time of the newest event the projection
	// applied (nil while it applied none): the basis of the freshness lag.
	LastEventAt(ctx context.Context) (*time.Time, error)
}

// MixDay is what the store counts for one UTC day of release: the releases
// (decisions), those in and out of the destination's region and those with an
// unknown region, the sum of their legs, the re-releases (releaseNo > 1), and
// the unsourceable declarations of that day.
type MixDay struct {
	Day           time.Time
	Releases      int
	InRegion      int
	OutOfRegion   int
	UnknownRegion int
	Legs          int
	Reassignments int
	Unsourceable  int
}

// SplitDay is what the store counts for one UTC day of first queueing: the
// distinct orders, their queued groups and the orders with more than one group.
type SplitDay struct {
	Day         time.Time
	Orders      int
	Groups      int
	SplitOrders int
}

// CostDay is what the store aggregates for one UTC day over the LAST decision
// of each assignment: the decisions, the complete ones, the sum of their cents
// and units, and the cost percentiles at CostP50 and CostP90 of the complete
// ones (nil without one).
type CostDay struct {
	Day       time.Time
	Decisions int
	Complete  int
	CostCents int64
	Units     int
	P50       *float64
	P90       *float64
}
