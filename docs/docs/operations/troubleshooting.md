---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
description: "Symptom, cause, check and fix for the failure modes visible in the code of the three binaries, plus the REST problem types."
---

# Troubleshooting

Symptom, cause, check, fix, built from the failure modes visible in the code. Log lines are the literal `msg` values in the
source (see [Observability](/docs/operations/observability) for the field names); none was captured from a run. SQL probes assume
the schema in the [Runbook](/docs/operations/runbook#migrations). This service has no outbound HTTP, MCP or A2A client, so there is
no circuit breaker to trip: the dependencies that can fail are Postgres and, for `cmd/api`, Kafka.

## Boot

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| `cmd/api` exits 1: `DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode` | `DATABASE_URL` unset or empty | `service exited with error` ERROR line; env of the pod | Set it ([Configuration](/docs/operations/configuration)); the chart refuses to render without `database.url` or `database.existingSecret` |
| `cmd/api` exits 1: `KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)` | `KAFKA_BROKERS` unset, blank or only commas | env of the pod | Set a comma-separated `host:port` list (`kafka.brokers` in the chart) |
| `cmd/api` exits 1: `<TOPIC>_CONSUMER_GROUP is required: the consumer of that topic always runs on real Kafka` | One of the nine consumer group variables is empty. The first missing one is reported | env of the pod | Set all nine to stable, service-prefixed names |
| `cmd/api` exits 1: `EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka` or `EVENT_PUBLISHER: unknown value "x" (want kafka)` | A deployment still sets a retired publisher mode | env of the pod | Unset it or set `kafka` |
| `cmd/a2a` exits 1: `invalid configuration: ...` naming `DATABASE_URL` and/or `A2A_AGENT_URL` (both are reported together) | Missing variable, or `A2A_AGENT_URL` is relative, schemeless or not `http(s)` | The error text has the offending value | Set an absolute URL such as `http://<release>-a2a.<ns>.svc.cluster.local:8093/`; the chart defaults it when `a2a.agentUrl` is empty |
| `cmd/mcp` exits 1: `DATABASE_URL is required: this context runs on real Postgres` | Same | env of the pod | Set `DATABASE_URL` |
| Exits 1 after about 15 s: `run migrations (after 5 attempts): ...` or `ping postgres (after 5 attempts): ...` | Postgres unreachable, wrong DSN, or the migration itself fails | WARN `retrying` lines carry `op` and `err`; connect with `psql` from the same network | Fix the DSN or network. The retry exists for the ~10 s first-dial reset of an Istio-injected pod; it is not a license to boot without a database |
| Migration fails and every later boot fails the same way | A partial migration left `schema_migrations.dirty = true`; golang-migrate refuses to continue | `SELECT version, dirty FROM schema_migrations` | Repair the schema by hand, then force the version with the golang-migrate tooling. Take a backup first |
| Migrations hang or fail through PgBouncer | `DATABASE_URL` is a transaction-pooler address; the advisory lock needs a session | Is the DSN a pooler? | Set `MIGRATIONS_DATABASE_URL` to a direct connection (`database.migrationsExistingSecretKey` in the chart) |
| A tuning variable seems ignored; WARN `invalid OUTBOX_RELAY_INTERVAL; using the default` (same for `OUTBOX_MAX_ATTEMPTS`, `SWEEP_INTERVAL`, `ignoring invalid SHUTDOWN_DRAIN_DELAY`) | Unparseable or non-positive value | The `value` and `default` fields | Use `500ms`, `15s` or plain seconds for the relay; a non-negative integer for the cap |
| Pod restarts with `<name> consumer stopped unexpectedly: exiting so the orchestrator restarts the service` (also `queue-sweep`, `outbox-relay`) | A background worker returned on its own: a consumer's reader failed for good (for example 10 consecutive stalled fetches) or the relay/sweep stopped | ERROR `consumer stopped` or `outbox relay stopped` just before | Fix the dependency (usually Kafka); the restart is deliberate |

## Probes and traffic

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| `/readyz` is 503 `{"status":"not_ready"}` | The process is shutting down; the drain gate flips only on SIGINT/SIGTERM | Is the pod terminating? | Nothing to fix; a new pod takes over |
| `/readyz` is 503 `dependency-unavailable`, detail `not ready: kafka` (or `postgres`, or both) | The broker answered no metadata request, or Postgres did not answer a ping. A missing topic never causes it | WARN `readiness: dependency is not ready` has the cause in `error` | Restore the broker or database. The process stays up and recovers by itself (verdict cached 3 s, re-checked after) |
| `/readyz` 200 but requests fail with 500 | A query failing for another reason (statement timeout, constraint, bug); readiness only proves a ping | ERROR `request failed` (the real error is only logged; the response says `an unexpected error occurred`) | Fix the cause shown in the log |
| Statements fail with `canceling statement due to statement timeout` | Every connection has `statement_timeout = 5s` (compiled in) | The failing request's logs; slow queries on listing | Investigate the query |
| `cmd/mcp` / `cmd/a2a` pods Ready but calls fail | They are read-only and dial Postgres only; a bad `DATABASE_URL` shows at boot. At runtime a database outage makes tool calls return `internal-error` | `mcp tool failed with an unexpected error` / `a2a skill failed with an unexpected error` | Restore Postgres. These binaries have no `/readyz`, so the probe stays green |
| Agent Card advertises an unreachable URL | `A2A_AGENT_URL` is the in-cluster DNS but the client is outside the cluster | `GET /.well-known/agent-card.json` and read the advertised interface URL | Set `a2a.agentUrl` to the Kong URL (`/api/a2a/order-sourcing`) |

## Kafka and the outbox

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| Events never reach Kafka; `outbox_events.published_at` stays NULL | The broker is unreachable (the relay is always on; its writer allows topic auto-creation, so a missing output topic is created on the first write) | ERROR `outbox relay pass failed` with the cause; `last_error` and `attempts` on the head row; `/readyz` | Restore the broker. Rows drain in id order |
| ERROR `outbox relay: row reached the attempt cap and is excluded from future claims` | A poison row, or an outage longer than `OUTBOX_MAX_ATTEMPTS` passes (default 20 at 1 s is about 20 s of broker outage) | `SELECT id, topic, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL AND attempts >= 20` | Fix the cause, then `UPDATE outbox_events SET attempts = 0 WHERE id = <id>` |
| A consumer is not consuming; WARN lines about the broker connection, no `consumer running` for a topic | The consumed topic does not exist yet (its producer has not written; this service never creates a consumed topic), the group coordinator is unreachable, or a consumer group variable is wrong | `consumer running` lines list `topic`, `group_id`, `brokers`; `/readyz` | Fix the broker, or wait for the producer's first event. Retries at boot are expected until every sibling has produced once |
| Consumer lag grows on one topic | One message is failing transiently and blocks its partition until its 5 attempts end (backoff 200 ms to 5 s) | Repeated `handling failed; retrying the same message` with the same `partition` and `offset`; database health | Fix the dependency (usually Postgres). After 5 attempts the message goes to the DLQ and the consumer moves on |
| Messages appear on `<topic>.dlq` | A payload that can never be applied (undecodable `data`, validation error such as a missing id or bad tier) or a transient failure that outlasted 5 attempts | `x-dlq-error` header; ERROR `dead-lettering` lines | Fix the producer or cause, then re-publish the value to the source topic ([Runbook](/docs/operations/runbook#dead-letters)). A message that is not a CloudEvent or has an unhandled type is skipped with a WARN (or silently), not dead-lettered |
| An event seems ignored | It was a duplicate (`event_id` already in `processed_events`), stale (older revision or instant), or the aggregate had moved on (WARN `skipping an event the aggregate can no longer take`) | Logs with `event_id` and `type`; `SELECT * FROM processed_events WHERE event_id = '<id>'` | Expected idempotency. Delete the `processed_events` row only if you deliberately want to re-apply the event |
| A node never receives an assignment; the group is `UNSOURCEABLE` at once | The local read models cannot satisfy the search. A node is eligible only if it is a fulfilment-centre node run by a vendor, `ACTIVE`, with a declared operational profile (`internal/domain/network/network.go`); it must also have a non-suppressed listing with enough available quantity for every line, a scheduled, uncancelled departure with remaining capacity and a cutoff after the handling time, a path to a usable wave of the destination's service area that ends by `deliverByLatest`; a `SAME_DAY` group additionally needs a `SUB_SAME_DAY` or `MICRO` node | `GET /assignments/{id}` shows `state`, `excludedNodes`; `SELECT count(*) FROM rm_nodes`, `rm_listings`, `rm_departures`, `rm_waves`, `rm_service_areas` | Feed the read models: the sibling topics must have been consumed. A revised promise re-searches an `UNSOURCEABLE` group |
| Assignments pile up in `QUEUED` past their `releaseAt` | The sweep is not running or failing | `queue sweep running` at boot; ERROR `queue sweep pass failed`; `SELECT count(*) FROM assignments WHERE state = 'QUEUED' AND release_at < now()` | Check the sweep error (usually Postgres); the process exits if the sweep stops |
| Released assignment never locks | The vendor has not sent `PurchaseOrderAccepted` (or the event is dead-lettered) | `releases` of the assignment (`outcome PENDING`); the vendor-connectivity topic and its `.dlq` | Not a fault of this service; see [Integration](/docs/ecosystem/integration) |

## REST problem types

Every error is `application/problem+json` with `type = https://errors.order-sourcing.retail-network.example/<slug>`, `title`,
`status`, `detail` and `instance` (the escaped request path). The catalogue is `internal/adapters/inbound/http/errors.go`.

| Status | Slug | When | What to do |
| --- | --- | --- | --- |
| 400 | `malformed-request` | Body is not valid JSON for the operation (unknown field, wrong type, trailing data, body over 64 KiB) or the request carries a NUL character in the query string or body | Fix the body against `apis/openapi.yaml` |
| 400 | `idempotency-key-required` | `POST /assignments/{id}/reoptimisations` without an `Idempotency-Key` header | Send a unique key per logical request |
| 400 | `idempotency-key-invalid` | The `Idempotency-Key` is not 1 to 128 printable ASCII characters | Send a UUID |
| 400 | `invalid-limit` / `invalid-cursor` / `unknown-query-parameter` | `GET /assignments` with a bad `limit`, an undecodable `cursor`, or a parameter it does not declare (`after`, `offset`, `nextToken`) | Use `limit` and the `nextCursor` of the previous page |
| 409 | `idempotency-key-reused` | The `Idempotency-Key` was already used with a different method, path or body | A new logical request needs a new key; do not retry blindly |
| 404 | `not-found` | Unknown assignment id | Normal for a wrong id |
| 404 | `resource-not-found` | A path that names no resource, or a NUL in the path | Fix the path |
| 405 | `method-not-allowed` | Method not allowed on the path | Use the documented method |
| 409 | `assignment-locked` | Re-optimise on a `LOCKED`, `SHIPPED` or `DELIVERED` assignment (the node never changes after the lock) | None; read the assignment |
| 409 | `release-in-flight` | Re-optimise while a release is `PENDING` (state `RELEASED`) | Wait for the node's answer |
| 409 | `node-excluded` | A candidate on a node that refused this promise revision | Internal guard; the search never proposes an excluded node |
| 409 | `stale-assignment-version` | `assignmentVersion` in the body differs from the current version | `GET` the assignment and retry with its version, or omit the field |
| 409 | `concurrent-update` | Two writers raced on one aggregate; the loser's version guard failed (the use case already retries once) | Retry |
| 409 | `illegal-transition` | Re-optimise on `UNSOURCEABLE` or `CANCELLED` | None |
| 422 | `validation-failed` | `limit` not 1 to 500, unknown `state`, `assignmentVersion` negative, or an `Idempotency-Key` over 128 characters | Use the `detail` text |
| 422 | `candidate-breaks-promise` | The search result's ETA is after `deliverByLatest` | Internal guard |
| 500 | `internal-error` | Unmapped error or handler panic; `detail` is always `an unexpected error occurred` | Read the `request failed` log line |
| 503 | `dependency-unavailable` | `GET /readyz` only | See the `/readyz` row above |

### Idempotency in practice

- Only `POST /assignments/{id}/reoptimisations` is wrapped; `GET` never needs a key.
- The key is scoped to the method and path. A key with a stored answer replays that answer byte for byte, whatever its status (2xx
  and 4xx are stored), with the header `Idempotency-Replayed: true`. The request body is not compared, so reusing a key with a
  different `assignmentVersion` returns the first answer.
- A 5xx or a panic rolls everything back and is never stored, so the same key can retry real work.
- Two concurrent requests with one new key are serialised by a transaction-scoped advisory lock; the second replays the first.
- An answer older than 24 hours is not replayed; the next use overwrites the row. Nothing deletes old rows.
