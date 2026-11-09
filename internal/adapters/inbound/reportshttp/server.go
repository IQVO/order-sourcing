// Package reportshttp is the read-only REST inbound adapter of the analytics
// read side (ADR 0013): GET /reports/sourcing-mix, /reports/order-splits,
// /reports/cost-to-serve and /reports/freshness over a report.Reader, plus
// /healthz and /readyz. It is served by cmd/os-reports, never by cmd/api. Every error is an RFC 9457
// problem; there is no auth (fleet decision 2026-09-11) and no write route.
package reportshttp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/claudioed/order-sourcing/internal/adapters/inbound/requestlog"
	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// DefaultServiceName names the server spans and the HTTP metrics when the
// composition root passes no service name.
const DefaultServiceName = "order-sourcing-reports"

// readyTimeout bounds the /readyz database ping.
const readyTimeout = 2 * time.Second

// Pinger is the readiness check of the analytical database.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds the reader the report handlers call.
type Server struct {
	Reader report.Reader
	// Ping checks the analytical database for /readyz; nil means always ready.
	Ping Pinger
	// Now is the clock of the default range and the freshness lag
	// (time.Now when nil).
	Now func() time.Time

	draining atomic.Bool
}

// SetNotReady flips /readyz to 503 for good: the first step of the shutdown.
func (s *Server) SetNotReady() { s.draining.Store(true) }

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// NewRouter builds the router. Middleware order is the fleet observability
// standard (RequestID, otelchi, the request-duration histogram, the request
// logger, Recoverer); telemetry.Setup must have run first, because
// otelchimetric binds the global MeterProvider at construction.
func NewRouter(s *Server, logger *slog.Logger, serviceName string) http.Handler {
	if serviceName == "" {
		serviceName = DefaultServiceName
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(otelchi.Middleware(serviceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(serviceName)))
	r.Use(requestlog.RequestLogger(logger))
	r.Use(middleware.Recoverer)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, notFound, "no resource matches the requested path")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, methodNotAllowed, "the reports are read-only: only GET is supported")
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, statusBody{Status: "ok"}) })
	r.Get("/readyz", s.handleReadyz)
	r.Get("/reports/sourcing-mix", s.handleSourcingMix)
	r.Get("/reports/order-splits", s.handleOrderSplits)
	r.Get("/reports/cost-to-serve", s.handleCostToServe)
	r.Get("/reports/freshness", s.handleFreshness)
	return r
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, statusBody{Status: "not_ready"})
		return
	}
	if s.Ping != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := s.Ping.Ping(ctx); err != nil {
			writeProblem(w, r, dependencyUnavailable, "not ready: analytical database")
			return
		}
	}
	writeJSON(w, http.StatusOK, statusBody{Status: "ready"})
}

// parseRange reads from/to; a bad range is written as a 400 and false returned.
func (s *Server) parseRange(w http.ResponseWriter, r *http.Request) (report.Range, bool) {
	q := r.URL.Query()
	rg, err := report.ParseRange(q.Get("from"), q.Get("to"), s.now())
	if err != nil {
		writeProblem(w, r, invalidRange, err.Error())
		return report.Range{}, false
	}
	return rg, true
}

// serveRange answers one ranged report: parse from/to, query, shape.
func serveRange[T any](s *Server, w http.ResponseWriter, r *http.Request, query func(report.Range) (T, error), body func(report.Range, T) any) {
	rg, ok := s.parseRange(w, r)
	if !ok {
		return
	}
	days, err := query(rg)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body(rg, days))
}

func (s *Server) handleSourcingMix(w http.ResponseWriter, r *http.Request) {
	serveRange(s, w, r, func(rg report.Range) ([]report.MixDay, error) { return s.Reader.SourcingMix(r.Context(), rg) },
		func(rg report.Range, days []report.MixDay) any { return mixBody(rg, report.SourcingMix(days)) })
}

func (s *Server) handleOrderSplits(w http.ResponseWriter, r *http.Request) {
	serveRange(s, w, r, func(rg report.Range) ([]report.SplitDay, error) { return s.Reader.OrderSplits(r.Context(), rg) },
		func(rg report.Range, days []report.SplitDay) any { return splitsBody(rg, report.OrderSplits(days)) })
}

func (s *Server) handleCostToServe(w http.ResponseWriter, r *http.Request) {
	serveRange(s, w, r, func(rg report.Range) ([]report.CostDay, error) { return s.Reader.CostToServe(r.Context(), rg) },
		func(rg report.Range, days []report.CostDay) any { return costBody(rg, report.CostToServe(days)) })
}

func (s *Server) handleFreshness(w http.ResponseWriter, r *http.Request) {
	asOf, err := s.Reader.LastEventAt(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	f := report.ComputeFreshness(asOf, s.now())
	writeJSON(w, http.StatusOK, freshnessBody{AsOf: f.AsOf, LagSeconds: f.LagSeconds})
}

type statusBody struct {
	Status string `json:"status"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
