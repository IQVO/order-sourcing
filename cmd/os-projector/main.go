// Command os-projector is the WRITER of the order-sourcing analytics
// read side (docs/adr/0013-analytics-read-side-and-the-sourcing-decision-fact.md).
// It consumes this context's own two topics (retailnetwork.order-sourcing.events
// and retailnetwork.order-sourcing.analytics) under a fixed, env-supplied
// consumer group, projects FulfillmentQueued, FulfillmentSourcingDecided and
// FulfillmentUnsourceable into the ANALYTICAL database (separate from the OLTP
// one, embedded migrations) and serves only /healthz and /readyz on an admin
// port. It is the only writer of that
// database; the read-only reports binary is cmd/os-reports.
//
// It never touches the OLTP database and never reads a sibling's topic. Kafka
// is dialled lazily (the reader connects inside its first fetch), so a broker
// outage at boot does not crash the process; the analytical migrations and the
// first ping are retried through internal/bootretry.
//
// Environment (REQUIRED unless a default is shown):
//
//	ANALYTICS_DATABASE_URL       direct DSN of the analytical database (REQUIRED; runs the migrations)
//	KAFKA_BROKERS                comma-separated brokers (REQUIRED)
//	ANALYTICS_CONSUMER_GROUP     the projector's fixed group (default order-sourcing-analytics)
//	ADMIN_ADDR                   /healthz and /readyz listen address (default :8091)
//	LOG_LEVEL                    debug|info|warn|error (default info)
//	OTEL_SERVICE_NAME            service.name (default order-sourcing-projector)
//	OTEL_EXPORTER_OTLP_ENDPOINT  Collector OTLP/gRPC endpoint (default telemetry.DefaultEndpoint); export never blocks
//	SERVICE_VERSION              service.version (default dev)
//	ENVIRONMENT                  deployment.environment.name (default local)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	inboundkafka "github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/inbound/requestlog"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/telemetry"
	"github.com/claudioed/order-sourcing/internal/bootretry"
)

// shutdownTimeout bounds the admin server drain and the wait for the consumer
// loop to commit or dead-letter the message it was handling.
const shutdownTimeout = 10 * time.Second

// defaultConsumerGroup is the projector's fixed group when
// ANALYTICS_CONSUMER_GROUP is unset (the chart always sets it): the fleet's
// `<ctx>-analytics` naming, distinct from every OLTP consumer group.
const defaultConsumerGroup = "order-sourcing-analytics"

// defaultServiceName labels this binary's spans and metrics.
const defaultServiceName = "order-sourcing-projector"

// config is the projector's validated environment.
type config struct {
	analyticsURL string
	brokers      []string
	group        string
	adminAddr    string
}

var (
	errMissingAnalyticsURL = errors.New("ANALYTICS_DATABASE_URL is required: the projector writes only to the analytical database")
	errMissingBrokers      = errors.New("KAFKA_BROKERS is required: the projector consumes retailnetwork.order-sourcing.events and .analytics")
)

func orDefault(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// loadConfig reads the environment through env (os.Getenv in main).
func loadConfig(env func(string) string) (config, error) {
	c := config{
		analyticsURL: env("ANALYTICS_DATABASE_URL"),
		group:        orDefault(env("ANALYTICS_CONSUMER_GROUP"), defaultConsumerGroup),
		adminAddr:    orDefault(env("ADMIN_ADDR"), ":8091"),
	}
	if c.analyticsURL == "" {
		return config{}, errMissingAnalyticsURL
	}
	for _, b := range strings.Split(env("KAFKA_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			c.brokers = append(c.brokers, b)
		}
	}
	if len(c.brokers) == 0 {
		return config{}, errMissingBrokers
	}
	return c, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Getenv)
	stop()
	if err != nil {
		slog.Error("os-projector exited with error", "error", err)
		os.Exit(1)
	}
}

// run boots the projector and serves until ctx is cancelled (a signal in main,
// the test's cancel in the wiring test).
func run(ctx context.Context, env func(string) string) error {
	logger := newLogger(env("LOG_LEVEL"))
	slog.SetDefault(logger)
	cfg, err := loadConfig(env)
	if err != nil {
		return err
	}
	serviceName := orDefault(env("OTEL_SERVICE_NAME"), defaultServiceName)
	flush, err := startTelemetry(logger, serviceName, orDefault(env("SERVICE_VERSION"), telemetry.DefaultServiceVersion),
		orDefault(env("OTEL_EXPORTER_OTLP_ENDPOINT"), telemetry.DefaultEndpoint))
	if err != nil {
		return err
	}
	defer flush()

	pool, err := openAnalyticsPool(ctx, logger, cfg.analyticsURL)
	if err != nil {
		return err
	}
	defer pool.Close() // runs last: after the consumer loop stopped touching it

	consumer := inboundkafka.NewAnalyticsConsumer(cfg.brokers, inboundkafka.OwnTopics(), inboundkafka.AnalyticsDLQTopic,
		cfg.group, analyticsstore.NewProjection(pool), logger)
	defer func() {
		if err := consumer.Close(); err != nil {
			logger.Error("closing the analytics consumer failed", "error", err)
		}
	}()
	logger.Info("os-projector starting", "version", env("SERVICE_VERSION"), "environment", env("ENVIRONMENT"),
		"topics", inboundkafka.OwnTopics(), "group_id", cfg.group, "brokers", cfg.brokers)
	return serve(ctx, logger, cfg.adminAddr, serviceName, pool, consumer)
}

// serve runs the admin server and the consumer until ctx is cancelled or one
// of them fails, then drains both.
func serve(ctx context.Context, logger *slog.Logger, addr, serviceName string, pool *pgxpool.Pool, consumer *inboundkafka.AnalyticsConsumer) error {
	var notReady atomic.Bool
	admin := &http.Server{Addr: addr, Handler: newAdminMux(&notReady, pool, logger, serviceName), ReadHeaderTimeout: 5 * time.Second}
	adminErr := make(chan error, 1)
	go func() {
		logger.Info("projector admin server listening", "addr", addr)
		if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			adminErr <- err
		}
	}()
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()
	consumerDone := make(chan error, 1)
	go func() { consumerDone <- consumer.Run(consumerCtx) }()

	select {
	case err := <-consumerDone:
		// The consumer only returns on an unrecoverable reader/commit failure
		// here: exit non-zero so the pod restarts.
		return fmt.Errorf("analytics consumer stopped unexpectedly: %w", err)
	case err := <-adminErr:
		return fmt.Errorf("admin server failed: %w", err)
	case <-ctx.Done():
	}
	// Graceful shutdown: flip readiness first, drain the admin server, then
	// let the consumer finish the message in flight before the pool closes.
	notReady.Store(true)
	deadline, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := admin.Shutdown(deadline)
	cancelConsumer()
	return drainConsumer(deadline, consumerDone, shutdownErr, logger)
}

// drainConsumer waits for the consumer after its context was cancelled. The
// loop returns the cancellation on a graceful stop, so that stays unreported;
// any OTHER error is joined onto the admin shutdown error so the process still
// exits non-zero. A consumer that misses the deadline leaves the admin error.
func drainConsumer(deadline context.Context, consumerDone <-chan error, shutdownErr error, logger *slog.Logger) error {
	select {
	case err := <-consumerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("analytics consumer failed during shutdown", "error", err)
			return errors.Join(shutdownErr, err)
		}
	case <-deadline.Done():
		logger.Warn("analytics consumer did not stop before the shutdown deadline")
	}
	return shutdownErr
}

// pinger is the analytical database check of /readyz.
type pinger interface {
	Ping(ctx context.Context) error
}

// newAdminMux serves /healthz (liveness, never flipped) and /readyz (503 once
// shutdown began or while the analytical database does not answer), behind the
// fleet's HTTP middleware stack. telemetry.Setup must have run first.
func newAdminMux(notReady *atomic.Bool, db pinger, logger *slog.Logger, serviceName string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(otelchi.Middleware(serviceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(serviceName)))
	r.Use(requestlog.RequestLogger(logger))
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { status(w, http.StatusOK, "ok") })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if notReady.Load() || db.Ping(ctx) != nil {
			status(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		status(w, http.StatusOK, "ready")
	})
	return r
}

func status(w http.ResponseWriter, code int, s string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"status":"` + s + `"}`))
}

// openAnalyticsPool applies the embedded analytical migrations over the direct
// DSN, opens the writer pool and pings it, each under boot retry.
func openAnalyticsPool(ctx context.Context, logger *slog.Logger, url string) (*pgxpool.Pool, error) {
	if err := bootretry.Retry(ctx, logger, "run analytics migrations", func() error { return analyticsstore.RunMigrations(url) }); err != nil {
		return nil, err
	}
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := bootretry.Retry(ctx, logger, "ping analytics database", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
