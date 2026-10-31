---
id: configuration
title: Configuration
sidebar_label: Configuration
description: "Every environment variable read by cmd/api, cmd/mcp and cmd/a2a: default, whether it is required and what it does."
---

# Configuration

Order Sourcing ships three binaries from one image: `cmd/api` (REST, nine Kafka consumers, queue sweep, outbox relay),
`cmd/mcp` (read-only MCP server) and `cmd/a2a` (read-only Agent2Agent agent). All three run on real PostgreSQL only;
`cmd/api` also needs real Kafka. There is no in-memory mode and no log sink. A missing mandatory variable is a boot error
that names the variable, raised before the listener binds.

Source: `cmd/api/main.go` (`loadRuntime`), `cmd/api/telemetry.go`, `cmd/mcp/main.go`, `cmd/a2a/main.go`
(`loadBootConfig`), `internal/adapters/outbound/telemetry/telemetry.go`.

Durations accept Go syntax (`15s`, `500ms`); `OUTBOX_RELAY_INTERVAL` also accepts plain seconds (`2`). An invalid
optional value is logged at WARN and the default is used.

## cmd/api

| Variable | Default | Required | Meaning |
| --- | --- | --- | --- |
| `DATABASE_URL` | none | yes | Postgres DSN of the service pool. Unset = boot error `DATABASE_URL is required`. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct (session-mode) DSN used only by the boot migrations; golang-migrate holds a session advisory lock that a transaction-pooling proxy cannot honour. |
| `KAFKA_BROKERS` | none | yes | Comma-separated `host:port` list, used by the outbox relay, the nine consumers and the `/readyz` broker check. Unset or blank = boot error. |
| `CUSTOMER_ORDERING_CONSUMER_GROUP` | none | yes | Stable consumer group id for topic `retailnetwork.customer-ordering.events`. |
| `DELIVERY_PROMISE_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.delivery-promise.events`. |
| `LAST_MILE_DELIVERY_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.last-mile-delivery.events`. |
| `LINEHAUL_TRANSPORTATION_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.linehaul-transportation.events`. |
| `NODE_PERFORMANCE_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.node-performance.events`. |
| `NODE_TOPOLOGY_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.node-topology.events`. |
| `OFFER_AVAILABILITY_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.offer-availability.events`. |
| `SHIPPING_AND_TRACKING_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.shipping-and-tracking.events`. |
| `VENDOR_CONNECTIVITY_CONSUMER_GROUP` | none | yes | Group id for `retailnetwork.vendor-connectivity.events`. |
| `EVENT_PUBLISHER` | `kafka` | no | Only `kafka` (or unset) is accepted. `log` is refused ("not supported") and any other value is a boot error. |
| `HTTP_ADDR` | `:8080` | no | REST listen address. |
| `SWEEP_INTERVAL` | `15s` | no | Period of the virtual-queue sweep (releases due assignments, re-optimises the other queued ones). |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Poll interval of the outbox relay. |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Publish failures before the relay excludes a row; `0` = unlimited. Negative or non-numeric = default. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | Wait between flipping `/readyz` to 503 and closing the listener; `0` disables the wait. |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`), `error`; case-insensitive, anything else = `info`. |
| `OTEL_SERVICE_NAME` | `order-sourcing` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint, bare `host:port` (becomes `http://`) or a URL. Export never blocks. |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute. |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. |

The consumer-group variables must be stable and service-prefixed (the chart CI values use `order-sourcing-<producer>`).
Never derive them from a pod name: a rescheduled pod must resume from its committed offsets.

## cmd/mcp

Read-only. Dials Postgres only: no Kafka, no relay, no consumers, no sweep.

| Variable | Default | Required | Meaning |
| --- | --- | --- | --- |
| `DATABASE_URL` | none | yes | Postgres DSN (the database `cmd/api` owns). |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the idempotent boot migrations. |
| `MCP_ADDR` | `:8090` | no | Streamable HTTP listen address. |
| `LOG_LEVEL` | `info` | no | As for `cmd/api`. |
| `OTEL_SERVICE_NAME` | `order-sourcing-mcp` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint. |
| `SERVICE_VERSION` | `dev` | no | `service.version`, also logged at boot. |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name`, also logged at boot. |

## cmd/a2a

Read-only. Dials Postgres only. Both required variables are validated together and reported in one error.

| Variable | Default | Required | Meaning |
| --- | --- | --- | --- |
| `DATABASE_URL` | none | yes | Postgres DSN. |
| `A2A_AGENT_URL` | none | yes | Absolute `http(s)` URL of this agent's JSON-RPC endpoint, advertised on the Agent Card (for example `http://<release>-a2a.<ns>.svc.cluster.local:8093/`). Relative or schemeless values are refused at boot. |
| `A2A_ADDR` | `:8093` | no | Listen address. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the idempotent boot migrations. |
| `LOG_LEVEL` | `info` | no | As for `cmd/api`. |
| `OTEL_SERVICE_NAME` | `order-sourcing-a2a` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint. |
| `SERVICE_VERSION` | `dev` | no | `service.version`, logged at boot and shown on the Agent Card. |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name`, logged at boot. |

## Helm values to variables

`charts/order-sourcing/values.yaml` maps one key to one variable. The nine consumer groups
(`config.<producer>ConsumerGroup`) and `kafka.brokers` are required by the chart as well: it refuses to render without
them. See [Runbook](/docs/operations/runbook) for deployment details.
