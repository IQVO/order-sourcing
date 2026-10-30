# Order Sourcing

Retail network, bounded context **Order Sourcing** (Core Subdomain, commerce
tier): decides, as late as the promise allows, which node and path will
fulfil each shipment group. It owns the `FulfillmentAssignment` aggregate and
the virtual queue behind it — not the promise (`delivery-promise`), not the
node's execution (the WMS via `vendor-connectivity`), not transport
(`linehaul-transportation`, `last-mile-delivery`): everything it knows about
the network (nodes, lanes, departures, waves, service areas, listings,
scorecards, book pressure) is a local read model fed by sibling events, and
it calls no other service. Its integration events drive
`vendor-connectivity`'s purchase orders and `delivery-promise`'s judgements.
This repository is a service of the retail network, not of the
warehouse-systems fleet; the fleet reaches the network only through the
`vendor-connectivity` Vendor API.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

## Documentation

The context is documented on the fleet-wide warehouse-docs site, in the
retail-network section: **<https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing>**
(the model under `network/model/model.yaml` in IQVO/warehouse-docs is the
single source of truth). Fleet-wide network pages:
[strategic design](https://iqvo.github.io/warehouse-docs/network/strategic-design/subdomain-classification),
[the CloudEvents event standard](https://iqvo.github.io/warehouse-docs/network/strategic-design/event-standard-cloudevents),
the [order-assignment model](https://iqvo.github.io/warehouse-docs/network/network-model/order-assignment-model),
and the [WMS/WES integration seam](https://iqvo.github.io/warehouse-docs/network/integration/wms-wes-integration).
This repository's own references: [`docs/runtime.md`](docs/runtime.md) (all
settings, the probes, the shutdown order and the outbox runbook) and the
packaging checklist [`docs/before-first-release.md`](docs/before-first-release.md).

Page map (on the warehouse-docs site, under `network/contexts/order-sourcing/`):

| Area | Pages |
|---|---|
| Business context | [Business context](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/business-context), [Use cases](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/use-cases) |
| Language & model | [Ubiquitous language](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/ubiquitous-language), [Domain events](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/domain-events), [Domain message flow](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/domain-message-flow), [Class diagram](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/class-diagram), [Sequence diagrams](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/sequence-diagrams), [Entity-relationship](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/entity-relationship) |
| DDD canvases | [Core domain chart](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/core-domain-chart), [Bounded context canvas](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/bounded-context-canvas), [Aggregate design canvas](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/aggregate-design-canvas), [EventStorming](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/eventstorming), [Context map](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing/context-map) |

## What it does

- **Queues** one `FulfillmentAssignment` per shipment group of a placed order and keeps it in a virtual queue, re-optimising the candidate
  (node, path, cost) against current capacity until the release point. A group nobody can serve is `UNSOURCEABLE` at once.
- **Releases** the group to one node at a time, **reassigns** on rejection or expiry (the refusing node is excluded for that promise revision) and
  **locks** the assignment when the node commits; the node never changes after the lock.
- **Follows** the group through `SHIPPED` and `DELIVERED`, decides cancel requests (`FulfillmentCancelled` before the lock,
  `FulfillmentCancelRefused` after it) and applies revised promises in every non-terminal state.

Aggregates, invariants and every assumption the model left open: [docs/adr/0002](docs/adr/0002-aggregates-invariants-and-assumptions.md).
Consumed contracts and read models: [docs/adr/0003](docs/adr/0003-consumed-contracts-and-local-read-models.md).

## Architecture

Hexagonal / ports & adapters; dependencies point inward only:

```
cmd/api/                      composition root: REST adapter, nine Kafka consumers, the queue sweep and the outbox relay in ONE process
cmd/mcp/                      read-only MCP server (Streamable HTTP, ADR 0006)
cmd/a2a/                      read-only A2A agent (JSON-RPC over HTTP, ADR 0009)
internal/
  domain/                     pure business logic — assignment, sourcing (the candidate search), network (the read-side graph), shared
  application/                ports, repository, usecases, views, outbox
  adapters/
    inbound/http/             chi handlers, problem+json mapping, readiness
    inbound/kafka/            the nine consumers feeding the local read models
    inbound/mcp/              MCP tools (read-only)
    inbound/a2a/              A2A skills (read-only)
    inbound/requestlog/       request logging middleware
    kafka/cloudevents/        CloudEvents 1.0 codec
    outbound/postgres/        repos, embedded migrations, pgtx UnitOfWork
    outbound/outbox/          transactional outbox + relay with attempt cap
    outbound/kafka/           integration and analytics publishers
    outbound/memory/          test doubles only, never linked into a binary
    outbound/clock/, ids/, repocontract/, telemetry/
  architecture/               the fitness tests
  bootretry/                  bounded boot retry
web/                          the order_sourcing_mfe Module Federation remote
features/                     godog BDD
```

The layering is enforced as an executable test, not a convention:
`internal/architecture` (`make arch-test`) fails the build if the domain ever
imports an outer layer or an adapter reaches sideways into its sibling.

## Domain events

Contract: [`apis/asyncapi.yaml`](apis/asyncapi.yaml), catalogue in [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md).
CloudEvents 1.0 structured JSON, type `com.retailnetwork.commerce.order-sourcing.assignment.<Event>`, `subject` and Kafka key = `assignmentId`:

| Topic | Events |
|---|---|
| `retailnetwork.order-sourcing.events` (integration) | `FulfillmentReleasedToNode`, `FulfillmentReassigned`, `FulfillmentLocked`, `FulfillmentShipped`, `FulfillmentUnsourceable`, `FulfillmentCancelled`, `FulfillmentCancelRefused`, `FulfillmentConstraintRevised` |
| `retailnetwork.order-sourcing.analytics` (analytics only) | `FulfillmentQueued`, `FulfillmentDelivered` |

**Consumed** — 32 event types from nine producer topics, each under its own
stable consumer group; delivery is at-least-once with a stable event id and
an inbox on the consuming side ([ADR 0005](docs/adr/0005-outbox-relay-attempt-cap-sweep-and-shutdown.md)):

| Topic | Events |
|---|---|
| `retailnetwork.customer-ordering.events` | `CustomerOrderPlaced`, `CustomerOrderCancelled` |
| `retailnetwork.delivery-promise.events` | `PromiseRevised`, `PromiseBookExhausted` |
| `retailnetwork.last-mile-delivery.events` | `DeliveryWaveScheduled`, `DeliveryWaveCapacityChanged`, `DeliveryWaveCancelled` |
| `retailnetwork.linehaul-transportation.events` | `DepartureScheduled`, `DepartureCapacityChanged`, `DepartureCancelled`, `BookingMoved`, `BookingFailed` |
| `retailnetwork.node-performance.events` | `NodeScorecardUpdated` |
| `retailnetwork.node-topology.events` | `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired`, `LaneOpened`, `LaneClosed`, `ServiceAreaAssigned`, `RegionDefined` |
| `retailnetwork.offer-availability.events` | `ListingCreated`, `ListingAvailabilityChanged`, `ListingSuppressed`, `ListingRestored`, `CatalogItemDiscontinued` |
| `retailnetwork.shipping-and-tracking.events` | `PackageDelivered` |
| `retailnetwork.vendor-connectivity.events` | `PurchaseOrderAccepted`, `PurchaseOrderRejected`, `PurchaseOrderExpired`, `VendorShipmentConfirmed` |

## Running it

Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS` and the nine `*_CONSUMER_GROUP` variables are mandatory (a boot error names the
missing one); the embedded migrations run at boot, the outbox relay and the queue sweep are always on.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/order_sourcing?sslmode=disable
export KAFKA_BROKERS=localhost:29092   # whatever your broker advertises
# plus one <PRODUCER>_CONSUMER_GROUP per consumed topic, see docs/runtime.md
go run ./cmd/api                       # listens on :8080 (HTTP_ADDR)
```

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | **required** | Postgres DSN of the service's pool |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations (golang-migrate holds a session advisory lock, which breaks behind a transaction-pooling proxy) |
| `KAFKA_BROKERS` | **required** | Comma-separated `host:port` list (relay, consumers and `/readyz`) |
| `CUSTOMER_ORDERING_` / `DELIVERY_PROMISE_` / `LAST_MILE_DELIVERY_` / `LINEHAUL_TRANSPORTATION_` / `NODE_PERFORMANCE_` / `NODE_TOPOLOGY_` / `OFFER_AVAILABILITY_` / `SHIPPING_AND_TRACKING_` / `VENDOR_CONNECTIVITY_CONSUMER_GROUP` | **required** | One stable, service-prefixed consumer group id per consumed topic |
| `HTTP_ADDR` | `:8080` | Listen address |
| `SWEEP_INTERVAL` | `15s` | Period of the virtual-queue sweep: releases every assignment whose `releaseAt` has passed and re-optimises the other queued ones (ADR 0002 A18) |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Poll interval of the relay (Go duration or seconds) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row; `0` = unlimited (ADR 0005) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait between flipping `/readyz` to 503 and closing the listener; `0` disables |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `EVENT_PUBLISHER` | `kafka` | Only `kafka` (or unset) is accepted; any other value is a boot error |
| `OTEL_SERVICE_NAME` | `order-sourcing` | `service.name` of spans and metrics (ADR 0008) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Collector OTLP/gRPC; export never blocks |
| `SERVICE_VERSION` / `ENVIRONMENT` | `dev` / `local` | `service.version` / `deployment.environment.name` |

`cmd/mcp` adds `MCP_ADDR` (`:8090`); `cmd/a2a` adds `A2A_ADDR` (`:8093`) and
the mandatory `A2A_AGENT_URL`. Both read only `DATABASE_URL` (plus the
optional `MIGRATIONS_DATABASE_URL`) and the telemetry/log variables — no
Kafka, no consumers, no sweep, no outbox.

## Observability

Logs, metrics and traces follow the fleet standard ([ADR 0008](docs/adr/0008-fleet-observability-standard.md)): JSON logs on stdout carry `trace_id`/`span_id`
and a `request_id`; `cmd/api`, `cmd/mcp` and `cmd/a2a` export OTLP/gRPC to the Collector (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `SERVICE_VERSION`, `ENVIRONMENT`;
export never blocks, so no Collector costs telemetry, nothing else) with HTTP request duration, Go runtime metrics and the business counter
`sourcing.groups.sourced` (`outcome`, `reason`); the trace continues across Kafka in the `traceparent` header of each published event and is picked up by the
consumers. The chart's `otel.*` values render the env.

## API

Contract: [`apis/openapi.yaml`](apis/openapi.yaml). No authentication. Errors are RFC 9457 `application/problem+json` (`type` is a slug such as
`not-found`, `illegal-transition`, `concurrent-update`, `validation-failed`, `assignment-locked`, `release-in-flight`, `stale-assignment-version`).
Every state-changing request needs an `Idempotency-Key` header: a repeat with the same request replays the first response for 24 hours.

| Method and path | Operation |
| --- | --- |
| `GET /assignments?state=&nodeId=&orderId=` | list assignments |
| `GET /assignments/{assignmentId}` | read one assignment |
| `POST /assignments/{assignmentId}:reoptimise` | re-run the candidate search of a queued assignment |
| `GET /healthz`, `GET /readyz` | liveness, readiness |

### MCP server (read-only)

`cmd/mcp` is a second, independent deployable (ADR 0006): the same two read use
cases exposed to AI agents over the official MCP Go SDK, Streamable HTTP on
`:8090` at `/mcp`, with an open `/healthz` and no auth (fleet decision). Two
tools, one per read: `list_assignments(state?, nodeId?, orderId?, limit?)` and
`get_assignment(assignmentId)`; typed errors carry the REST problem slug
(`not-found`, `validation-failed`). It reads only: no Kafka, no consumers, no
sweep, no outbox — `DATABASE_URL` (plus the optional
`MIGRATIONS_DATABASE_URL`) is its only dependency, and it runs the idempotent
boot migrations like `cmd/api`. `POST /assignments/{id}:reoptimise` stays
REST-only.

### A2A agent (read-only)

`cmd/a2a` is a third, independent deployable (ADR 0009): the same two read use cases offered to other agents as an Agent2Agent (A2A) agent over
the official A2A Go SDK (`a2aproject/a2a-go/v2`), JSON-RPC on `:8093` at `/` and `/a2a`, the Agent Card at `/.well-known/agent-card.json`, an
open `/healthz` and no auth (fleet decision). A2A carries **delegation** of a goal, never data sync: facts still leave only as CloudEvents.
The MCP charter is read-only, so the agent is too (the write gate): two goal-shaped skills, `find_assignments` (`state?`, `orderId?`, `nodeId?`,
`limit?`) and `assignment_detail` (`assignmentId`), sent as `{"skill":"<id>","arguments":{...}}` in a data part. Unknown arguments are
rejected; rejections are answered in-band as RFC 7807-shaped problems with the REST statuses (`404`, `422`, `400`, `500`), never JSON-RPC
errors. It needs `DATABASE_URL` and `A2A_AGENT_URL` (the absolute URL the card advertises; a boot error names a missing one) and nothing else.

## Helm chart

`charts/order-sourcing/` is a Helm chart with one component, the `api` Deployment (REST API, nine consumers, queue sweep and outbox relay in one process), a Service,
a ConfigMap mounted with `envFrom`, a Secret (or `database.existingSecret`) for `DATABASE_URL`, a ServiceAccount, and an HPA, Ingress or Gateway API HTTPRoute that are off by default.
It refuses to render without `database.url`/`database.existingSecret`, `kafka.brokers` and all nine `config.*ConsumerGroup` values, and it keeps `terminationGracePeriodSeconds`
above the binary's shutdown bounds. See [docs/runtime.md](docs/runtime.md#deployment-helm-chart).

```sh
helm template order-sourcing charts/order-sourcing -f charts/order-sourcing/ci/default-values.yaml   # dummy values, nothing is dialled
bash charts/order-sourcing/tests/render_test.sh && python3 charts/order-sourcing/tests/test_service_selectors.py
```

The CI `helm-lint` job runs `ct lint` (pull requests); `chart-tests` runs the
render and selector tests.

### Running the MCP server in Kubernetes

In the chart the MCP server is behind `mcp.enabled` (off by default), with its
own ClusterIP Service and an optional Gateway API route
(`/api/mcp/order-sourcing` rewritten to `/mcp`); the A2A agent is behind
`a2a.enabled` (off by default, route `/api/a2a/order-sourcing` rewritten to
`/`). Both are read-only and unauthenticated by fleet decision — the
in-cluster ClusterIP boundary is the access control.

## Operator micro-frontend (web/)

`web/` is the `order_sourcing_mfe` Module Federation remote (ADR 0007): a
small read-only UI the warehouse-console shell mounts under
`/order-sourcing/*` — the virtual queue with its state/order/node filters and
an assignment detail screen (promise, lines, candidate path, release history,
excluded nodes). The API origin comes from the shell's runtime `/config.json`
(`window.__WAREHOUSE_CONFIG__.apiOrigin` → `/api/order-sourcing`), never
from the build. Dev/preview on `:4019` (`npm run dev` in `web/`); served in-cluster by its own
nginx-unprivileged pod behind `frontend.enabled` (off by default) at
`/mfes/order-sourcing/`. `@warehouse/ui-kit` is a sibling checkout, so run
npm commands from `web/` with `warehouse-ui-kit` next to this repository.

## Local development / quality gate

```
make check          # format, vet, build, lint, unit + handler + BDD tests (the pre-push gate)
make check-fast     # fmt-check, vet, arch-test + changed-package tests (Stop hook)
make coverage       # >= 90% over internal/domain and internal/application
make mutation       # gremlins, one run per internal/domain package
make arch-test      # fitness tests
make integration    # testcontainers Postgres and Kafka (needs Docker)
make vuln           # govulncheck
```

lefthook (`lefthook install` once per clone) runs `fmt-check`, `vet`, `lint`
on pre-commit and `make check` on pre-push. The harness itself:
[Makefile](Makefile), [HARNESS.md](HARNESS.md), `make guide-lint`,
`make harness-test`.

## Tests

- unit + handler tests: `make test` (`go test ./... -race`, CI job `test`,
  with the 90 % coverage gate in the same job)
- BDD acceptance: godog/Gherkin in `features/` (`make bdd`, CI job `bdd`)
- integration: testcontainers Postgres + Kafka (`make integration`, CI job
  `integration`; needs only Docker)
- mutation: gremlins per `internal/domain` package (CI job `mutation-fast`;
  the exhaustive `mutation` job is scheduled)
- architecture fitness: `internal/architecture` (CI job `arch-test`)
- API contract lint (CI job `api-lint`), spec/model drift (`drift`,
  scheduled), `lint`, `guide-lint`, `complexity`, `vuln`, `web` (the
  vitest/oxlint/tsc suite of `web/`), `helm-lint` + `chart-tests` (chart),
  `docker-publish` + `release` (main only)

## Architecture Decision Records

Index: [`docs/adr/index.md`](docs/adr/index.md) — 9 ADRs (0001–0009): the
bounded context (0001), aggregates, invariants and assumptions (0002),
consumed contracts and local read models (0003), the CloudEvents catalogue
(0004), outbox relay, attempt cap, sweep and shutdown (0005), the MCP server
(0006), the console remote (0007), the fleet observability standard (0008),
the A2A inbound adapter (0009).

## License

[MIT](LICENSE).
