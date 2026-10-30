// Command a2a is the composition root for the order-sourcing A2A agent
// (docs/adr/0009-a2a-inbound-adapter.md): it wires env config to the outbound
// Postgres repository, the repository to the SAME read use cases the REST and
// MCP adapters use (Queries: ListAssignments, GetAssignment), and those to the
// inbound A2A adapter, then serves Agent2Agent JSON-RPC over HTTP with the
// Agent Card at /.well-known/agent-card.json. It is a third, independent
// deployable alongside cmd/api and cmd/mcp.
//
// The agent is READ-ONLY like cmd/mcp: no write use case is wired here at
// all, so this binary can never insert into the outbox, and it needs neither
// Kafka nor the outbox publisher. It never starts the outbox relay, never
// runs the virtual-queue sweep, never runs the nine Kafka consumers and never
// dials Kafka (no KAFKA_BROKERS, no consumer groups): those run in cmd/api
// only. It makes no REST, MCP or A2A call to any sibling context.
// POST /assignments/{id}:reoptimise stays a REST operation.
//
// Like cmd/api it runs on real Postgres only: there is no in-memory mode. A
// missing mandatory variable is a boot error naming it, raised before
// telemetry, any connection and the listener.
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// Environment:
//
//	DATABASE_URL             REQUIRED. Postgres DSN (the service database cmd/api owns)
//	A2A_AGENT_URL            REQUIRED. Absolute http(s) URL of this agent's JSON-RPC
//	                         endpoint, advertised on the Agent Card
//	A2A_ADDR                 listen address (default :8093)
//	MIGRATIONS_DATABASE_URL  direct DSN for the boot migrations (default DATABASE_URL)
//	LOG_LEVEL                debug | info | warn | error (default info)
//	OTEL_SERVICE_NAME        service.name of spans and metrics (default order-sourcing-a2a)
//	OTEL_EXPORTER_OTLP_ENDPOINT  Collector OTLP/gRPC endpoint (default telemetry.DefaultEndpoint); export never blocks
//	SERVICE_VERSION          service.version, reported at boot and on the Agent Card (default "dev")
//	ENVIRONMENT              deployment.environment.name, reported at boot too (default "local")
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inbounda2a "github.com/claudioed/order-sourcing/internal/adapters/inbound/a2a"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/telemetry"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// errNoDatabase is the boot error of a missing DATABASE_URL: this binary has
// no in-memory mode.
var errNoDatabase = errors.New("DATABASE_URL is required: this context runs on real Postgres")

// bootConfig is the mandatory configuration of cmd/a2a: real Postgres and the
// absolute URL the Agent Card advertises, with no default and no degraded mode
// behind either.
type bootConfig struct {
	databaseURL string
	agentURL    string
}

// errNoAgentURL is the boot error for a missing A2A_AGENT_URL: the Agent Card
// must advertise the absolute URL clients dial (docs/adr/0009).
var errNoAgentURL = errors.New("A2A_AGENT_URL is required: the Agent Card must advertise the absolute URL of this agent's JSON-RPC endpoint (docs/adr/0009), e.g. http://<release>-a2a.<ns>.svc.cluster.local:8093/")

// checkAgentURL validates A2A_AGENT_URL: an absolute http(s) URL. The card is
// the discovery contract: a relative or schemeless URL is a misconfiguration
// every client would hit, so it is refused at boot, never silently served.
func checkAgentURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errNoAgentURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("A2A_AGENT_URL=%q is not an absolute http(s) URL: the Agent Card must advertise the endpoint clients dial (docs/adr/0009)", raw)
	}
	return nil
}

// loadBootConfig reads and validates the mandatory environment. Every problem
// is reported at once, each naming its variable, so the process fails fast at
// boot, before telemetry, any connection and the listener.
func loadBootConfig() (bootConfig, error) {
	cfg := bootConfig{
		databaseURL: os.Getenv("DATABASE_URL"),
		agentURL:    strings.TrimSpace(os.Getenv("A2A_AGENT_URL")),
	}
	var errs []error
	if cfg.databaseURL == "" {
		errs = append(errs, errNoDatabase)
	}
	errs = append(errs, checkAgentURL(cfg.agentURL))
	if err := errors.Join(errs...); err != nil {
		return bootConfig{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("a2a agent exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Fail fast, before telemetry, any connection and the listener.
	cfg, err := loadBootConfig()
	if err != nil {
		return err
	}

	// Telemetry comes up before the router is built (otelchimetric binds the
	// global MeterProvider at construction) and is flushed last.
	tel := loadTelemetryEnv(os.Getenv)
	flushTelemetry, err := startTelemetry(logger, tel)
	if err != nil {
		return err
	}
	defer flushTelemetry()

	logger.Info("a2a agent starting",
		"service_version", getenv("SERVICE_VERSION", "dev"),
		"environment", getenv("ENVIRONMENT", "local"),
	)

	databaseURL := cfg.databaseURL
	deps, closeAdapters, err := buildDeps(context.Background(), logger, databaseURL, getenv("MIGRATIONS_DATABASE_URL", databaseURL))
	if err != nil {
		return err
	}
	defer closeAdapters()

	// The card advertises the URL clients dial and the build the operator runs.
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: cfg.agentURL, Version: tel.serviceVersion})
	srv := &http.Server{
		Addr:              getenv("A2A_ADDR", ":8093"),
		Handler:           newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)), card, logger, tel.serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serveA2A(logger, srv)
}

// buildDeps migrates the database, connects a pgx pool and wires the read use
// cases over the Postgres assignment repository. The returned close func
// releases the pool. An empty databaseURL is a boot error: there is no
// fallback runtime.
//
// Like cmd/api and cmd/mcp this binary runs the idempotent embedded
// migrations on start (mirroring cmd/api, which migrates at boot too), so it
// can boot against a fresh database before cmd/api has; golang-migrate's
// advisory lock makes concurrent starts safe. migrationsDatabaseURL is used
// ONLY for that step (a DIRECT Postgres DSN where DATABASE_URL points at
// PgBouncer, which cannot honour the session-scoped advisory lock); the
// runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL string) (inbounda2a.Deps, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return inbounda2a.Deps{}, noop, errNoDatabase
	}
	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the process refuses to boot.
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsDatabaseURL)
	}); err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return inbounda2a.Deps{}, noop, err
	}
	logger.Info("postgres repositories configured")
	assignments := postgres.NewAssignmentRepository(pool)
	return inbounda2a.Deps{Queries: usecases.NewQueries(assignments)}, pool.Close, nil
}

// serveA2A runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveA2A(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("a2a agent listening (JSON-RPC over HTTP)", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the process-wide JSON logger. LOG_LEVEL maps
// debug|info|warn|warning|error (case-insensitive; "warning" is accepted
// like "warn" fleet-wide), defaulting to Info. The handler is wrapped so a
// record written with a span-carrying context also carries trace_id/span_id.
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
	return slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
