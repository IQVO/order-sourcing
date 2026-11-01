---
id: observability
title: Observability
sidebar_label: Observability
description: "Every OpenTelemetry instrument, span, log field and exporter variable of the three binaries, and the dashboards and alerts that exist in warehouse-infra."
---

# Observability

Logs, metrics and traces follow the fleet standard ([ADR 0008](/docs/adr/0008-fleet-observability-standard)). All three binaries
export OTLP/gRPC to the Collector; logs are JSON on stdout for Alloy and Loki. Export never blocks: the gRPC exporters connect
lazily and no blocking dial option is set, so a Collector that is down or absent costs telemetry and nothing else.

Source: `internal/adapters/outbound/telemetry/` (`telemetry.go`, `slogotel.go`, `sourcing_metrics.go`), `cmd/*/telemetry.go`,
`internal/adapters/inbound/requestlog/requestlog.go`.

## Exporter variables

| Variable | Default | Applies to |
| --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | all; bare `host:port` becomes `http://host:port`, a URL is used as is. The chart sets `otel-collector.observability.svc.cluster.local:4317` |
| `OTEL_SERVICE_NAME` | `order-sourcing` (`cmd/api`), `order-sourcing-mcp`, `order-sourcing-a2a` | all; the resource attribute `service.name` |
| `SERVICE_VERSION` | `dev` | all; `service.version` (the chart passes the image tag) |
| `ENVIRONMENT` | `local` | all; `deployment.environment.name` |
| `LOG_LEVEL` | `info` | all; `debug`, `info`, `warn` or `warning`, `error` |

Metrics are pushed by a periodic reader every 15 s. `OTEL_RESOURCE_ATTRIBUTES` is honoured through the SDK default resource. SDK
diagnostics (for example "no collector listening") are logged at DEBUG, so they do not appear at the default level. A flush
failure on shutdown is logged at WARN `telemetry flush failed on shutdown` (5 s bound).

## Metrics

| Instrument | Type | Unit | Attributes | Emitted by | Meaning |
| --- | --- | --- | --- | --- | --- |
| `sourcing.groups.sourced` (Prometheus: `sourcing_groups_sourced_total`) | Int64Counter, meter `github.com/claudioed/order-sourcing` | `{group}` | `outcome`: `queued`, `released`, `unsourceable` or `failed`; `reason` (only when `outcome=failed`): `concurrent-update`, `duplicate-group`, `validation-failed`, `internal-error` | `cmd/api` | One count per shipment group that `QueueAssignment` turned into an assignment, labelled with the state it was created in; one `failed` count per command that stored nothing. A rolled-back attempt never counts a group and a replayed event (nothing created) records nothing |
| `http.server.request.duration` (Prometheus: `http_server_request_duration_seconds_*`) | Float64Histogram (otelchi metric middleware) | `s` | the otelchi base attributes; no status-code attribute in this fleet | all three | Request duration per route; `/healthz` and `/readyz` are included in the metric but not in the access log |
| Go runtime metrics (`go.goroutine.count`, `go.memory.used`, GC and heap series) | from `go.opentelemetry.io/contrib/instrumentation/runtime` | per series | per series | all three | Goroutines, memory and GC |

`cmd/mcp` and `cmd/a2a` register no business instrument. Because `http.server.request.duration` has no status attribute, 5xx
rates come from Kong (`kong_http_requests_total`) in the dashboard and alerts, not from the service.

## Traces

| Span | Kind | Created by | Attributes |
| --- | --- | --- | --- |
| `<METHOD> <chi route pattern>` (one name per route, not per assignment id) | server | otelchi middleware in all three routers | otelchi defaults |
| `<topic> process` (for example `retailnetwork.customer-ordering.events process`) | consumer | `internal/adapters/inbound/kafka/tracing.go`, one per handling attempt, parented to the producer's span from the `traceparent` header (a message without one starts a new trace) | `messaging.system=kafka`, `messaging.destination.name`, `messaging.operation.name=process`; errors are recorded on the span |
| `a2a.skill <skill id>` | internal | `cmd/a2a`, around each skill run that parsed | `a2a.skill`; status Error on a problem answer |

The trace continues across Kafka: when an event is appended to `outbox_events`, the active span's W3C `traceparent` (and
`tracestate`, `baggage` when present) is stored in the row's `headers`, and the relay copies it onto the Kafka message. Consumers
of this service extract it. The MCP adapter creates no tool span of its own: an MCP call is visible only as the HTTP server span.
(A comment in `internal/adapters/inbound/a2a/skills.go:25` mentions "mcp.tool spans"; no such span exists in this repository.)

## Logs

JSON via `log/slog` on stdout, one object per line with `time`, `level`, `msg` and the fields below. Records written with a
span-carrying context (the `*Context` slog calls) also carry `trace_id` and `span_id`; boot, shutdown and background-worker records
written with plain `slog.Info` do not.

| Where | Level | `msg` (abridged) | Fields |
| --- | --- | --- | --- |
| Access log (all three) | INFO / WARN (4xx) / ERROR (5xx) | `http request` | `method`, `route` (the pattern, never the raw path), `status`, `duration_ms`, `bytes`, `request_id`. `/healthz` and `/readyz` are skipped |
| Boot (api) | INFO | `telemetry configured`; `consumer running`; `outbox relay running`; `queue sweep running`; `http server listening` | `service_name`, `otlp_endpoint`; `consumer`, `topic`, `group_id`, `brokers`; `publisher`, `interval`, `max_attempts`, `topics`; `interval`; `addr` |
| Boot (mcp, a2a) | INFO | `mcp server starting` / `a2a agent starting`; `postgres repositories configured`; `mcp server listening (Streamable HTTP)` | `service_version`, `environment`; `addr` |
| Boot retry | WARN / INFO | `retrying` / `succeeded after retry` | `op` (`run migrations`, `ping postgres`), `attempt`, `in`, `err` |
| Boot | WARN | `invalid OUTBOX_RELAY_INTERVAL; using the default`, `invalid OUTBOX_MAX_ATTEMPTS; using the default`, `invalid SWEEP_INTERVAL; using the default`, `ignoring invalid SHUTDOWN_DRAIN_DELAY` | `value`, `default` |
| Boot failure | ERROR | `service exited with error` / `mcp server exited with error` / `a2a agent exited with error` (exit 1) | `error` |
| Shutdown | INFO / WARN | `shutdown: readiness flipped to not-ready; waiting for traffic to drain`; `worker did not stop before the shutdown drain deadline` | `drain_delay`; `worker` |
| Readiness | WARN | `readiness: dependency is not ready` (once per check round, at most every 3 s) | `dependency` (`postgres` or `kafka`), `error` |
| Consumer | WARN | `skipping a message that is not a valid CloudEvent`; `dead-lettering an event that can never be applied`; `skipping an event the aggregate can no longer take` | `consumer`, `topic`, `type`, `event_id`, `error` |
| Consumer | ERROR | `<consumer> handling failed; retrying the same message` | `error`, `partition`, `offset`, `attempt`, `max_attempts`, `retry_in` |
| Consumer | ERROR | `<consumer> handling failed after the maximum attempts; dead-lettering the message`; `<consumer> message can never be applied; dead-lettering it without retry` | `error`, `partition`, `offset` (and `attempts`) |
| Consumer | ERROR | `<consumer> offset commit failed; retrying the same message`; `<consumer> dead-letter publish failed; retrying the same message` | `error`, `partition`, `offset`, `attempt`, `retry_in` |
| Consumer | WARN | `<consumer> broker connection stalled or lost; fetching again after the backoff` | `error`, `attempt`, `backoff` |
| Consumer | ERROR | `consumer stopped` (the reader failed for good; the process then exits) | `consumer`, `error` |
| Relay | ERROR | `outbox relay pass failed` | `error` |
| Relay | ERROR | `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause` | `outbox_id`, `topic`, `event_type`, `key`, `attempts`, `max_attempts`, `error` |
| Relay | ERROR | `outbox relay stopped`, `kafka relay sink close failed` | `error` |
| Sweep | INFO | `queue sweep` (only when it did something) | `released`, `reoptimised`, `skipped` |
| Sweep | ERROR | `queue sweep pass failed` | `error` |
| MCP | ERROR | `mcp tool failed with an unexpected error` | `error` |
| A2A | ERROR | `a2a skill failed with an unexpected error` | `error` |

`<consumer>` is the consumer name, the short topic name: `customer-ordering`, `delivery-promise`, `last-mile-delivery`,
`linehaul-transportation`, `node-performance`, `node-topology`, `offer-availability`, `shipping-and-tracking` or
`vendor-connectivity` (the worker that supervises it is reported as `<name> consumer` when it exits).

REST errors are `application/problem+json` documents whose `type` is `https://errors.order-sourcing.retail-network.example/<slug>`;
the slug and status table is in [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).

## Dashboards and alerts that exist

From `warehouse-infra` `origin/develop` (nothing else was found there):

| Artifact | Where | Content |
| --- | --- | --- |
| Grafana dashboard `warehouse-order-sourcing` | `terraform/dashboards/contexts/order-sourcing.json` | Kong request rate, 5xx ratio and p95 latency (request versus upstream); service request rate by route and p95 by process (`http_server_request_duration_seconds`); `sum by (outcome) (rate(sourcing_groups_sourced_total[5m]))`; Go goroutines and memory; Loki log stream, log volume by level, errors and warnings |
| Alert group `warehouse-order-sourcing` | `terraform/alerting/warehouse-alert-rules.json` | `WarehouseKong5xxRatioHigh` (warning, 5 m, 5xx ratio above 0.05), `WarehousePodRestarting` (warning, at least 2 restarts in 15 m of a pod named `order-sourcing-.+`) |
| Alert group `warehouse-order-sourcing-slo` | same file | `WarehouseApiAvailabilityBurnFast` and `WarehouseApiLatencyBurnFast` (critical, 2 m), `WarehouseApiAvailabilityBurnSlow` and `WarehouseApiLatencyBurnSlow` (warning, 15 m); availability SLO 99 percent, latency SLO 400 ms upstream at 95 percent |

There is no alert on outbox backlog, consumer lag, dead-letter volume or the `unsourceable` share of
`sourcing_groups_sourced_total`. The alert annotations link a `runbook_url` on the pre-site path
`docs/operations/runbook.md`.

## Suggested checks

Built from the data that exists, not shipped as rules:

| Signal | How to read it | Suggested alert |
| --- | --- | --- |
| Outbox backlog | `SELECT count(*), min(created_at) FROM outbox_events WHERE published_at IS NULL` | oldest unpublished row older than a few minutes |
| Rows parked at the cap | ERROR `row reached the attempt cap`; `... WHERE published_at IS NULL AND attempts >= 20` | any row |
| Dead letters | ERROR `dead-lettering` lines; depth of the `<topic>.dlq` topics | any message |
| Consumer stuck on one message | repeated `handling failed; retrying the same message` with the same `partition` and `offset` | more than the 5 attempts' worth |
| Unsourceable share | `sourcing_groups_sourced_total{outcome="unsourceable"}` against the total | a rising share means stock, lanes or service areas are missing for what is promised |
| Service failing | `sourcing_groups_sourced_total{outcome="failed",reason="internal-error"}` | any sustained rate: orders are not being sourced |
| Readiness | `GET /readyz` 503 `dependency-unavailable`; WARN `readiness: dependency is not ready` | not ready for more than a minute |
| Table growth | row counts of `outbox_events`, `idempotency_keys`, `processed_events` (nothing deletes them) | capacity planning |
