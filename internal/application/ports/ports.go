// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary is the
// sentinels of errors.go and the query structs of internal/application/repository.
//
// Optimistic concurrency: every Save expects the stored version to be
// aggregate.Version()-1; a create (Version() 1) expects that the id is not
// stored. Any mismatch is ErrConcurrentUpdate. Inside a UnitOfWork every
// repository joins the transaction carried by the ctx the unit handed to its
// function.
package ports

import (
	"context"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// AssignmentRepository persists FulfillmentAssignment aggregates.
type AssignmentRepository interface {
	// Get returns the assignment, or ErrNotFound.
	Get(ctx context.Context, id assignment.ID) (*assignment.Assignment, error)
	// FindByGroup returns the assignment of (orderId, groupNo), or ErrNotFound.
	FindByGroup(ctx context.Context, orderID string, groupNo int) (*assignment.Assignment, error)
	// FindByPromise returns the assignment that keeps the promise, or ErrNotFound.
	FindByPromise(ctx context.Context, promiseID string) (*assignment.Assignment, error)
	// List returns the assignments matching the filter ordered by id ascending
	// (at most filter.Limit, MaxLimit when zero or larger); no match is an
	// empty list, never an error.
	List(ctx context.Context, filter repository.AssignmentFilter) ([]*assignment.Assignment, error)
	// Due returns the ids of QUEUED assignments whose releaseAt is not after
	// now, ordered by releaseAt then id, at most limit.
	Due(ctx context.Context, now time.Time, limit int) ([]assignment.ID, error)
	// Queued returns the ids of QUEUED assignments after the given id, ordered
	// by id, at most limit (the keyset the re-optimisation sweep pages by).
	Queued(ctx context.Context, after assignment.ID, limit int) ([]assignment.ID, error)
	// Save persists the assignment under the concurrency rule of the package
	// comment. A second assignment of the same (orderId, groupNo) is
	// ErrDuplicateGroup.
	Save(ctx context.Context, a *assignment.Assignment) error
}

// ReadModels is the write side of the five local read models. Every Get
// returns ErrNotFound for a row that was never stored.
type ReadModels interface {
	GetNode(ctx context.Context, id string) (network.Node, error)
	PutNode(ctx context.Context, n network.Node) error
	GetLane(ctx context.Context, id string) (network.Lane, error)
	PutLane(ctx context.Context, l network.Lane) error
	GetDeparture(ctx context.Context, id string) (network.Departure, error)
	PutDeparture(ctx context.Context, d network.Departure) error
	GetWave(ctx context.Context, id string) (network.Wave, error)
	PutWave(ctx context.Context, w network.Wave) error
	GetServiceArea(ctx context.Context, postalPrefix string) (network.ServiceArea, error)
	PutServiceArea(ctx context.Context, s network.ServiceArea) error
	PutRegion(ctx context.Context, r network.Region) error
	GetListing(ctx context.Context, id string) (network.Listing, error)
	PutListing(ctx context.Context, l network.Listing) error
	PutDiscontinued(ctx context.Context, catalogItemID string) error
	GetScorecard(ctx context.Context, nodeID, window string) (network.Scorecard, error)
	PutScorecard(ctx context.Context, s network.Scorecard) error
	PutPressure(ctx context.Context, p network.Pressure) error
}

// WorldReader is the read side of the read models: one consistent snapshot
// for one search.
type WorldReader interface {
	Load(ctx context.Context, q repository.WorldQuery) (sourcing.World, error)
}

// EventOutbox is the write side of the transactional outbox. Called with the
// ctx handed to UnitOfWork.Do it joins the SAME transaction as the aggregate
// Save calls. The adapter encodes each event into its CloudEvents envelope
// (the id is minted once, at append) and stores one row per event in order. A
// relay drains the rows to Kafka afterwards. Appending no events is a no-op.
type EventOutbox interface {
	Append(ctx context.Context, events ...shared.Event) error
}

// Inbox is the idempotency record of the Kafka consumers (table
// processed_events). Claim inside the unit of work that applies the effect:
// it returns false when the CloudEvents id was processed before.
type Inbox interface {
	Claim(ctx context.Context, eventID, eventType string) (bool, error)
}

// UnitOfWork runs fn as ONE atomic unit: every repository, inbox and outbox
// call fn makes with the ctx it is handed commits together when fn returns
// nil, or rolls back together when fn returns an error (or panics). Do is
// re-entrant: when ctx already carries an open unit, Do joins it. The error fn
// returns is returned from Do unchanged (errors.Is keeps working).
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Clock is the service's notion of "now"; use cases stamp aggregates with it.
type Clock interface {
	Now() time.Time
}

// IDGenerator mints the ids of new aggregates (ADR 0002, A1).
type IDGenerator interface {
	NewAssignmentID() assignment.ID
}

// IdempotencyStore keeps the first response of a state-changing REST call per
// (route, key) for 24 hours (table idempotency_keys).
type IdempotencyStore interface {
	// Lookup returns the stored response, or ok false.
	Lookup(ctx context.Context, route, key string) (status int, body []byte, ok bool, err error)
	// Store keeps the response of the first call.
	Store(ctx context.Context, route, key string, status int, body []byte) error
}

// SourcingMetrics records the business outcome of the context's primary
// command (Tier 2 of the fleet metrics convention): the sourcing decision taken
// for each shipment group of a placed order. QueueAssignment calls it once per
// outcome: GroupSourced per group it created, SourcingFailed once for a command
// that stored nothing. A replayed order event or an already-queued group is not
// an outcome. The implementation lives in internal/adapters/outbound/telemetry;
// a nil Deps.Metrics records nothing.
type SourcingMetrics interface {
	// GroupSourced counts one shipment group that now has an assignment; state
	// is the aggregate's state after the decision (QUEUED, RELEASED or
	// UNSOURCEABLE).
	GroupSourced(ctx context.Context, state string)
	// SourcingFailed counts a QueueAssignment that stored nothing; reason is a
	// stable, low-cardinality slug.
	SourcingFailed(ctx context.Context, reason string)
}
