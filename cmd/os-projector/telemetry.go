package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/telemetry"
)

// telemetryFlushTimeout bounds the final export attempt on shutdown.
const telemetryFlushTimeout = 5 * time.Second

// startTelemetry installs the global TracerProvider/MeterProvider (ADR 0008)
// and returns the flush to defer. It MUST run before newAdminMux: otelchi and
// otelchimetric resolve the global MeterProvider at construction. Export never
// blocks, and a failed flush is logged, never returned.
func startTelemetry(logger *slog.Logger, serviceName, serviceVersion, endpoint string) (func(), error) {
	shutdown, err := telemetry.Setup(context.Background(), serviceName, serviceVersion, endpoint)
	if err != nil {
		return nil, err
	}
	logger.Info("telemetry configured", "service_name", serviceName, "otlp_endpoint", endpoint)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			logger.Warn("telemetry flush failed on shutdown", "error", err)
		}
	}, nil
}

// newLogger builds the process-wide JSON logger (debug|info|warn|warning|error,
// default info), wrapped so a span-carrying record also carries
// trace_id/span_id.
func newLogger(level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	return slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}
