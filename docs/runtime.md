# Runtime: configuration, probes, shutdown and the outbox

`cmd/api` is the only binary. It runs on real PostgreSQL and real Kafka; there is no alternative runtime and no
log sink. One process serves the REST API, runs the nine Kafka consumers, the virtual-queue sweep and the outbox relay.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres DSN of the service's pool. A boot error names it when unset. |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations (golang-migrate holds a session advisory lock, which breaks behind a transaction-pooling proxy). |
| `KAFKA_BROKERS` | required | Comma-separated `host:port` list (relay, consumers and `/readyz`). A boot error names it when unset or blank. |
| `CUSTOMER_ORDERING_CONSUMER_GROUP`, `DELIVERY_PROMISE_CONSUMER_GROUP`, `LAST_MILE_DELIVERY_CONSUMER_GROUP`, `LINEHAUL_TRANSPORTATION_CONSUMER_GROUP`, `NODE_PERFORMANCE_CONSUMER_GROUP`, `NODE_TOPOLOGY_CONSUMER_GROUP`, `OFFER_AVAILABILITY_CONSUMER_GROUP`, `SHIPPING_AND_TRACKING_CONSUMER_GROUP`, `VENDOR_CONNECTIVITY_CONSUMER_GROUP` | required | One stable, service-prefixed consumer group id per consumed topic. A boot error names the missing one. |
| `HTTP_ADDR` | `:8080` | Listen address. |
| `SWEEP_INTERVAL` | `15s` | Period of the virtual-queue sweep: it releases every assignment whose `releaseAt` has passed and re-optimises the other queued ones (ADR 0002 A18). |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Poll interval of the relay (Go duration or seconds). |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row; `0` = unlimited (ADR 0005). |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait between flipping `/readyz` to 503 and closing the listener; `0` disables. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `EVENT_PUBLISHER` | `kafka` | Only `kafka` (or unset) is accepted; any other value is a boot error. |

Configuration errors are raised before anything is opened or bound.

## Boot and shutdown

1. Validate the environment. 2. Run the embedded migrations and ping Postgres, both with exponential backoff.
3. Start the consumers, the queue sweep and the outbox relay (none dials at construction; a broker that is down only
makes passes fail until it is back). 4. Serve.

On SIGINT/SIGTERM: `/readyz` flips to 503 `not_ready`, wait `SHUTDOWN_DRAIN_DELAY`, drain HTTP (10 s), stop the
consumers and the sweep and await them (10 s each), stop and await the relay (10 s), close the pool. If a worker
returns on its own the process exits non-zero so the orchestrator restarts it.

## Probes

- `GET /healthz` is liveness: 200 `{"status":"ok"}` while the process serves, even while draining or while a
  dependency is down.
- `GET /readyz` is 200 `{"status":"ready"}` only if the drain gate is open, Postgres answers a ping and a Kafka broker
  answers a metadata request. The request names no topics (an empty, non-nil list), so it never creates a topic and does
  not need the output topic to exist; a fresh connection is dialled on every check. While draining it answers 503
  `{"status":"not_ready"}`; while a dependency is down, 503 `application/problem+json` of type
  `dependency-unavailable` naming it. Verdicts are cached 3 s.

## Consumers and the inbox

Each consumed topic has its own consumer group and a retry loop with capped exponential backoff (200 ms to 5 s). A
message that keeps failing is written to `<topic>.dlq` with `x-dlq-*` headers after a bounded number of attempts, so
one poison message never blocks a partition. Every use case claims the CloudEvents `id` in `processed_events` inside
its own transaction, so a redelivered event changes nothing. Unknown types and unknown fields are ignored.

## The outbox in operation

- Events are written in the same transaction as the aggregate and published later, so a write is accepted while Kafka
  is down; `outbox_events.published_at IS NULL` rows wait and are published, in id order, when the broker is back.
- Delivery is at-least-once with a stable CloudEvents `id`; consumers deduplicate.
- A row that failed `OUTBOX_MAX_ATTEMPTS` times is skipped (logged once at ERROR with id, topic, type and key) and
  stays in the table with `attempts` and `last_error`. Fix the cause, then re-arm it:
  `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`. While a row is skipped, later events of the same assignment
  can overtake it.
- Idempotency keys older than 24 h are purged on the next keyed write.

## Deployment (Helm chart)

`charts/order-sourcing/` renders the `api` component only. Every key under `config.*` maps 1:1 to one variable above.

| Value | Variable | Rule |
| --- | --- | --- |
| `database.url` or `database.existingSecret` (+ `existingSecretKey`) | `DATABASE_URL` | Mandatory. Always a `secretKeyRef`; the chart Secret is created only from `database.url`; the DSN never lands in the ConfigMap. |
| `database.migrationsExistingSecretKey` | `MIGRATIONS_DATABASE_URL` | `secretKeyRef` with `optional: true` (the binary falls back to `DATABASE_URL`); empty drops the variable. |
| `kafka.brokers` | `KAFKA_BROKERS` | Mandatory. |
| `config.<producer>ConsumerGroup` (nine) | `<PRODUCER>_CONSUMER_GROUP` | Mandatory, stable, service-prefixed (`order-sourcing-<producer>` in `ci/default-values.yaml`); a value containing `$(` (pod-name derived) is refused. |
| `config.eventPublisher` | `EVENT_PUBLISHER` | Only `kafka`. |
| `config.outboxRelayInterval`, `outboxMaxAttempts`, `sweepInterval`, `httpAddr`, `logLevel` | `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, `SWEEP_INTERVAL`, `HTTP_ADDR`, `LOG_LEVEL` | Optional ones are not rendered while empty, so the binary default applies. |
| `config.shutdownDrainDelay` | `SHUTDOWN_DRAIN_DELAY` | Whole seconds (`5s`), always rendered. |
| `terminationGracePeriodSeconds` | n/a | Default 60. The chart refuses a value that does not exceed `shutdownDrainDelay` + 30 s (HTTP 10 s + consumers and sweep 10 s + relay 10 s, see Boot and shutdown). |
| `extraEnv` | n/a | Refuses `EVENT_PUBLISHER` other than `kafka`, any `*_MODE` and any `*_CONSUMER_GROUP`. |

The pod runs as uid 1000 with a read-only root filesystem, `allowPrivilegeEscalation: false`, all capabilities dropped and the `RuntimeDefault` seccomp profile; liveness
is `/healthz`, readiness `/readyz`, with a startup probe on `/healthz`. The chart tests are `charts/order-sourcing/tests/render_test.sh` (needs helm) and
`test_service_selectors.py` (helm and PyYAML); the `chart-tests` CI job runs both on every push and the `helm-lint` job runs `ct lint` on pull requests.
The image is `ghcr.io/iqvo/order-sourcing`; an empty `image.tag` resolves to the chart `appVersion`, which the `release` job sets to the release version.

## Tests

`make check` (format, vet, build, lint, unit + handler + BDD tests), `make coverage` (90% over `internal/domain` and
`internal/application`), `make mutation` (gremlins per domain package), `make arch-test` (fitness), `make integration`
(testcontainers Postgres + Kafka: repositories, the outbox drain, the middleware and the real-wiring test
`cmd/api/e2e_integration_test.go`; needs Docker).
