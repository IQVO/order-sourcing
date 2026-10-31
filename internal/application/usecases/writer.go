// Package usecases holds the application's use cases: one per command of the
// FulfillmentAssignment, one per REST operation, the read-model projections and
// the queue sweep. Every command follows one shape inside ONE ports.UnitOfWork:
// claim the inbox row (Kafka triggers), load the aggregate, call the domain,
// save it and append the raised events to the outbox. Domain, shared and port
// errors pass through unchanged (errors.Is keeps working): the adapters map
// them to problem types or to a consumer action.
package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Deps bundles the ports the use cases need.
type Deps struct {
	Assignments ports.AssignmentRepository
	ReadModels  ports.ReadModels
	World       ports.WorldReader
	Outbox      ports.EventOutbox
	Inbox       ports.Inbox
	UoW         ports.UnitOfWork
	Clock       ports.Clock
	IDs         ports.IDGenerator
	// Metrics records the business outcome of each queued shipment group; nil
	// records nothing, so unit tests need no wiring.
	Metrics ports.SourcingMetrics
}

// Meta identifies the Kafka message that triggered a command. A zero Meta (a
// REST or scheduler trigger) skips the inbox.
type Meta struct {
	EventID   string
	EventType string
}

// metrics returns the configured SourcingMetrics or a no-op.
func (d Deps) metrics() ports.SourcingMetrics {
	if d.Metrics != nil {
		return d.Metrics
	}
	return noMetrics{}
}

// noMetrics is the SourcingMetrics of a Deps without one.
type noMetrics struct{}

func (noMetrics) GroupSourced(context.Context, string)   {}
func (noMetrics) SourcingFailed(context.Context, string) {}

// writer is what every command embeds.
type writer struct{ Deps }

// now returns the clock's current time in UTC.
func (w writer) now() time.Time { return w.Clock.Now().UTC() }

// publish appends the raised events to the outbox, on the ctx of the unit of work.
func (w writer) publish(ctx context.Context, events []shared.Event) error {
	if err := w.Outbox.Append(ctx, events...); err != nil {
		return fmt.Errorf("append events to outbox: %w", err)
	}
	return nil
}

// claim records the CloudEvents id in the inbox; it reports true when this
// message was already processed and the effect must be skipped.
func (w writer) claim(ctx context.Context, m Meta) (bool, error) {
	if m.EventID == "" {
		return false, nil
	}
	first, err := w.Inbox.Claim(ctx, m.EventID, m.EventType)
	if err != nil {
		return false, fmt.Errorf("claim inbox row: %w", err)
	}
	return !first, nil
}

// persist saves the aggregate and publishes its events when the version moved
// since the load (a no-op command changes nothing and writes nothing).
func (w writer) persist(ctx context.Context, a *assignment.Assignment, before int64, events []shared.Event) error {
	if a.Version() == before {
		return nil
	}
	if err := w.Assignments.Save(ctx, a); err != nil {
		return err
	}
	return w.publish(ctx, events)
}

// transact runs attempt in a unit of work. A ports.ErrConcurrentUpdate means
// another writer got in between the load and the save, so the whole attempt
// runs once more on fresh state before the conflict is reported.
func transact[T any](ctx context.Context, uow ports.UnitOfWork, attempt func(ctx context.Context) (T, error)) (T, error) {
	var out T
	run := func(ctx context.Context) error {
		v, err := attempt(ctx)
		if err != nil {
			return err
		}
		out = v
		return nil
	}
	err := uow.Do(ctx, run)
	if errors.Is(err, ports.ErrConcurrentUpdate) {
		err = uow.Do(ctx, run)
	}
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// found reports whether a lookup hit: a nil error is a hit, ports.ErrNotFound
// a miss, anything else a failure.
func found(err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ports.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}
