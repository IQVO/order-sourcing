// Command os-reports is the read-only HTTP reader of the order-sourcing
// analytics read side
// (docs/adr/0013-analytics-read-side-and-the-sourcing-decision-fact.md): GET
// /reports/sourcing-mix, /reports/order-splits, /reports/cost-to-serve and
// /reports/freshness, plus /healthz and /readyz, answered from the ANALYTICAL database only
// through a read-only pool. It writes nothing, never opens the OLTP database or
// Kafka and calls no sibling. No auth: access control is the in-cluster
// boundary (fleet rule).
//
// Environment:
//
//	ANALYTICS_READER_DATABASE_URL  DSN of a read-only role on the analytical database
//	ANALYTICS_DATABASE_URL         fallback for single-role local runs (one of the two is REQUIRED)
//	HTTP_ADDR                      listen address (default :8092)
//	LOG_LEVEL                      debug|info|warn|error (default info)
//	OTEL_SERVICE_NAME              service.name (default order-sourcing-reports)
//	OTEL_EXPORTER_OTLP_ENDPOINT    Collector OTLP/gRPC endpoint (default telemetry.DefaultEndpoint); export never blocks
//	SERVICE_VERSION                service.version (default dev)
//	ENVIRONMENT                    deployment.environment.name (default local)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/inbound/reportshttp"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/telemetry"
	"github.com/claudioed/order-sourcing/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// errMissingAnalyticsURL is the boot error without any analytical DSN.
var errMissingAnalyticsURL = errors.New("ANALYTICS_READER_DATABASE_URL (or ANALYTICS_DATABASE_URL) is required: the reports are served from the analytical database")

// config is the reports binary's validated environment.
type config struct {
	analyticsURL string
	httpAddr     string
}

func orDefault(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// loadConfig reads the environment through env (os.Getenv in main). The
// reader DSN wins; ANALYTICS_DATABASE_URL (the projector's) is the fallback.
func loadConfig(env func(string) string) (config, error) {
	c := config{
		analyticsURL: orDefault(env("ANALYTICS_READER_DATABASE_URL"), env("ANALYTICS_DATABASE_URL")),
		httpAddr:     orDefault(env("HTTP_ADDR"), ":8092"),
	}
	if c.analyticsURL == "" {
		return config{}, errMissingAnalyticsURL
	}
	return c, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Getenv)
	stop()
	if err != nil {
		slog.Error("os-reports exited with error", "error", err)
		os.Exit(1)
	}
}

// run boots the reports server and serves until ctx is cancelled.
func run(ctx context.Context, env func(string) string) error {
	logger := newLogger(env("LOG_LEVEL"))
	slog.SetDefault(logger)
	cfg, err := loadConfig(env)
	if err != nil {
		return err
	}
	serviceName := orDefault(env("OTEL_SERVICE_NAME"), reportshttp.DefaultServiceName)
	flush, err := startTelemetry(logger, serviceName, orDefault(env("SERVICE_VERSION"), telemetry.DefaultServiceVersion),
		orDefault(env("OTEL_EXPORTER_OTLP_ENDPOINT"), telemetry.DefaultEndpoint))
	if err != nil {
		return err
	}
	defer flush()

	pool, err := analyticsstore.NewReadOnlyPool(ctx, cfg.analyticsURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := bootretry.Retry(ctx, logger, "ping analytics database", func() error { return pool.Ping(ctx) }); err != nil {
		return err
	}
	reader := analyticsstore.NewReader(pool)
	server := &reportshttp.Server{Reader: reader, Ping: reader}
	srv := &http.Server{Addr: cfg.httpAddr, Handler: reportshttp.NewRouter(server, logger, serviceName), ReadHeaderTimeout: 5 * time.Second}
	logger.Info("os-reports starting", "version", env("SERVICE_VERSION"), "environment", env("ENVIRONMENT"), "addr", cfg.httpAddr)
	return serveUntilDone(ctx, logger, srv, server)
}

// serveUntilDone runs srv until it fails or ctx is cancelled, then flips
// /readyz and drains it.
func serveUntilDone(ctx context.Context, logger *slog.Logger, srv *http.Server, server *reportshttp.Server) error {
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("reports server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	select {
	case err := <-serveErr:
		return fmt.Errorf("reports server failed: %w", err)
	case <-ctx.Done():
	}
	server.SetNotReady()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
