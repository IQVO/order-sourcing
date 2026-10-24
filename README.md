# order-sourcing

Retail network, bounded context **Order Sourcing** (Core, commerce tier): decides, as late as the promise allows, which node
and path will fulfil each shipment group.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

The design is proposed in [retail-network docs](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing); `model/model.yaml` there is the single source of truth.
This repository is a service of the retail network, not of the warehouse-systems fleet; the fleet reaches the network only through the
`vendor-connectivity` Vendor API.

## What it does

- **Queues** one `FulfillmentAssignment` per shipment group of a placed order and keeps it in a virtual queue, re-optimising the candidate
  (node, path, cost) against current capacity until the release point. A group nobody can serve is `UNSOURCEABLE` at once.
- **Releases** the group to one node at a time, **reassigns** on rejection or expiry (the refusing node is excluded for that promise revision) and
  **locks** the assignment when the node commits; the node never changes after the lock.
- **Follows** the group through `SHIPPED` and `DELIVERED`, decides cancel requests (`FulfillmentCancelled` before the lock,
  `FulfillmentCancelRefused` after it) and applies revised promises in every non-terminal state.
- Everything it knows about the network (nodes, lanes, departures, waves, service areas, listings, scorecards, book pressure) is a local read model
  fed by sibling events; it calls no other service.

Aggregates, invariants and every assumption the model left open: [docs/adr/0002](docs/adr/0002-aggregates-invariants-and-assumptions.md).
Consumed contracts and read models: [docs/adr/0003](docs/adr/0003-consumed-contracts-and-local-read-models.md).

## REST API

Contract: [`apis/openapi.yaml`](apis/openapi.yaml). No authentication. Errors are RFC 9457 `application/problem+json` (`type` is a slug such as
`not-found`, `illegal-transition`, `concurrent-update`, `validation-failed`, `assignment-locked`, `release-in-flight`, `stale-assignment-version`).
Every state-changing request needs an `Idempotency-Key` header: a repeat with the same request replays the first response for 24 hours.

| Method and path | Operation |
| --- | --- |
| `GET /assignments?state=&nodeId=&orderId=` | list assignments |
| `GET /assignments/{assignmentId}` | read one assignment |
| `POST /assignments/{assignmentId}:reoptimise` | re-run the candidate search of a queued assignment |
| `GET /healthz`, `GET /readyz` | liveness, readiness |

## Events

Published (contract [`apis/asyncapi.yaml`](apis/asyncapi.yaml), catalogue in [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md)),
CloudEvents 1.0 structured JSON, type `com.retailnetwork.commerce.order-sourcing.assignment.<Event>`, `subject` and Kafka key = `assignmentId`:

- `retailnetwork.order-sourcing.events`: `FulfillmentReleasedToNode`, `FulfillmentReassigned`, `FulfillmentLocked`, `FulfillmentShipped`,
  `FulfillmentUnsourceable`, `FulfillmentCancelled`, `FulfillmentCancelRefused`, `FulfillmentConstraintRevised`.
- `retailnetwork.order-sourcing.analytics`: `FulfillmentQueued`, `FulfillmentDelivered`.

Consumed (32 types from nine topics): `customer-ordering` (placed, cancelled), `delivery-promise` (`PromiseRevised`, `PromiseBookExhausted`),
`last-mile-delivery` (wave scheduled, capacity changed, cancelled), `linehaul-transportation` (departure scheduled, capacity changed, cancelled;
`BookingMoved`, `BookingFailed`), `node-performance` (`NodeScorecardUpdated`), `node-topology` (node, lane, service-area and region events),
`offer-availability` (listing and catalog-item events), `shipping-and-tracking` (`PackageDelivered`), `vendor-connectivity` (purchase-order outcomes,
`VendorShipmentConfirmed`). Delivery is at-least-once with a stable event id and an inbox on the consuming side ([ADR 0005](docs/adr/0005-outbox-relay-attempt-cap-sweep-and-shutdown.md)).

## MCP server (read-only)

`cmd/mcp` is a second, independent deployable (ADR 0006): the same two read use
cases exposed to AI agents over the official MCP Go SDK, Streamable HTTP on
`:8090` at `/mcp`, with an open `/healthz` and no auth (fleet decision). Two
tools, one per read: `list_assignments(state?, nodeId?, orderId?, limit?)` and
`get_assignment(assignmentId)`; typed errors carry the REST problem slug
(`not-found`, `validation-failed`). It reads only: no Kafka, no consumers, no
sweep, no outbox — `DATABASE_URL` (plus the optional
`MIGRATIONS_DATABASE_URL`) is its only dependency, and it runs the idempotent
boot migrations like `cmd/api`. `POST /assignments/{id}:reoptimise` stays
REST-only. In the chart it is behind `mcp.enabled` (off by default), with its
own ClusterIP Service and an optional Gateway API route
(`/api/mcp/order-sourcing` rewritten to `/mcp`).

## Web console remote

`web/` is the `order_sourcing_mfe` Module Federation remote (ADR 0007): a
small read-only UI the warehouse-console shell mounts under
`/order-sourcing/*` — the virtual queue with its state/order/node filters and
an assignment detail screen (promise, lines, candidate path, release history,
excluded nodes). The API origin comes from the shell's runtime `/config.json`
(`window.__WAREHOUSE_CONFIG__.apiOrigin` → `/api/order-sourcing`), never
from the build. Dev/preview on `:4019`; served in-cluster by its own
nginx-unprivileged pod behind `frontend.enabled` (off by default) at
`/mfes/order-sourcing/`. `@warehouse/ui-kit` is a sibling checkout, so run
npm commands from `web/` with `warehouse-ui-kit` next to this repository.

## Run it

Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS` and the nine `*_CONSUMER_GROUP` variables are mandatory (a boot error names the
missing one); the embedded migrations run at boot, the outbox relay and the queue sweep are always on.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/order_sourcing?sslmode=disable
export KAFKA_BROKERS=localhost:29092   # whatever your broker advertises
# plus one <PRODUCER>_CONSUMER_GROUP per consumed topic, see docs/runtime.md
go run ./cmd/api                       # listens on :8080 (HTTP_ADDR)
```

All settings, the probes, the shutdown order and the outbox runbook: [docs/runtime.md](docs/runtime.md). Development: `make check` (format, vet, lint, tests),
`make coverage`, `make mutation`, `make bdd`, `make integration` (Docker), `make vuln`; see [HARNESS.md](HARNESS.md).

## Deploy

`charts/order-sourcing/` is a Helm chart with one component, the `api` Deployment (REST API, nine consumers, queue sweep and outbox relay in one process), a Service,
a ConfigMap mounted with `envFrom`, a Secret (or `database.existingSecret`) for `DATABASE_URL`, a ServiceAccount, and an HPA, Ingress or Gateway API HTTPRoute that are off by default.
It refuses to render without `database.url`/`database.existingSecret`, `kafka.brokers` and all nine `config.*ConsumerGroup` values, and it keeps `terminationGracePeriodSeconds`
above the binary's shutdown bounds. See [docs/runtime.md](docs/runtime.md#deployment-helm-chart).

```sh
helm template order-sourcing charts/order-sourcing -f charts/order-sourcing/ci/default-values.yaml   # dummy values, nothing is dialled
bash charts/order-sourcing/tests/render_test.sh && python3 charts/order-sourcing/tests/test_service_selectors.py
```

See `docs/adr/` for decisions and `docs/before-first-release.md` for the packaging checklist.
