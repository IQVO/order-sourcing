package reportshttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// RFC 9110 section 15.5.6: a 405 MUST carry an Allow header listing the methods
// the resource supports. Every report route is GET-only.
func TestMethodNotAllowedCarriesTheAllowHeader(t *testing.T) {
	h := NewRouter(&Server{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	cases := map[string]struct{ method, target string }{
		"healthz":       {http.MethodPost, "/healthz"},
		"readyz":        {http.MethodDelete, "/readyz"},
		"sourcing mix":  {http.MethodPost, "/reports/sourcing-mix"},
		"order splits":  {http.MethodPut, "/reports/order-splits"},
		"cost to serve": {http.MethodPatch, "/reports/cost-to-serve"},
		"freshness":     {http.MethodDelete, "/reports/freshness"},
		"head":          {http.MethodHead, "/reports/freshness"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), c.method, c.target, http.NoBody))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405", c.method, c.target, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != http.MethodGet {
				t.Errorf("Allow = %q, want GET", got)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

// A path no route matches is a 404 and has no methods to advertise.
func TestUnknownPathHasNoAllowHeader(t *testing.T) {
	h := NewRouter(&Server{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/reports/nope", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "" {
		t.Errorf("Allow = %q on a 404, want none", got)
	}
}

// The list is derived from the router, not hard-coded: a path with three
// methods yields all three, in probe order.
func TestAllowedMethodsIsDerivedFromTheRoutes(t *testing.T) {
	r := chi.NewRouter()
	noop := func(http.ResponseWriter, *http.Request) {}
	r.Get("/multi", noop)
	r.Post("/multi", noop)
	r.Delete("/multi", noop)
	r.Put("/single", noop)
	for path, want := range map[string]string{"/multi": "GET, POST, DELETE", "/single": "PUT", "/absent": ""} {
		if got := allowedMethods(r, path); got != want {
			t.Errorf("allowedMethods(%q) = %q, want %q", path, got, want)
		}
	}
}
