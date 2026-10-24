# Order Sourcing runbook

Order Sourcing is a Core bounded context of the retail-network DDD study (brand-free,
illustrative data only). It decides, as late as the promise allows, which node and path
will fulfil each shipment group: one `FulfillmentAssignment` per group sits in a virtual
queue where its candidate (node, path, cost) is re-optimised against current capacity
until the release point, is released to one node at a time, reassigned on refusal or
expiry, locked when the node commits, and followed through `SHIPPED`/`DELIVERED`; it also
decides cancels and applies revised promises. Everything it knows about the network is a
local read model fed by sibling events — it calls no sibling REST API (`README.md`,
`docs/adr/0002`, `docs/adr/0003`).

## Runtime shape

One component: the `api` Deployment (`cmd/api/main.go`, chart `charts/order-sourcing`,
default `replicaCount: 1`). A single process runs:

- **REST API** on `:8080` (`HTTP_ADDR`): `GET /assignments`, `GET /assignments/{id}`,
  `POST /assignments/{id}:reoptimise`, `GET /healthz`, `GET /readyz`. State-changing
  requests require an `Idempotency-Key` header, replayed for 24 h (`idempotency_keys`
  table). Externally reached via the Kong route `/api/order-sourcing` (prefix stripped).
- **Transactional-outbox relay**: drains `outbox_events` to Kafka every
  `OUTBOX_RELAY_INTERVAL` (default 1 s, batch 100); at-least-once, CloudEvents id fixed
  at encode time so a retry republishes the same id (`internal/adapters/outbound/outbox/relay.go`).
- **Nine Kafka consumers**, one per sibling topic (`internal/adapters/inbound/kafka/handlers.go`),
  32 consumed event types; handler failures retry with capped backoff (200 ms doubling,
  cap 5 s), deterministic poison is dead-lettered to `<topic>.dlq` after 5 attempts
  (`internal/adapters/inbound/kafka/kafka.go`, `deadletter.go`); duplicates deduped by
  the `processed_events` inbox on the CloudEvents id.
- **Virtual-queue sweep**: every `SWEEP_INTERVAL` (default 15 s) releases due
  assignments and walks every page (100 rows) of queued ones, re-optimising each
  (`cmd/api/main.go` `startSweep`, `internal/application/usecases/sweep.go`).

Any background worker that exits takes the process down on purpose so the orchestrator
restarts it (`serveUntilSignal` in `cmd/api/main.go`). Shutdown is sequential: readiness
flips to 503 first, waits `SHUTDOWN_DRAIN_DELAY` (5 s), closes the listener (10 s), then
drains consumers/sweep and relay (10 s each); `terminationGracePeriodSeconds: 60`.

## Dependencies

- **Postgres** (schema in `internal/adapters/outbound/postgres/migrations/0001_*.sql`:
  `assignments`, `rm_*` read models, `outbox_events`, `processed_events`,
  `idempotency_keys`). Boot runs migrations then pings, each wrapped in `bootretry`
  (5 attempts, 15 s backoff — survives the fleet's ~10 s first-dial sidecar reset).
- **PgBouncer vs direct DSN**: `DATABASE_URL` is the pooled DSN the pool uses;
  `MIGRATIONS_DATABASE_URL` (optional key in the same secret) is a direct, session-mode
  DSN used only by the migration step, because golang-migrate holds a session advisory
  lock a transaction-pooling proxy cannot honour. If unset, the binary falls back to
  `DATABASE_URL`. In the fleet both come from the Secret `order-sourcing-db`.
- **Kafka** (`KAFKA_BROKERS`, comma-separated): the relay, the nine consumers and the
  `/readyz` kafka check all dial it. Consumed topics
  `retailnetwork.{customer-ordering,delivery-promise,last-mile-delivery,linehaul-transportation,node-performance,node-topology,offer-availability,shipping-and-tracking,vendor-connectivity}.events`;
  produced `retailnetwork.order-sourcing.events` (8 types) and
  `retailnetwork.order-sourcing.analytics` (2 types). Topics are auto-created by the
  first writer — this service does not pre-create the topics it consumes.

## Health

- `GET /healthz` — liveness: always `200 {"status":"ok"}`; it never checks dependencies
  and never flips during shutdown (`internal/adapters/inbound/http/server.go:48`,
  asserted by `readiness_test.go` and `cmd/api/e2e_integration_test.go`).
- `GET /readyz` — readiness: `200 {"status":"ready"}`; `503 {"status":"not_ready"}`
  once shutdown began draining; `503 problem+json` slug `dependency-unavailable`
  naming the failing dependency while Postgres (pool ping) or Kafka (broker metadata)
  is unreachable. Verdicts are cached 3 s and one check round is bounded at 2 s
  (`internal/adapters/inbound/http/readiness.go`).
- Chart probes: startup and liveness on `/healthz`, readiness on `/readyz`
  (`charts/order-sourcing/values.yaml`).
- **Writes while a dependency is down (per code)**: the REST write path never dials
  Kafka — the aggregate and its `outbox_events` row commit in one Postgres transaction,
  and only the relay publishes — so the process itself still accepts writes and queues
  them while Kafka is down; `/readyz` turning 503 simultaneously removes the pod from
  the Service endpoints, so new external traffic stops at the router. With Postgres
  down every write fails (each is a Postgres transaction) and the sweep/relay/consumers
  only log errors.

## Configuration

Env vars read by `cmd/api/main.go`; chart value that sets each (`charts/order-sourcing/values.yaml` `config.*` maps 1:1).

| Env var | Chart value | Default | Meaning |
| --- | --- | --- | --- |
| `DATABASE_URL` | `database.url` / `database.existingSecret` | — (required) | pooled Postgres DSN; empty = boot error |
| `MIGRATIONS_DATABASE_URL` | `database.migrationsExistingSecretKey` | falls back to `DATABASE_URL` | direct DSN for the boot migrations only |
| `KAFKA_BROKERS` | `kafka.brokers` | — (required) | comma-separated `host:port` |
| `HTTP_ADDR` | `config.httpAddr` | `:8080` | REST listen address |
| `LOG_LEVEL` | `config.logLevel` | `info` | `debug|info|warn|error`, JSON logs on stdout |
| `EVENT_PUBLISHER` | `config.eventPublisher` | `kafka` | only `kafka` (or empty) is accepted; `log` refused at boot |
| `OUTBOX_RELAY_INTERVAL` | `config.outboxRelayInterval` | `1s` | relay pass period; Go duration or plain seconds |
| `OUTBOX_MAX_ATTEMPTS` | `config.outboxMaxAttempts` | `20` | failures before a row is excluded from claims; `0` = unlimited |
| `SWEEP_INTERVAL` | `config.sweepInterval` | `15s` | virtual-queue sweep period |
| `SHUTDOWN_DRAIN_DELAY` | `config.shutdownDrainDelay` | `5s` | wait between `/readyz` 503 and closing the listener |
| `CUSTOMER_ORDERING_CONSUMER_GROUP` … `VENDOR_CONNECTIVITY_CONSUMER_GROUP` (nine) | `config.customerOrderingConsumerGroup` … `config.vendorConnectivityConsumerGroup` | — (all required) | stable, service-prefixed consumer group id per topic (`order-sourcing-<context>` in the fleet); an empty one fails chart render and boot |

## Alerts and dashboards

- Grafana dashboard uid `warehouse-order-sourcing`; pod-restart alerts exist for this
  Deployment. Logs are structured JSON on stdout, queryable in Grafana Loki with
  `app=order-sourcing`.
- No business metrics are instrumented yet: business-metric panels on the dashboard
  stay empty until the service emits its own metrics. Do not expect assignment,
  outbox or lag panels to show data.

## Failure modes

- **Pod crash-loops**: the binary exits on purpose when a mandatory env var is missing
  (the error names it), Postgres migrations/ping exhaust `bootretry` (5 attempts), or a
  background worker dies. Diagnose `kubectl -n warehouse-systems logs deploy/order-sourcing`:
  a boot error names the exact missing variable; `run migrations (after 5 attempts)` /
  `ping postgres (after 5 attempts)` means Postgres (or the PgBouncer DSN) is wrong or
  down — the ~10 s sidecar first-dial reset is already absorbed by the retry.
- **Postgres unreachable**: `/readyz` 503 `dependency-unavailable` naming `postgres`;
  `/healthz` stays 200 so the pod is not restarted. REST writes fail, relay passes log
  `outbox relay pass failed`, the sweep logs `queue sweep pass failed`. Nothing is lost:
  events already in `outbox_events` wait. Action: check the Secret `order-sourcing-db`
  and PgBouncer, then the database.
- **Kafka unreachable**: `/readyz` 503 naming `kafka`, `/healthz` 200 (asserted in
  `cmd/api/e2e_integration_test.go`). The process keeps accepting writes and queuing
  them in `outbox_events` (rows with `published_at IS NULL`); the relay logs failed
  passes and retries every interval, so nothing is lost. Action: restore Kafka; the
  outbox drains on its own; the pod being not-ready already stopped new traffic.
- **Outbox relay stuck / row parked after max attempts**: a row whose failure count
  reaches `OUTBOX_MAX_ATTEMPTS` (20) is excluded from future claims, stays in
  `outbox_events` with its `attempts` and `last_error`, and is logged once at ERROR:
  `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm
  it with attempts = 0 after fixing the cause` (with `outbox_id`, `topic`, `event_type`,
  `key`). Later rows overtake it. Diagnose backlog with
  `SELECT id, topic, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL ORDER BY id;`
  (index `outbox_events_unpublished_idx`). After fixing the cause, re-arm:
  `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`
- **Consumer lag or retry logs**: this service consumes nine sibling streams and topics
  are auto-created by the first writer, so a sibling that has never produced leaves its
  consumer retrying with only logs — retries at boot are EXPECTED until every sibling
  has produced at least once; a quiet topic is not an incident by itself. Poison
  messages land on `<topic>.dlq` after 5 handler attempts; check `.dlq` topics and
  Loki (`app=order-sourcing`) for `handling` retries before suspecting lag.

## Operational actions

- Restart: `kubectl -n warehouse-systems rollout restart deploy/order-sourcing`.
- Logs: Grafana Loki, `app=order-sourcing` (JSON slog fields: `consumer`, `topic`,
  `error`, `outbox_id`, …).
- Redeploy/config change: the Helm chart `charts/order-sourcing` (values per the table
  above); a config change is a rollout, the pod re-runs migrations at boot.
