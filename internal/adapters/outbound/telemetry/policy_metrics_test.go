package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/telemetry"
)

func TestPolicyMetricsCountBudgetDecisionsByKindAndExposeTheVersion(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	m, err := telemetry.NewPolicyMetrics()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m.BudgetPreferred(ctx, "region")
	m.BudgetPreferred(ctx, "region")
	m.PolicyVersionRead(ctx, 4)
	m.PolicyVersionRead(ctx, 5)

	preferred, version := collectPolicyMetrics(t, reader)
	if preferred != 2 || version != 5 {
		t.Fatalf("preferred = %d (want 2), version = %d (want 5, the last one read)", preferred, version)
	}
}

// collectPolicyMetrics reads the budget counter (checking its kind attribute)
// and the version gauge; -1 means the instrument reported nothing.
func collectPolicyMetrics(t *testing.T, reader sdkmetric.Reader) (preferred, version int64) {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	preferred, version = -1, -1
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				if m.Name == "ordersourcing.budget.preferred" && len(data.DataPoints) == 1 {
					wantKind(t, data.DataPoints[0].Attributes)
					preferred = data.DataPoints[0].Value
				}
			case metricdata.Gauge[int64]:
				if m.Name == "ordersourcing.sourcing.policy.version" && len(data.DataPoints) == 1 {
					version = data.DataPoints[0].Value
				}
			}
		}
	}
	return preferred, version
}

func wantKind(t *testing.T, attrs attribute.Set) {
	t.Helper()
	if v, ok := attrs.Value("kind"); !ok || v.AsString() != "region" {
		t.Fatalf("kind attribute = %v", attrs)
	}
}
