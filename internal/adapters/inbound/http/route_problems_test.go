package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// RFC 9110 section 15.5.6: a 405 MUST carry an Allow header listing the methods
// the resource supports.
func TestMethodNotAllowedCarriesTheAllowHeader(t *testing.T) {
	h := (&Server{List: &fakeUC{}, Get: &fakeUC{}, Reoptimise: &fakeUC{}}).Handler()
	cases := map[string]struct{ method, target, allow string }{
		"healthz":                 {http.MethodPost, "/healthz", "GET"},
		"readyz":                  {http.MethodDelete, "/readyz", "GET"},
		"list":                    {http.MethodPost, "/assignments", "GET"},
		"get one":                 {http.MethodDelete, "/assignments/asg-1", "GET"},
		"get one, unknown verb":   {http.MethodPatch, "/assignments/asg-1:cancel", "GET"},
		"reoptimise":              {http.MethodGet, "/assignments/asg-1/reoptimisations", "POST"},
		"policy, post":            {http.MethodPost, "/sourcing-policy", "GET, PUT"},
		"policy, delete":          {http.MethodDelete, "/sourcing-policy", "GET, PUT"},
		"policy, head is not GET": {http.MethodHead, "/sourcing-policy", "GET, PUT"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), c.method, c.target, http.NoBody))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405", c.method, c.target, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != c.allow {
				t.Errorf("Allow = %q, want %q", got, c.allow)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

// A path no route matches is a 404 and has no methods to advertise.
func TestUnknownPathHasNoAllowHeader(t *testing.T) {
	h := (&Server{}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/nowhere", http.NoBody))
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
