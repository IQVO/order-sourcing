# order-sourcing

harness-template: v4

Retail network, bounded context Order Sourcing (Core, commerce tier). Owns the FulfillmentAssignment aggregate: decides, as late as the promise allows, which node and path fulfil each shipment group (virtual queue, re-optimisation, release, reassignment, lock). Consumes nine sibling topics into local read models and calls no sibling.
Design source of truth: IQVO/retail-network-docs `model/model.yaml` (context order-sourcing) plus the orchestrator's contracts-delta. This service belongs to the retail network, not the warehouse-systems fleet.
This project is a domain-driven design study, not associated with any company: never name a real company, brand or product in code, comments, ADRs, specs, tests or fixtures (README `## Disclaimer`).

## Commands
`make check` (fmt, vet, build, lint, unit + handler + BDD; the pre-push gate), `make coverage` (>= 90% over internal/domain + internal/application), `make mutation` (gremlins per internal/domain/<pkg>, thresholds below measurement),
`make bdd`, `make arch-test`, `make integration` (testcontainers Postgres + Kafka, needs Docker; build tag `integration`), `make vuln`. Lint: `golangci-lint run --allow-parallel-runners ./...`.
Guides: `python3 scripts/harness/repo_lint.py`, `python3 scripts/harness/guide_lint.py` must stay clean. Never gate a commit on a piped command.

## Layout (hexagonal)
`internal/domain/{shared,assignment,network,sourcing}` pure Go (assignment = the aggregate, network = read-model value types, sourcing = the pure candidate search); `internal/application/{ports (interfaces only),repository (filters),views,usecases,outbox}`;
`internal/adapters/inbound/{http (chi),kafka (nine consumers, DLQ)}`; `internal/adapters/outbound/{postgres,kafka,outbox,clock,ids,memory,repocontract}`; `internal/adapters/kafka/cloudevents` (the ONLY envelope builder);
`internal/testing/pgtest` (testcontainers helper); `cmd/api` (composition root, `run(ctx)`); `features/` (godog, never a .go file at the repo root). Migrations are embedded in `internal/adapters/outbound/postgres/migrations`.

## Runtime rule
Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS` and the nine `<PRODUCER>_CONSUMER_GROUP` variables are mandatory (boot error naming the variable, raised before the listener binds; `MIGRATIONS_DATABASE_URL` is an optional direct DSN).
The outbox relay (`OUTBOX_MAX_ATTEMPTS` 20, `OUTBOX_RELAY_INTERVAL` 1s), the queue sweep (`SWEEP_INTERVAL` 15s) and the consumers are always on; the process exits non-zero when a worker ends on its own.
`/healthz` = liveness; `/readyz` = drain gate + Postgres ping + Kafka metadata request with an EMPTY non-nil topic list (fresh connection per check).
Shutdown: /readyz 503, `SHUTDOWN_DRAIN_DELAY` (5s), drain HTTP, stop consumers and sweep, stop relay, close pool. cmd/** never imports internal/adapters/outbound/memory or repocontract. No authentication on REST. See docs/runtime.md.

## REST
chi router: `GET /assignments`, `GET /assignments/{assignmentId}`, `POST /assignments/{assignmentId}:reoptimise` plus /healthz, /readyz. Every other transition is event-driven. The state-changing route takes `Idempotency-Key` (table `idempotency_keys`, key = (route, key), 24h, replays the first response, same key + other request = 422 `idempotency-key-reused`).
NUL: 404 in the path, 400 elsewhere. Errors are RFC 9457 problem+json, `type` = slug: not-found, illegal-transition, concurrent-update, validation-failed + one kebab-case slug per model Err (assignment-locked, release-in-flight, stale-assignment-version, ...).

## Events: CloudEvents 1.0 is MANDATORY
Published type `com.retailnetwork.commerce.order-sourcing.assignment.<Event>`; source `/retailnetwork/order-sourcing`; dataschema `urn:retailnetwork:order-sourcing:<events|analytics>:<Event>:v1`; subject and Kafka key = assignmentId;
eight events on `retailnetwork.order-sourcing.events`, `FulfillmentQueued` and `FulfillmentDelivered` only on `.analytics`; header `content-type: application/cloudevents+json; charset=UTF-8`.
Use internal/adapters/kafka/cloudevents only. Transactional outbox: aggregate + outbox rows in ONE Postgres transaction, id minted once at append, at-least-once delivery. No `warehouse.*` topic, ever.
Consumed: 32 types from nine topics, dispatched on the full type, tolerant decoding, inbox (`processed_events`) claimed inside the use case's transaction, DLQ `<topic>.dlq`. Read the producer's apis/asyncapi.yaml before touching a handler; report a mismatch, never adapt silently.
A new event: domain struct + apis/asyncapi.yaml + ADR 0004 `type:` line in the SAME commit + encoder payload + golden file (.claude/skills/how-to-add-an-integration-event).

## Domain
Aggregates, invariants and assumptions A1-A23 are in docs/adr/0002; consumed contracts and read models in 0003; outbox, relay, sweep and shutdown in 0005. The node never changes after LOCKED; a refusal excludes the node until the promise revision changes; an empty candidate set is UNSOURCEABLE at once.

## Workflow
GitFlow: feature/* -> PR into develop, CI green before merge, never push develop or main. lefthook: pre-commit fmt/vet/lint, pre-push `make check`. Commit and push after each step.
Open follow-up: the Helm chart (docs/before-first-release.md rows 5-9).
