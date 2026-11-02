---
id: runbook
title: Runbook
sidebar_label: Runbook
description: "Operating Order Sourcing: components, deployment with the Helm chart, probes, migrations, Kafka topics and consumer groups, outbox relay, dead letters, the queue sweep and routine procedures."
---

# Order Sourcing runbook

Order Sourcing decides, as late as the promise allows, which node and path will fulfil each shipment group of a placed order.
One `FulfillmentAssignment` per group sits in a virtual queue where its candidate (node, path, cost) is re-optimised against
current capacity until the release point, is released to one node at a time, reassigned on refusal or expiry, locked when the
node commits, and followed through `SHIPPED` and `DELIVERED`; the service also decides cancels and applies revised promises.
Everything it knows about the network is a local read model fed by sibling events: it calls no sibling API
([Introduction](/docs/overview/introduction), [ADR 0002](/docs/adr/0002-aggregates-invariants-and-assumptions),
[ADR 0003](/docs/adr/0003-consumed-contracts-and-local-read-models)).

## Components

| Component | Binary | Chart switch | Port | Exposure in the fleet (`warehouse-infra` `helm-values/order-sourcing.yaml`) |
| --- | --- | --- | --- | --- |
| `api` Deployment | `cmd/api` | always on | 8080 (Service port 80) | Kong route `/api/order-sourcing`, prefix stripped |
| `mcp` Deployment | `cmd/mcp` | `mcp.enabled` (default off, on in the fleet) | 8090 | Kong route `/api/mcp/order-sourcing`, rewritten to `/mcp` |
| `a2a` Deployment | `cmd/a2a` | `a2a.enabled` (default off, on in the fleet) | 8093 | Kong route `/api/a2a/order-sourcing`, rewritten to `/`; the Agent Card stays under the same prefix |
| `frontend` Deployment | nginx serving `web/` | `frontend.enabled` (default off, on in the fleet) | 8080 | the web gateway, `/mfes/order-sourcing/` (never the API route) |

The three Go binaries are in one image (`ghcr.io/iqvo/order-sourcing`). `cmd/api` is the only one that writes: one process runs
the REST API, nine Kafka consumers, the transactional-outbox relay and the virtual-queue sweep. `cmd/mcp` and `cmd/a2a` are
read-only and only read Postgres. See [Runtime](/docs/overview/runtime) for how each boots and drains.

## Dependencies

- **Postgres** (schema in `internal/adapters/outbound/postgres/migrations/0001_order_sourcing_schema.up.sql`): `assignments`,
  the `rm_*` read models, `outbox_events`, `processed_events`, `idempotency_keys`. Every binary runs the embedded migrations at
  boot, then pings, each wrapped in `bootretry` (5 attempts, 15 s of backoff in total).
- **PgBouncer versus a direct DSN**: `DATABASE_URL` is the pooled DSN the pool uses; `MIGRATIONS_DATABASE_URL` (an optional key in
  the same secret) is a direct, session-mode DSN used only by the migration step, because golang-migrate holds a session
  advisory lock a transaction-pooling proxy cannot honour. If unset, the binary falls back to `DATABASE_URL`. In the fleet both
  come from the Secret `order-sourcing-db`.
- **Kafka** (`KAFKA_BROKERS`, comma-separated; one broker platform-wide): used by `cmd/api` only (the relay, the nine consumers and
  the `/readyz` broker check). Topics are auto-created by the first writer; this service never creates the topics it consumes.

### Kafka topics and consumer groups

| Direction | Topic | Consumer group (variable, fleet value) |
| --- | --- | --- |
| consumes | `retailnetwork.customer-ordering.events` | `CUSTOMER_ORDERING_CONSUMER_GROUP`, `order-sourcing-customer-ordering` |
| consumes | `retailnetwork.delivery-promise.events` | `DELIVERY_PROMISE_CONSUMER_GROUP`, `order-sourcing-delivery-promise` |
| consumes | `retailnetwork.last-mile-delivery.events` | `LAST_MILE_DELIVERY_CONSUMER_GROUP`, `order-sourcing-last-mile-delivery` |
| consumes | `retailnetwork.linehaul-transportation.events` | `LINEHAUL_TRANSPORTATION_CONSUMER_GROUP`, `order-sourcing-linehaul-transportation` |
| consumes | `retailnetwork.node-performance.events` | `NODE_PERFORMANCE_CONSUMER_GROUP`, `order-sourcing-node-performance` |
| consumes | `retailnetwork.node-topology.events` | `NODE_TOPOLOGY_CONSUMER_GROUP`, `order-sourcing-node-topology` |
| consumes | `retailnetwork.offer-availability.events` | `OFFER_AVAILABILITY_CONSUMER_GROUP`, `order-sourcing-offer-availability` |
| consumes | `retailnetwork.shipping-and-tracking.events` | `SHIPPING_AND_TRACKING_CONSUMER_GROUP`, `order-sourcing-shipping-and-tracking` |
| consumes | `retailnetwork.vendor-connectivity.events` | `VENDOR_CONNECTIVITY_CONSUMER_GROUP`, `order-sourcing-vendor-connectivity` |
| produces | `retailnetwork.order-sourcing.events` | integration events (8 types) |
| produces | `retailnetwork.order-sourcing.analytics` | analytics-only events (`FulfillmentQueued`, `FulfillmentDelivered`) |
| dead letter | `<consumed topic>.dlq` | written by the consumers, read by no one in this service |

The consumer group ids are stable names you choose (the fleet values come from `warehouse-infra`
`helm-values/order-sourcing.yaml`). Never derive one from a pod name: a rescheduled pod must resume from its committed offsets,
and a brand-new group re-reads the topic from the start. The event types per topic are in
[Events](/docs/api-reference/events) and [Integration](/docs/ecosystem/integration).

## Deployment (Helm chart)

`charts/order-sourcing/` renders the `api` Deployment plus the optional `mcp`, `a2a` and `frontend` Deployments, a Service for
each, a ConfigMap mounted with `envFrom`, a Secret (or `database.existingSecret`) for `DATABASE_URL`, a ServiceAccount, and an
HPA, Ingress, Gateway API HTTPRoute (api) and MCP/A2A routes that are off by default. The image is
`ghcr.io/iqvo/order-sourcing`; an empty `image.tag` resolves to the chart `appVersion`, which the `release` job sets to the
release version. Every key under `config.*` maps one-to-one to a `cmd/api` variable (see
[Configuration](/docs/operations/configuration)).

| Value | Variable | Rule |
| --- | --- | --- |
| `database.url` or `database.existingSecret` (+ `existingSecretKey`) | `DATABASE_URL` | Mandatory. Always a `secretKeyRef`; the chart Secret is created only from `database.url`; the DSN never lands in the ConfigMap. |
| `database.migrationsExistingSecretKey` | `MIGRATIONS_DATABASE_URL` | `secretKeyRef` with `optional: true` (the binary falls back to `DATABASE_URL`); empty drops the variable. |
| `kafka.brokers` | `KAFKA_BROKERS` | Mandatory. |
| `config.<producer>ConsumerGroup` (nine) | `<PRODUCER>_CONSUMER_GROUP` | Mandatory, stable, service-prefixed (`order-sourcing-<producer>` in `ci/default-values.yaml`); a value containing `$(` (pod-name derived) is refused. |
| `config.eventPublisher` | `EVENT_PUBLISHER` | Only `kafka`. |
| `config.outboxRelayInterval`, `outboxMaxAttempts`, `sweepInterval`, `httpAddr`, `logLevel` | `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, `SWEEP_INTERVAL`, `HTTP_ADDR`, `LOG_LEVEL` | Optional ones are not rendered while empty, so the binary default applies. |
| `config.shutdownDrainDelay` | `SHUTDOWN_DRAIN_DELAY` | Whole seconds (`5s`), always rendered. |
| `terminationGracePeriodSeconds` | n/a | Default 60. The chart refuses a value that does not exceed `shutdownDrainDelay` + 30 s (HTTP 10 s + consumers and sweep 10 s + relay 10 s). |
| `otel.enabled`, `otel.endpoint`, `otel.serviceName`, `environment` | `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `ENVIRONMENT` (and `SERVICE_VERSION` from the image tag) | `otel.enabled=false` renders no `OTEL_*` variable and the binaries fall back to `localhost:4317`. The `mcp` component gets `<serviceName>-mcp`. |
| `mcp.enabled`, `mcp.httpAddr`, `mcp.route.*` | `MCP_ADDR` | `mcp.httpAddr` must equal `mcp.service.targetPort`. |
| `a2a.enabled`, `a2a.httpAddr`, `a2a.agentUrl`, `a2a.route.*` | `A2A_ADDR`, `A2A_AGENT_URL` | Empty `agentUrl` defaults to the in-cluster Service DNS `http://<release>-a2a.<ns>.svc.cluster.local:8093/`; set it to the Kong URL when the agent must be reachable off-cluster. |
| `extraEnv` | n/a | Refuses `EVENT_PUBLISHER` other than `kafka`, any `*_MODE`, any `*_CONSUMER_GROUP` and any variable the chart renders itself. |

The pods run as uid 1000 with a read-only root filesystem, `allowPrivilegeEscalation: false`, all capabilities dropped and the
`RuntimeDefault` seccomp profile. Probes: startup and liveness on `/healthz`, readiness on `/readyz` (api); the startup probe
allows about 60 s (2 s period, 30 failures). Default `replicaCount: 1`, requests 100m CPU and 128Mi memory, limits 500m and 256Mi.
More than one `api` replica is safe by design (the relay claims rows with `FOR UPDATE SKIP LOCKED`, aggregate writes are
version-guarded, the consumers have stable groups and an inbox) but has not been load-tested; the optional HPA
(`autoscaling.api.*`) is off by default.

The chart tests are `charts/order-sourcing/tests/render_test.sh` (needs helm) and `test_service_selectors.py` (helm and PyYAML);
the `chart-tests` CI job runs both on every push and the `helm-lint` job runs `ct lint` on pull requests. The packaging checklist
is [Before the first release](/docs/operations/before-first-release).

```sh
helm template order-sourcing charts/order-sourcing -f charts/order-sourcing/ci/default-values.yaml   # dummy values, nothing is dialled
bash charts/order-sourcing/tests/render_test.sh
python3 charts/order-sourcing/tests/test_service_selectors.py
```

## Migrations

A single migration, `0001_order_sourcing_schema`, is embedded in the binary (`internal/adapters/outbound/postgres/migrations/`).
It runs at boot in all three binaries through golang-migrate over `MIGRATIONS_DATABASE_URL`, so a config change or a new image is a
rollout that migrates before serving. State lives in `schema_migrations`; a failed partial migration leaves `dirty = true` and
every later boot fails until the schema is repaired by hand and the version forced with the golang-migrate tooling (take a
backup first).

## Health

- `GET /healthz` is liveness: always `200 {"status":"ok"}`; it never checks dependencies and never flips during shutdown.
- `GET /readyz` (`cmd/api` only) is readiness: `200 {"status":"ready"}`; `503 {"status":"not_ready"}` once shutdown began
  draining; `503 problem+json` slug `dependency-unavailable` naming the failing dependency while Postgres (pool ping) or Kafka
  (broker metadata) is unreachable. Verdicts are cached for 3 s, one round is bounded at 2 s.
- **Writes while a dependency is down (per code)**: the REST write path never dials Kafka. The aggregate and its `outbox_events`
  row commit in one Postgres transaction and only the relay publishes, so the process still accepts and queues writes while Kafka
  is down; `/readyz` turning 503 removes the pod from Service endpoints, so new external traffic stops at the router. With Postgres
  down every write fails (each is a Postgres transaction) and the sweep, relay and consumers only log errors.

## Outbox relay

`cmd/api` drains `outbox_events` to Kafka every `OUTBOX_RELAY_INTERVAL` (default 1 s, batch 100, another pass at once after a full
batch), at-least-once, with the CloudEvents `id` fixed at encode time so a retry republishes the same id.

```sql
-- backlog and oldest waiting row
SELECT count(*), min(created_at) FROM outbox_events WHERE published_at IS NULL;
-- rows parked at the attempt cap (use your OUTBOX_MAX_ATTEMPTS, default 20)
SELECT id, topic, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL AND attempts >= 20 ORDER BY id;
-- re-arm one row after fixing the cause
UPDATE outbox_events SET attempts = 0 WHERE id = <id>;
```

A row whose failure count reaches `OUTBOX_MAX_ATTEMPTS` is excluded from claims, stays in the table with `attempts` and
`last_error`, and is logged once at ERROR with `outbox_id`, `topic`, `event_type` and `key`. Later rows overtake it. `0` means
unlimited.

## Dead letters

A consumer that cannot apply a message writes it to `<topic>.dlq` with the original key, value and headers plus
`x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`, `x-dlq-error` and `x-dlq-failed-at`. Two paths lead there:
a payload that can never be applied (dead-lettered at once) and a transient failure that outlasted 5 attempts. Messages that are
not CloudEvents, have a type this service does not handle, or are stale for the aggregate are skipped, not dead-lettered.
Nothing in this repository reads or replays a `.dlq` topic. To replay, fix the cause, then publish the dead-lettered value back
to the source topic with your Kafka tooling; the inbox on the CloudEvents `id` makes a message that was in fact applied a no-op.

## Virtual-queue sweep

Every `SWEEP_INTERVAL` (default 15 s) the sweep releases due assignments and re-optimises the queued ones page by page (100 rows).
It logs `queue sweep` with `released`, `reoptimised` and `skipped` only when it did something, and `queue sweep pass failed` at
ERROR on a database error. To force one assignment, call `POST /assignments/{id}/reoptimisations` with an `Idempotency-Key` header
([API overview](/docs/api-reference)). There is no REST operation that releases early.

## Alerts and dashboards (from `warehouse-infra` `origin/develop`)

- Grafana dashboard `warehouse-order-sourcing` (`terraform/dashboards/contexts/order-sourcing.json`): Kong request rate, 5xx ratio
  and p95 latency for the context's routes; service HTTP rate and p95 by process; the business counter
  `sourcing_groups_sourced_total` by `outcome`; Go goroutines and memory; the log stream, log volume by level and an
  errors-and-warnings panel from Loki (`{app="order-sourcing"}`). Some panel text still mentions a projector and reports
  component that this service does not have.
- Alert rule groups `warehouse-order-sourcing` (`WarehouseKong5xxRatioHigh` warning after 5 m, `WarehousePodRestarting`
  warning) and `warehouse-order-sourcing-slo` (`WarehouseApiAvailabilityBurnFast` and `WarehouseApiLatencyBurnFast` critical after
  2 m, `WarehouseApiAvailabilityBurnSlow` and `WarehouseApiLatencyBurnSlow` warning after 15 m) in
  `terraform/alerting/warehouse-alert-rules.json`. There is no outbox-lag, consumer-lag or dead-letter alert for this context.
- Those alerts carry `runbook_url` `https://github.com/IQVO/order-sourcing/blob/develop/docs/operations/runbook.md`, a path that no
  longer exists once this site replaces it; the rules live in `warehouse-infra` and must be updated there.

See [Observability](/docs/operations/observability) for the instruments, spans and log fields.

## Failure modes

Symptom-by-symptom tables are in [Troubleshooting](/docs/operations/troubleshooting). The short list:

- **Pod crash-loops**: a mandatory variable is missing (the error names it), Postgres migrations or ping exhausted `bootretry`, or
  a background worker died. `kubectl -n warehouse-systems logs deploy/order-sourcing` shows the cause.
- **Postgres unreachable**: `/readyz` 503 naming `postgres`, `/healthz` stays 200 so the pod is not restarted, writes fail, events
  already in `outbox_events` wait.
- **Kafka unreachable**: `/readyz` 503 naming `kafka`, writes are still accepted and queued in `outbox_events`, the relay retries
  every interval. Restore Kafka and the outbox drains by itself.
- **A sibling has never produced**: its topic does not exist yet, its consumer retries with only logs. Retries at boot are expected
  until every sibling has produced once.

## Routine procedures

| Task | How |
| --- | --- |
| Restart | `kubectl -n warehouse-systems rollout restart deploy/order-sourcing` (the pod re-runs migrations at boot) |
| Read logs | Grafana Loki `{app="order-sourcing"}`; JSON fields include `consumer`, `topic`, `error`, `outbox_id`, `trace_id`, `request_id` |
| Change configuration | change the Helm values (a config change is a rollout); consumer group changes re-read the topic from the start |
| Inspect an assignment | `GET /assignments/{id}` or the MCP tool `get_assignment` ([MCP tools](/docs/mcp/tools)) |
| Re-optimise one queued assignment | `POST /assignments/{id}/reoptimisations` with `Idempotency-Key` and optionally `{"assignmentVersion": N}` |
| Prune bookkeeping tables | nothing prunes `idempotency_keys`, `processed_events` or published `outbox_events`; plan retention in SQL yourself (keep `processed_events` as long as a topic can be re-read) |
