package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/telemetry"
)

func TestConsolidationMetricsCountMergesWithdrawalsRefusalsAndStaleAccepts(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	m, err := telemetry.NewConsolidationMetrics()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m.Consolidated(ctx, 2)
	m.Consolidated(ctx, 1)
	m.Withdrawn(ctx, "PROMISE_REVISED")
	m.CancelRefused(ctx)
	m.CancelRefused(ctx)
	m.StaleAccept(ctx)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if sum, ok := metric.Data.(metricdata.Sum[int64]); ok && len(sum.DataPoints) == 1 {
				got[metric.Name] = sum.DataPoints[0].Value
			}
		}
	}
	want := map[string]int64{
		"ordersourcing.consolidated.releases": 2, "ordersourcing.consolidated.assignments": 3, "ordersourcing.consolidation.withdrawn": 1,
		"ordersourcing.merged.cancel.refused": 2, "ordersourcing.stale.accept": 1,
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s = %d, want %d (all: %v)", name, got[name], v, got)
		}
	}
}
