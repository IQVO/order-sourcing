package reportshttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/inbound/reportshttp"
	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// fakeReader answers fixed rows and records the range it was asked for.
type fakeReader struct {
	mix    []report.MixDay
	splits []report.SplitDay
	cost   []report.CostDay
	last   *time.Time
	err    error
	asked  report.Range
}

func (f *fakeReader) SourcingMix(_ context.Context, r report.Range) ([]report.MixDay, error) {
	f.asked = r
	return f.mix, f.err
}

func (f *fakeReader) OrderSplits(_ context.Context, r report.Range) ([]report.SplitDay, error) {
	f.asked = r
	return f.splits, f.err
}

func (f *fakeReader) CostToServe(_ context.Context, r report.Range) ([]report.CostDay, error) {
	f.asked = r
	return f.cost, f.err
}

func (f *fakeReader) LastEventAt(context.Context) (*time.Time, error) { return f.last, f.err }

type fakePing struct{ err error }

func (p fakePing) Ping(context.Context) error { return p.err }

var (
	now  = time.Date(2026, 11, 8, 18, 30, 0, 0, time.UTC)
	nov3 = time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
)

func serve(t *testing.T, s *reportshttp.Server, method, target string) (int, string, map[string]any) {
	t.Helper()
	if s.Now == nil {
		s.Now = func() time.Time { return now }
	}
	h := reportshttp.NewRouter(s, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, target, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s: body %q is not JSON: %v", method, target, rec.Body.String(), err)
	}
	return rec.Code, rec.Header().Get("Content-Type"), body
}

func p(v float64) *float64 { return &v }

func TestSourcingMixReport(t *testing.T) {
	r := &fakeReader{mix: []report.MixDay{
		{Day: nov3, Releases: 4, InRegion: 2, OutOfRegion: 1, UnknownRegion: 1, Legs: 6, Reassignments: 1, Unsourceable: 2},
		{Day: nov3.Add(24 * time.Hour), Unsourceable: 1},
	}}
	code, ct, body := serve(t, &reportshttp.Server{Reader: r}, http.MethodGet, "/reports/sourcing-mix?from=2026-11-01&to=2026-11-07")
	if code != http.StatusOK || ct != "application/json" {
		t.Fatalf("status %d content-type %s", code, ct)
	}
	want := map[string]any{"from": "2026-11-01", "to": "2026-11-07", "days": []any{
		map[string]any{"day": "2026-11-03", "groupsReleased": 4.0, "inRegionShare": 2.0 / 3, "unknownRegionCount": 1.0, "meanLegs": 1.5,
			"reassignmentRate": 0.25, "unsourceableCount": 2.0},
		map[string]any{"day": "2026-11-04", "groupsReleased": 0.0, "inRegionShare": nil, "unknownRegionCount": 0.0, "meanLegs": nil,
			"reassignmentRate": nil, "unsourceableCount": 1.0},
	}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v\nwant %v", body, want)
	}
	if !r.asked.From.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || !r.asked.To.Equal(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("asked range = %+v, want [11-01, 11-08)", r.asked)
	}
}

func TestOrderSplitsReport(t *testing.T) {
	r := &fakeReader{splits: []report.SplitDay{{Day: nov3, Orders: 4, Groups: 6, SplitOrders: 1}}}
	code, _, body := serve(t, &reportshttp.Server{Reader: r}, http.MethodGet, "/reports/order-splits")
	want := map[string]any{"from": "2026-10-10", "to": "2026-11-08", "days": []any{
		map[string]any{"day": "2026-11-03", "ordersQueued": 4.0, "groupsPerOrder": 1.5, "splitOrderShare": 0.25},
	}}
	if code != http.StatusOK || !reflect.DeepEqual(body, want) {
		t.Fatalf("%d body = %v\nwant %v", code, body, want)
	}
}

func TestCostToServeReport(t *testing.T) {
	r := &fakeReader{cost: []report.CostDay{
		{Day: nov3, Decisions: 4, Complete: 3, CostCents: 900, Units: 6, P50: p(300), P90: p(340)},
		{Day: nov3.Add(24 * time.Hour), Decisions: 1},
	}}
	code, _, body := serve(t, &reportshttp.Server{Reader: r}, http.MethodGet, "/reports/cost-to-serve?from=2026-11-03&to=2026-11-04")
	want := map[string]any{"from": "2026-11-03", "to": "2026-11-04", "days": []any{
		map[string]any{"day": "2026-11-03", "costCompleteShare": 0.75, "costPerUnitCents": 150.0, "p50CostCentsPerGroup": 300.0,
			"p90CostCentsPerGroup": 340.0},
		map[string]any{"day": "2026-11-04", "costCompleteShare": 0.0, "costPerUnitCents": nil, "p50CostCentsPerGroup": nil,
			"p90CostCentsPerGroup": nil},
	}}
	if code != http.StatusOK || !reflect.DeepEqual(body, want) {
		t.Fatalf("%d body = %v\nwant %v", code, body, want)
	}
}

func TestEmptyReportsAreEmptyArrays(t *testing.T) {
	for _, path := range rangedReports() {
		_, _, body := serve(t, &reportshttp.Server{Reader: &fakeReader{}}, http.MethodGet, path)
		if days, ok := body["days"].([]any); !ok || len(days) != 0 {
			t.Errorf("%s days = %#v, want []", path, body["days"])
		}
	}
}

func rangedReports() []string {
	return []string{"/reports/sourcing-mix", "/reports/order-splits", "/reports/cost-to-serve"}
}

func TestFreshness(t *testing.T) {
	_, _, body := serve(t, &reportshttp.Server{Reader: &fakeReader{}}, http.MethodGet, "/reports/freshness")
	if v, ok := body["asOf"]; !ok || v != nil || body["lagSeconds"] != nil {
		t.Fatalf("before the first event = %v, want explicit nulls", body)
	}
	asOf := now.Add(-90 * time.Second)
	_, _, body = serve(t, &reportshttp.Server{Reader: &fakeReader{last: &asOf}}, http.MethodGet, "/reports/freshness")
	if body["asOf"] != "2026-11-08T18:28:30Z" || body["lagSeconds"] != 90.0 {
		t.Fatalf("freshness = %v", body)
	}
}

func assertProblem(t *testing.T, code int, ct string, body map[string]any, wantCode int, wantType string) {
	t.Helper()
	if code != wantCode || ct != "application/problem+json" || body["type"] != "https://errors.order-sourcing.retail-network.example/"+wantType || body["status"] != float64(wantCode) {
		t.Fatalf("got %d %s %v, want %d problem %s", code, ct, body, wantCode, wantType)
	}
}

func TestInvalidRangeIsAProblem(t *testing.T) {
	for _, path := range rangedReports() {
		for _, q := range []string{"?from=2026-11-05&to=2026-11-04", "?from=nope", "?to=2026-13-01", "?from=2025-01-01&to=2026-11-01"} {
			code, ct, body := serve(t, &reportshttp.Server{Reader: &fakeReader{}}, http.MethodGet, path+q)
			assertProblem(t, code, ct, body, http.StatusBadRequest, "invalid-report-range")
		}
	}
}

func TestReaderFailureIsAnOpaque500(t *testing.T) {
	r := &fakeReader{err: errors.New("pq: password authentication failed for secret-role")}
	for _, path := range append(rangedReports(), "/reports/freshness") {
		code, ct, body := serve(t, &reportshttp.Server{Reader: r}, http.MethodGet, path)
		assertProblem(t, code, ct, body, http.StatusInternalServerError, "internal-error")
		if strings.Contains(body["detail"].(string), "secret") {
			t.Fatalf("%s leaked the cause: %v", path, body)
		}
	}
}

func TestUnknownPathAndWriteMethodsAreProblems(t *testing.T) {
	code, ct, body := serve(t, &reportshttp.Server{Reader: &fakeReader{}}, http.MethodGet, "/reports/nope")
	assertProblem(t, code, ct, body, http.StatusNotFound, "resource-not-found")
	code, ct, body = serve(t, &reportshttp.Server{Reader: &fakeReader{}}, http.MethodPost, "/reports/freshness")
	assertProblem(t, code, ct, body, http.StatusMethodNotAllowed, "method-not-allowed")
}

func TestHealthAndReadiness(t *testing.T) {
	if code, _, body := serve(t, &reportshttp.Server{}, http.MethodGet, "/healthz"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", code, body)
	}
	if code, _, body := serve(t, &reportshttp.Server{Ping: fakePing{}}, http.MethodGet, "/readyz"); code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("readyz = %d %v", code, body)
	}
	code, ct, body := serve(t, &reportshttp.Server{Ping: fakePing{err: errors.New("down")}}, http.MethodGet, "/readyz")
	assertProblem(t, code, ct, body, http.StatusServiceUnavailable, "dependency-unavailable")
	draining := &reportshttp.Server{}
	draining.SetNotReady()
	if code, _, body := serve(t, draining, http.MethodGet, "/readyz"); code != http.StatusServiceUnavailable || body["status"] != "not_ready" {
		t.Fatalf("draining readyz = %d %v", code, body)
	}
}
