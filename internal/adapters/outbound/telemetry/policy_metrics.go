package telemetry

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

// The instruments of the sourcing policy (ADR 0014). Exported to Prometheus as
// ordersourcing_budget_preferred_total{kind} and
// ordersourcing_sourcing_policy_version.
const (
	budgetPreferredName = "ordersourcing.budget.preferred"
	policyVersionName   = "ordersourcing.sourcing.policy.version"
	kindKey             = attribute.Key("kind")
)

// PolicyMetrics implements ports.PolicyMetrics against the global
// MeterProvider (a no-op until Setup installs a real one).
type PolicyMetrics struct {
	preferred metric.Int64Counter
	version   atomic.Int64
}

var _ ports.PolicyMetrics = (*PolicyMetrics)(nil)

// NewPolicyMetrics registers the counter and the gauge. Call it after Setup so
// the instruments bind to the real MeterProvider.
func NewPolicyMetrics() (*PolicyMetrics, error) {
	meter := otel.Meter(meterName)
	preferred, err := meter.Int64Counter(budgetPreferredName,
		metric.WithDescription("Releases whose winner the sourcing policy's cost-deviation budget changed relative to the pure cheapest "+
			"ordering, by kind (region: an in-region candidate inside the budget beat a cheaper cross-region one). A rising count means "+
			"the operator is paying for in-region fulfilment; compare the cost per unit before and after publishing a policy."),
		metric.WithUnit("{release}"))
	if err != nil {
		return nil, err
	}
	m := &PolicyMetrics{preferred: preferred}
	_, err = meter.Int64ObservableGauge(policyVersionName,
		metric.WithDescription("The sourcing policy version the last search read; 0 is the built-in default (nothing published)."),
		metric.WithUnit("{version}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(m.version.Load())
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return m, nil
}

// PolicyVersionRead records the version a search read (or a publish stored).
func (m *PolicyMetrics) PolicyVersionRead(_ context.Context, version int) {
	m.version.Store(int64(version))
}

// BudgetPreferred counts one release the budget rule decided.
func (m *PolicyMetrics) BudgetPreferred(ctx context.Context, kind string) {
	m.preferred.Add(ctx, 1, metric.WithAttributes(kindKey.String(kind)))
}
