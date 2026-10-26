# ADR 0008: Adopt the fleet observability standard (logs, metrics, traces)

## Status

Accepted (2026-10-26).

## Context

order-sourcing logged JSON with `log/slog` and nothing else: no trace or span id
on a log line, no request id, no request log, no metrics, no traces, no
`traceparent` on the events it publishes, and no OpenTelemetry settings in the
Helm chart. The warehouse-systems services (inventory-storage, wes-work-planning)
already share one pipeline and one convention, recorded in their ADRs "standard
metrics convention" (inventory-storage ADR 0016, wes-work-planning ADR 0013), and
customer-ordering adopted it first (its ADR 0007):

- pods export OTLP/gRPC to the in-cluster Collector
  (`otel-collector.observability.svc.cluster.local:4317`), which feeds Jaeger
  (traces) and a Prometheus exporter (metrics);
- logs stay on stdout as JSON for Alloy and Loki.

Without the same wiring a sourcing decision could not be followed from the
`CustomerOrderPlaced` consumer through the release event to the node's side, a
log line could not be joined to its trace, and no dashboard could show request
rate, errors, latency or how many shipment groups end up unsourceable. The
alternatives weighed were a service-specific setup (rejected: every repo should
answer the same query the same way) and the OTel Go auto-instrumentation agents
(rejected: they cannot follow a trace across `segmentio/kafka-go`, which has no
instrumentation, nor name spans by the chi route pattern).

## Decision

Tier 1 (identical in every repo) and Tier 2 (this context's business metric):

1. **Telemetry adapter.** `internal/adapters/outbound/telemetry` holds `Setup`
   (OTLP/gRPC traces and metrics, W3C TraceContext + Baggage propagator, Go
   runtime metrics; exporters dial lazily, so no Collector means dropped
   telemetry, never a blocked boot or request; returns a shutdown func) and
   `WithTraceContext` (stamps `trace_id`/`span_id` on a log record written with
   a span-carrying context). Only the composition roots (`cmd/api`, `cmd/mcp`)
   and the other adapters import it; `internal/domain` and `internal/application`
   never import OpenTelemetry.
2. **Logger.** `slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, ...)))`,
   `LOG_LEVEL`, uppercase levels. Code on a request, message or sweep path uses
   the `*Context` slog variants; the bare variants have no span.
3. **HTTP middleware order** on both chi routers (REST and MCP):
   `RequestID` -> `otelchi.Middleware(serviceName, WithChiRoutes(r))` ->
   `otelchimetric.NewServerRequestDuration` -> `RequestLogger` -> `Recoverer`;
   the existing `rejectNUL` and Idempotency-Key middleware stay after them.
   `telemetry.Setup` runs before the router is built (otelchimetric binds the
   global MeterProvider at construction) and is flushed last on shutdown. The
   request line carries the method, the chi route pattern (never the raw path,
   which holds assignment ids), status, duration, bytes and request id; 5xx is
   ERROR, 4xx WARN, the rest INFO; `/healthz` and `/readyz` are not logged.
   `RequestLogger` lives in `internal/adapters/inbound/requestlog`, not in the
   REST adapter, because `cmd/mcp` may not import the REST adapter (the MCP
   fitness tests) and both routers must log identically.
4. **Traces over Kafka.** `Outbox.Append` stamps `traceparent`/`tracestate` from
   the active span into the outbox headers, inside the transaction that writes
   the aggregate (`internal/adapters/outbound/postgres/outbox_trace.go`). The
   relay publishes the headers unchanged as Kafka headers; each of the nine
   inbound consumers extracts them and runs every handling attempt in a consumer
   span named `<topic> process`, so the use case's logs carry the publisher's
   trace id and an event enqueued while handling continues the trace. Only extra
   headers are added: the CloudEvents attributes and body, and the encoder's
   output, are unchanged (ADR 0004).
5. **Environment contract.** `cmd/api` and `cmd/mcp` read `OTEL_SERVICE_NAME`
   (defaults `order-sourcing` and `order-sourcing-mcp`),
   `OTEL_EXPORTER_OTLP_ENDPOINT` (default `telemetry.DefaultEndpoint`, a bare
   `host:port` or a URL), `SERVICE_VERSION`, `ENVIRONMENT` (read by `Setup`) and
   `LOG_LEVEL`.
6. **Chart.** `otel.enabled`, `otel.endpoint`, `otel.serviceName` render
   `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_SERVICE_NAME` (`<name>-mcp` for the
   mcp component); `SERVICE_VERSION` is the image tag, or the chart appVersion
   when empty, for the api and the mcp component; both render `ENVIRONMENT`
   (`environment`, `mcp.environment`). `extraEnv` refuses all four names.
   `charts/order-sourcing/tests/render_test.sh` asserts it and scans `cmd/` for
   the variables the binaries read. The optional `frontend` component serves
   static files and gets no telemetry env.
7. **Tier 2 business metric.** One counter, `sourcing.groups.sourced`, unit
   `{group}`, attribute `outcome` (`queued` | `released` | `unsourceable` |
   `failed`) and, on `failed`, `reason` (`concurrent-update`, `duplicate-group`,
   `validation-failed`, `internal-error`). `ports.SourcingMetrics` is the
   application port; `QueueAssignment` (the use case behind `CustomerOrderPlaced`,
   where the context decides for the first time which shipment groups can be
   sourced) calls it once per created group, or once for a command that stored
   nothing; a replay (inbox hit) or an already-queued group is not an outcome and
   records nothing. The OTel implementation is in the telemetry adapter, is
   injected from `cmd/api`, and a nil `Deps.Metrics` is a no-op. It follows the
   fleet convention: dotted lowercase name, one counter with an `outcome`
   attribute rather than two counters, UCUM unit, a description that says what a
   human should conclude when it moves (a rising `unsourceable` share means the
   network cannot fulfil what is being promised; `failed` with `internal-error`
   means orders are not being sourced). Chosen because the sourcing decision is
   the aggregate's reason to exist (ADR 0001) and its `UNSOURCEABLE` outcome is
   the one business signal that no HTTP or runtime metric reveals; the later
   transitions (release, lock, ship) are consumed-event driven and would be
   further counters, which the standard says not to invent.

Assumptions (the brief was silent):

- The counter sits on `QueueAssignment` rather than `SweepQueue`/`ReleaseToNode`
  because that is the single command in which the decision to source (or not) is
  taken for every order; the sweep only executes it later.
- The MCP binary is read-only, so it carries the Tier 1 signals (spans, request
  duration, logs) and no business counter.
- ADR numbering: 0008 is the next free number here (customer-ordering used 0007).

Not part of this ADR: dashboards and alert rules, which live in warehouse-infra
and are generated per deployed context from the instrument names above.

## Consequences

- A request, its log lines, the events it enqueued and the consumers that handle
  them in other services share one trace id; `trace_id` in Loki jumps to Jaeger.
- Request rate, errors and duration come from `http.server.request.duration`
  (otelchimetric) with no hand-rolled counter; sourcing health from
  `sourcing.groups.sourced`.
- Every new log call on a request, message or sweep path must use a `*Context`
  variant, or its line carries no trace id.
- A consumer span is created per handling attempt, so a retried message shows as
  several spans under the same parent.
- The trace parent of a consumed event is the span that was active at the outbox
  write (the REST request or the upstream consumer span), not a separate publish
  span: the relay is asynchronous and adds no span of its own.
