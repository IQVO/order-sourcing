package telemetry

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

// meterName scopes this service's own instruments, keeping them distinct from
// the ones otelchi and the runtime collector register.
const meterName = "github.com/claudioed/order-sourcing"

// groupsSourcedName is the Tier 2 business metric of the context (ADR 0008, the
// fleet's standard metrics convention): every shipment group of a placed order
// that QueueAssignment turned into an assignment, by outcome.
const groupsSourcedName = "sourcing.groups.sourced"

const (
	outcomeKey = attribute.Key("outcome")
	reasonKey  = attribute.Key("reason")

	outcomeFailed = "failed"
)

// SourcingMetrics implements ports.SourcingMetrics against the global
// MeterProvider. Until Setup installs a real provider the global one is a
// no-op, so recording is cheap and safe in tests and local runs.
type SourcingMetrics struct {
	counter metric.Int64Counter
}

var _ ports.SourcingMetrics = (*SourcingMetrics)(nil)

// NewSourcingMetrics registers the counter. It only fails if the instrument
// name is invalid, which is a programming error, not a runtime condition. Call
// it after Setup so the instrument binds to the real MeterProvider.
func NewSourcingMetrics() (*SourcingMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		groupsSourcedName,
		metric.WithDescription("Shipment groups of placed orders that order sourcing decided on, by outcome: queued (waiting for its release time), "+
			"released (sent to a node at once, same-day), unsourceable (no node can ship it) or failed (nothing stored; reason says why, "+
			"for example concurrent-update or internal-error). A rising unsourceable share means the network cannot fulfil what is being "+
			"promised (stock, lanes or service areas are missing); a rising failed with internal-error means the service itself is failing "+
			"and orders are not being sourced."),
		metric.WithUnit("{group}"),
	)
	if err != nil {
		return nil, err
	}
	return &SourcingMetrics{counter: counter}, nil
}

// GroupSourced counts one shipment group by the state its assignment was
// created in; the outcome attribute is that state in lower case.
func (m *SourcingMetrics) GroupSourced(ctx context.Context, state string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(strings.ToLower(state))))
}

// SourcingFailed counts a command that stored nothing, with the reason why.
func (m *SourcingMetrics) SourcingFailed(ctx context.Context, reason string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeFailed), reasonKey.String(reason)))
}
