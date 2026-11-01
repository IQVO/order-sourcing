// Command api is the order-sourcing service: the REST API, the nine Kafka
// consumers, the virtual-queue sweep and the transactional-outbox relay in one
// process, on real Postgres and real Kafka (there is no in-memory mode).
//
// Environment (the observability part; the runtime settings are in docs/runtime.md):
//
//	LOG_LEVEL                        debug | info | warn | error (default info)
//	OTEL_SERVICE_NAME                service.name of spans and metrics (default order-sourcing)
//	OTEL_EXPORTER_OTLP_ENDPOINT      Collector OTLP/gRPC endpoint (default telemetry.DefaultEndpoint); export never blocks
//	SERVICE_VERSION                  service.version (default dev)
//	ENVIRONMENT                      deployment.environment.name (default local)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/clock"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/telemetry"
	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/bootretry"
)

const (
	shutdownTimeout          = 10 * time.Second
	workerDrainTimeout       = 10 * time.Second
	defaultShutdownDrain     = 5 * time.Second
	defaultSweepInterval     = 15 * time.Second
	defaultSweepPageSize     = 100
	serviceReadHeaderTimeout = 5 * time.Second
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

var (
	errMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")
	errMissingBrokers     = errors.New("KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)")
)

// runtimeEnv is the validated boot configuration.
type runtimeEnv struct {
	brokers []string
	groups  map[string]string // consumer-group env name -> group id
}

// loadRuntime validates the mandatory environment before anything is dialled.
func loadRuntime(env func(string) string, groupEnvs []string) (runtimeEnv, error) {
	if env("DATABASE_URL") == "" {
		return runtimeEnv{}, errMissingDatabaseURL
	}
	rt := runtimeEnv{brokers: splitList(env("KAFKA_BROKERS")), groups: map[string]string{}}
	if len(rt.brokers) == 0 {
		return runtimeEnv{}, errMissingBrokers
	}
	if err := parsePublisherMode(env("EVENT_PUBLISHER")); err != nil {
		return runtimeEnv{}, err
	}
	for _, name := range groupEnvs {
		group := env(name)
		if group == "" {
			return runtimeEnv{}, fmt.Errorf("%s is required: the consumer of that topic always runs on real Kafka", name)
		}
		rt.groups[name] = group
	}
	return rt, nil
}

func parsePublisherMode(raw string) error {
	switch raw {
	case "", "kafka":
		return nil
	case "log":
		return errors.New("EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka")
	default:
		return fmt.Errorf("EVENT_PUBLISHER: unknown value %q (want kafka)", raw)
	}
}

func splitList(raw string) []string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// services is every adapter and use case the process runs on.
type services struct {
	pool     *pgxpool.Pool
	outbox   *postgres.Outbox
	deps     usecases.Deps
	handlers inboundkafka.Handlers
	server   *inboundhttp.Server
	sweep    *usecases.SweepQueue
}

// buildServices wires the adapters and use cases; metrics (nil = none) is the
// business-counter port the queueing use case reports to.
func buildServices(pool *pgxpool.Pool, readiness *inboundhttp.Readiness, metrics ports.SourcingMetrics) services {
	outbox := postgres.NewOutbox(pool, outboundkafka.Encoder{})
	assignments := postgres.NewAssignmentRepository(pool)
	deps := usecases.Deps{
		Assignments: assignments, ReadModels: postgres.NewReadModels(pool), World: postgres.NewWorldReader(pool), Outbox: outbox,
		Inbox: postgres.NewInbox(pool), UoW: postgres.NewUnitOfWork(pool), Clock: clock.System{}, IDs: ids.UUID{},
		Metrics: metrics,
	}
	reoptimise := usecases.NewReoptimiseAssignment(deps)
	queries := usecases.NewQueries(assignments)
	return services{
		pool: pool, outbox: outbox, deps: deps,
		handlers: inboundkafka.Handlers{UC: inboundkafka.UseCases{
			Queue: usecases.NewQueueAssignment(deps), Cancel: usecases.NewCancelAssignment(deps), Revise: usecases.NewApplyRevisedPromise(deps),
			Refusal: usecases.NewHandleNodeRefusal(deps), Lock: usecases.NewLockAssignment(deps), Ship: usecases.NewMarkShipped(deps),
			Deliver: usecases.NewMarkDelivered(deps), PickupMoved: usecases.NewRecordPickupMoved(deps), Processed: usecases.NewRecordProcessed(deps),
			Project: usecases.NewProjector(deps),
		}},
		server: &inboundhttp.Server{
			List: queries, Get: queries, Reoptimise: reoptimise, Readiness: readiness,
			Idempotency: inboundhttp.RequireIdempotencyKey(pool, postgres.BindTx),
		},
		sweep: usecases.NewSweepQueue(deps, usecases.NewReleaseToNode(deps), reoptimise),
	}
}

func run(ctx context.Context) error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	groupEnvs := consumerGroupEnvs(inboundkafka.Handlers{})
	rt, err := loadRuntime(os.Getenv, groupEnvs)
	if err != nil {
		return err
	}

	// Telemetry comes up before the router is built (otelchimetric binds the
	// global MeterProvider at construction) and is flushed LAST: deferred before
	// everything below, so the final spans of the drain still leave the process.
	tel := loadTelemetryEnv(os.Getenv)
	flushTelemetry, err := startTelemetry(logger, tel)
	if err != nil {
		return err
	}
	defer flushTelemetry()

	databaseURL := os.Getenv("DATABASE_URL")
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(getenv("MIGRATIONS_DATABASE_URL", databaseURL))
	}); err != nil {
		return err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		return err
	}

	readiness := inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: pool.Ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(rt.brokers).Check},
	)
	// The business counter binds to the MeterProvider telemetry.Setup installed.
	sourcingMetrics, err := telemetry.NewSourcingMetrics()
	if err != nil {
		return err
	}
	svc := buildServices(pool, readiness, sourcingMetrics)
	svc.server.Logger, svc.server.ServiceName = logger, tel.serviceName
	httpServer := &http.Server{
		Addr: getenv("HTTP_ADDR", ":8080"), Handler: svc.server.Handler(), ReadHeaderTimeout: serviceReadHeaderTimeout,
	}

	consumers, closeConsumers := startConsumers(svc, rt, logger)
	relay, closeRelay, err := startOutboxRelay(svc.outbox, rt.brokers, logger)
	if err != nil {
		closeConsumers()
		return err
	}
	sweep, closeSweep := startSweep(svc.sweep, logger)
	return serveUntilSignal(ctx, logger, httpServer, readiness, append(consumers, sweep), closeConsumers, closeSweep, relay, closeRelay)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	// Wrapped so a record written with a span-carrying context (the *Context
	// slog variants) also carries trace_id and span_id.
	return slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
