package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

var errBoomForLog = errors.New("boom")

// recordSpans installs a recording TracerProvider for the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

// logLines decodes every JSON line the logger wrote.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// serve runs one request through a router logging into buf.
func serveLogged(f *fakeUC, buf *bytes.Buffer, method, target string) *httptest.ResponseRecorder {
	h := (&Server{List: f, Get: f, Reoptimise: f, Logger: slog.New(slog.NewJSONHandler(buf, nil))}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, http.NoBody))
	return rec
}

func TestRouterServerSpanIsNamedAfterTheRoutePattern(t *testing.T) {
	recorder := recordSpans(t)

	serveLogged(&fakeUC{}, &bytes.Buffer{}, http.MethodGet, "/assignments/asg-with-a-distinct-id")

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", spans[0].SpanKind())
	}
	if got := spans[0].Name(); !strings.HasSuffix(got, "/assignments/{id}") {
		t.Errorf("span name = %q, want the route pattern /assignments/{id} (one name per route, not per assignment id)", got)
	}
}

// The trace_id stamping itself (the telemetry handler turning the request
// context into trace_id/span_id) is covered in the telemetry package and, end to
// end, by cmd/api's TestRouterLogCarriesTheServerSpanTraceID; this test pins the
// shape of the request line.
func TestRouterRequestLogShape(t *testing.T) {
	recorder := recordSpans(t)
	var buf bytes.Buffer

	r := serveLogged(&fakeUC{err: ports.ErrNotFound}, &buf, http.MethodGet, "/assignments/missing")
	if r.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown assignment", r.Code)
	}

	lines := logLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly the request line: %v", len(lines), lines)
	}
	line := lines[0]
	if line["msg"] != "http request" || line["method"] != "GET" || line["status"] != float64(404) {
		t.Errorf("request line = %v", line)
	}
	if line["level"] != "WARN" {
		t.Errorf("level = %v, want WARN for a 4xx", line["level"])
	}
	if line["route"] != "/assignments/{id}" {
		t.Errorf("route = %v, want the pattern, never the raw path", line["route"])
	}
	if id, _ := line["request_id"].(string); id == "" {
		t.Error("request_id is empty: RequestID must run before RequestLogger")
	}
	for _, key := range []string{"duration_ms", "bytes"} {
		if _, ok := line[key]; !ok {
			t.Errorf("request line has no %s", key)
		}
	}
	if len(recorder.Ended()) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(recorder.Ended()))
	}
}

func TestRouterRequestLogLevelFollowsTheStatus(t *testing.T) {
	var buf bytes.Buffer
	serveLogged(&fakeUC{}, &buf, http.MethodGet, "/assignments/asg-1")                   // 200 -> INFO
	serveLogged(&fakeUC{}, &buf, http.MethodGet, "/assignments?limit=nope")              // 400 -> WARN
	serveLogged(&fakeUC{err: errBoomForLog}, &buf, http.MethodGet, "/assignments/asg-1") // 500 -> ERROR

	levels := map[float64]bool{}
	for _, l := range logLines(t, &buf) {
		if l["msg"] != "http request" {
			continue
		}
		want := map[float64]string{200: "INFO", 400: "WARN", 500: "ERROR"}[l["status"].(float64)]
		levels[l["status"].(float64)] = l["level"] == want
	}
	if !levels[200] || !levels[400] || !levels[500] || len(levels) != 3 {
		t.Errorf("levels by status = %v, want 200 INFO, 400 WARN and 500 ERROR", levels)
	}
}

func TestRouterDoesNotLogProbes(t *testing.T) {
	var buf bytes.Buffer
	h := (&Server{Readiness: NewReadiness(DefaultReadinessTTL, DefaultReadinessTimeout), Logger: slog.New(slog.NewJSONHandler(&buf, nil))}).Handler()
	for _, path := range []string{"/healthz", "/readyz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, http.NoBody))
	}

	if strings.Contains(buf.String(), "http request") {
		t.Errorf("probes were logged: %s", buf.String())
	}
}
