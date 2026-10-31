package main

import (
	"log/slog"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/claudioed/order-sourcing/internal/adapters/inbound/requestlog"
)

// newRouter wraps the A2A handler in a small chi router so the binary is
// deployable behind Kubernetes probes:
//
//   - GET /healthz  -> 200 {"status":"ok"} (open; liveness/readiness probes).
//   - GET /.well-known/agent-card.json -> the Agent Card (the discovery
//     contract, docs/adr/0009).
//   - /  and /a2a   -> the A2A JSON-RPC handler. Both paths are served so the
//     fleet's <CTX>_A2A_ENDPOINT convention (".../a2a") and the canonical
//     root endpoint both work.
//
// The middleware order is the fleet observability standard, identical to the
// REST router: RequestID, otelchi (server span named after the route pattern),
// the request-duration histogram, RequestLogger, Recoverer. telemetry.Setup
// must have run before this call (otelchimetric binds the global MeterProvider
// at construction).
//
// No auth middleware is ever added here (TestNoAuthMiddlewareReintroduced).
func newRouter(a2aHandler http.Handler, card *a2a.AgentCard, logger *slog.Logger, serviceName string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(otelchi.Middleware(serviceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(serviceName)))
	r.Use(requestlog.RequestLogger(logger))
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	r.Handle("/a2a", a2aHandler)
	r.Handle("/", a2aHandler)
	return r
}
