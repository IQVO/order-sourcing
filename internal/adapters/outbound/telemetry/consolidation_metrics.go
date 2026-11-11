package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

// The instruments of the carrier and follower consolidation (ADR 0015).
// Exported to Prometheus as ordersourcing_consolidated_releases_total,
// ordersourcing_consolidated_assignments_total,
// ordersourcing_consolidation_withdrawn_total{reason},
// ordersourcing_merged_cancel_refused_total and ordersourcing_stale_accept_total.
const (
	consolidatedReleasesName    = "ordersourcing.consolidated.releases"
	consolidatedAssignmentsName = "ordersourcing.consolidated.assignments"
	consolidationWithdrawnName  = "ordersourcing.consolidation.withdrawn"
	mergedCancelRefusedName     = "ordersourcing.merged.cancel.refused"
	staleAcceptName             = "ordersourcing.stale.accept"
)

// ConsolidationMetrics implements ports.ConsolidationMetrics against the
// global MeterProvider (a no-op until Setup installs a real one).
type ConsolidationMetrics struct {
	releases, assignments, withdrawn, cancelRefused, staleAccept metric.Int64Counter
}

var _ ports.ConsolidationMetrics = (*ConsolidationMetrics)(nil)

// NewConsolidationMetrics registers the counters. Call it after Setup so the
// instruments bind to the real MeterProvider.
func NewConsolidationMetrics() (*ConsolidationMetrics, error) {
	meter := otel.Meter(meterName)
	counter := func(name, unit, description string) (metric.Int64Counter, error) {
		return meter.Int64Counter(name, metric.WithDescription(description), metric.WithUnit(unit))
	}
	var m ConsolidationMetrics
	var err error
	specs := []struct {
		dst               *metric.Int64Counter
		name, unit, descr string
	}{
		{&m.releases, consolidatedReleasesName, "{release}", "Releases that merged two or more groups into one purchase order. Divide " +
			"consolidated assignments by this for the packages saved per merge."},
		{&m.assignments, consolidatedAssignmentsName, "{assignment}", "Follower groups absorbed into a merged release (the carrier is not counted)."},
		{&m.withdrawn, consolidationWithdrawnName, "{merge}", "Merged releases dissolved after the purchase order was published, by reason " +
			"(PROMISE_REVISED). A cancel never withdraws: it is refused while the merge is live."},
		{&m.cancelRefused, mergedCancelRefusedName, "{cancel}", "Cancels of a member of a live merge that was refused until the carrier locks " +
			"or the merge dissolves. A rising count means shoppers cancel merged groups more than expected."},
		{&m.staleAccept, staleAcceptName, "{acceptance}", "Acceptances for a release that was no longer pending. After a withdrawal each one is a " +
			"purchase order the vendor holds for the old union (a possible duplicate pick); expect it to stay near zero."},
	}
	for _, s := range specs {
		if *s.dst, err = counter(s.name, s.unit, s.descr); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// Consolidated counts one merged release and the followers it absorbed.
func (m *ConsolidationMetrics) Consolidated(ctx context.Context, followers int) {
	m.releases.Add(ctx, 1)
	m.assignments.Add(ctx, int64(followers))
}

// Withdrawn counts one dissolved merge that withdrew its release.
func (m *ConsolidationMetrics) Withdrawn(ctx context.Context, reason string) {
	m.withdrawn.Add(ctx, 1, metric.WithAttributes(reasonKey.String(reason)))
}

// CancelRefused counts a cancel of a merged member that was refused.
func (m *ConsolidationMetrics) CancelRefused(ctx context.Context) { m.cancelRefused.Add(ctx, 1) }

// StaleAccept counts an acceptance for a release that is no longer pending.
func (m *ConsolidationMetrics) StaleAccept(ctx context.Context) { m.staleAccept.Add(ctx, 1) }
