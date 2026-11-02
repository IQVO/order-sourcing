---
id: runtime
title: Runtime
sidebar_label: Runtime
description: "How the three binaries boot, serve, drain and shut down: probes, consumers and the inbox, the outbox relay and the queue sweep."
---

# Runtime: boot, probes, shutdown and the outbox

Order Sourcing ships three binaries from one image. All three run on real PostgreSQL; only `cmd/api` also needs Kafka.
There is no in-memory mode and no log sink for events. Variable names, defaults and the Helm values that set them are in
[Configuration](/docs/operations/configuration); deployment is in the [Runbook](/docs/operations/runbook).

| Binary | Role | Default listener | Dials |
| --- | --- | --- | --- |
| `cmd/api` | REST API, nine Kafka consumers, virtual-queue sweep and outbox relay in one process | `:8080` (`HTTP_ADDR`) | Postgres, Kafka |
| `cmd/mcp` | Read-only MCP server, Streamable HTTP | `:8090` (`MCP_ADDR`) at `/mcp` and `/` | Postgres |
| `cmd/a2a` | Read-only Agent2Agent agent, JSON-RPC 2.0 | `:8093` (`A2A_ADDR`) at `/` and `/a2a` | Postgres |

Source: `cmd/api/main.go`, `cmd/mcp/main.go`, `cmd/a2a/main.go`.

## Boot sequence

`cmd/api`:

1. Read `LOG_LEVEL` and validate the mandatory environment (`DATABASE_URL`, `KAFKA_BROKERS`, the nine consumer groups,
   `EVENT_PUBLISHER`) before anything is opened or bound. A bad value is a boot error that names the variable.
2. Bring OpenTelemetry up (before the router is built, because the request-duration histogram binds the global
   `MeterProvider` at construction). Export never blocks.
3. Run the embedded golang-migrate migrations on `MIGRATIONS_DATABASE_URL` (default `DATABASE_URL`), then open the pool and
   ping Postgres. Both steps go through `internal/bootretry`: 5 attempts, 1 s base delay doubling between attempts (1+2+4+8 = 15 s),
   which absorbs the ~10 s reset of the first outbound dial of a pod behind a native Istio sidecar. After the fifth failure the
   process exits with `run migrations (after 5 attempts): ...` or `ping postgres (after 5 attempts): ...`.
4. Build the `/readyz` gate (Postgres ping and a Kafka broker metadata request), register the business counter, wire the
   use cases.
5. Start the nine consumers, the outbox relay and the queue sweep (none dials at construction; a broker that is down only
   makes passes fail until it is back), then serve HTTP.

`cmd/mcp` and `cmd/a2a` do the same for Postgres only (telemetry, migrations with retry, pool, ping) and then serve. They run the
idempotent migrations too, so either can boot against an empty database before `cmd/api` has; the golang-migrate advisory lock
makes concurrent starts safe. `cmd/a2a` validates `A2A_AGENT_URL` and `DATABASE_URL` together, before telemetry.

## Shutdown

`cmd/api`, on SIGINT or SIGTERM: `/readyz` flips to 503 `{"status":"not_ready"}`, wait `SHUTDOWN_DRAIN_DELAY` (default 5 s;
`0` skips), close the HTTP listener and drain in-flight requests (10 s bound), stop the consumers and the sweep and await each
(10 s bound), stop the relay and await it (10 s bound), close the pool, flush telemetry (5 s bound). The sum with the default
delay is 35 s, which is why the chart refuses a `terminationGracePeriodSeconds` that does not exceed `shutdownDrainDelay` + 30 s.

If any background worker (a consumer, the sweep or the relay) returns on its own, or the listener fails, the process stops
the rest and exits non-zero (`<worker> stopped unexpectedly: exiting so the orchestrator restarts the service`).

`cmd/mcp` and `cmd/a2a` have no readiness gate and no drain delay: on SIGINT or SIGTERM they close the listener with a 10 s
bound and flush telemetry.

## Probes

| Binary | Liveness | Readiness |
| --- | --- | --- |
| `cmd/api` | `GET /healthz`, always 200 `{"status":"ok"}`, even while draining or while a dependency is down | `GET /readyz` |
| `cmd/mcp`, `cmd/a2a` | `GET /healthz`, always 200 `{"status":"ok"}`, no dependency check | none (the chart uses `/healthz` for both) |

`GET /readyz` on `cmd/api` is 200 `{"status":"ready"}` only if the drain gate is open, Postgres answers a pool ping and a Kafka
broker answers a metadata request. The request names no topics (an empty, non-nil list), so it never creates a topic and does not
need an output topic to exist; a fresh connection is dialled on every check. While draining it answers 503
`{"status":"not_ready"}`; while a dependency is down, 503 `application/problem+json` with type slug `dependency-unavailable` and
detail `not ready: postgres`, `not ready: kafka` or both. Verdicts are cached for 3 s and one round of checks is bounded at 2 s
on a context detached from the caller, and each failing round logs a WARN `readiness: dependency is not ready`.
Source: `internal/adapters/inbound/http/readiness.go`, `internal/adapters/outbound/kafka/health.go`.

## Consumers and the inbox

Each consumed topic has its own reader, its own stable consumer group and its own retry loop
(`internal/adapters/inbound/kafka`). The loop fetches one message, handles it inside one unit of work and commits the offset only
after the handler returned (there is no auto-commit), so delivery is at-least-once with an atomic effect.

- Every use case claims the CloudEvents `id` in `processed_events` inside its own transaction, so a redelivered event changes
  nothing.
- A message that is not a valid CloudEvents 1.0 event, or whose `type` has no handler on that topic, is skipped (WARN for the
  former, silent for the latter): unknown types and unknown fields are ignored.
- A payload that can never be applied (undecodable `data`, a validation error) is dead-lettered at once when a DLQ writer is set
  (it always is in `cmd/api`).
- An event the aggregate can no longer take (not found, illegal transition, locked, release not pending, stale revision, stale
  assignment version) is logged at WARN and acknowledged.
- Any other error is transient: the same message is retried with capped exponential backoff (200 ms doubling to 5 s) up to
  5 attempts, then published to `<topic>.dlq` with `x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`,
  `x-dlq-error` and `x-dlq-failed-at` headers. The DLQ publish itself is retried until it succeeds.
- A stalled or reset broker connection on fetch is retried with the same backoff up to 10 consecutive failures; after that the
  consumer fails and the process exits for a restart.

## The outbox in operation

- Events are written in the same transaction as the aggregate and published later by the relay, so a write is accepted while
  Kafka is down; rows with `outbox_events.published_at IS NULL` wait and are published, in id order, when the broker is back.
- The relay polls every `OUTBOX_RELAY_INTERVAL` (default 1 s), claims up to 100 rows per pass, and runs another pass at once
  after a full batch. Claims use `FOR UPDATE SKIP LOCKED`, so more than one replica is safe.
- Delivery is at-least-once with a stable CloudEvents `id` (minted at encode time and stored with the row); consumers deduplicate.
- Writes use RequireAll acks, the hash balancer on the key (the assignment id, so one assignment's events stay ordered) and
  allow topic auto-creation.
- A row that failed `OUTBOX_MAX_ATTEMPTS` times is skipped (logged once at ERROR with id, topic, type and key) and stays in the
  table with `attempts` and `last_error`. Fix the cause, then re-arm it:
  `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`. While a row is skipped, later events of the same assignment can
  overtake it.
- Idempotency keys older than 24 h are ignored on lookup (the row is overwritten on the next use of the same key). Nothing in
  the code deletes rows from `idempotency_keys`, `processed_events` or published `outbox_events`; they grow until an operator
  prunes them.
- Every Postgres connection has `statement_timeout = 5s` and the pool holds at most 10 connections per process
  (`internal/adapters/outbound/postgres/pool.go`).

Source: `internal/adapters/outbound/outbox/relay.go`, `internal/adapters/outbound/kafka/relay_sink.go`,
`internal/adapters/outbound/postgres/outbox.go`.

## The queue sweep

Every `SWEEP_INTERVAL` (default 15 s, also once at start) `cmd/api` runs `SweepQueue` in pages of 100: it releases every `QUEUED`
assignment whose `releaseAt` has passed (`ReleaseToNode`), then walks every page of the other queued assignments and
re-optimises each (`ReoptimiseAssignment`). A row another writer changed meanwhile, that no longer exists, that is already locked or
that has a release in flight is counted as skipped, not as an error. A pass logs `queue sweep` with `released`, `reoptimised` and
`skipped` only when it did something. Source: `cmd/api/main.go` (`startSweep`, `sweepOnce`),
`internal/application/usecases/sweep.go`.
