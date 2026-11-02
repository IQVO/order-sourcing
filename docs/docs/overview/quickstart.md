---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
description: "Build, test and run Order Sourcing against Postgres and Kafka, feed it one event and make the first REST, MCP and A2A calls."
---

# Quickstart

Build the service, run its tests, start it on Postgres and Kafka and make the first calls. **Nothing on this page was captured
from a run**: the commands and the answer shapes are written from the code (`cmd/*/main.go`, `internal/adapters/inbound`) and
from `apis/openapi.yaml`. Where a number or a body is shown it comes from a named source file, not from output.

## Prerequisites

| Tool | Version | Needed for |
| --- | --- | --- |
| Go | `1.27.3` (`go.mod`) | everything |
| `make` | any | the targets below |
| Docker | any recent | the Postgres container below and `make integration` (testcontainers boots its own Postgres and Kafka) |
| golangci-lint | `v2.14.0` (`GOLANGCI_VERSION` in the `Makefile`, pinned in CI) | `make lint`, `make check` |
| gremlins | `v0.6.0` (`GREMLINS_VERSION`) | `make mutation`, `make mutation-full` |
| govulncheck | latest | `make vuln` |
| Spectral (`@stoplight/spectral-cli`) | latest | linting the two specs ([Testing](/docs/development/testing#ci-jobs)) |
| Node 20 | | building this documentation site (`docs/`) |
| lefthook | optional | `lefthook install` wires `fmt-check`, `vet`, `lint` at pre-commit and `make check` at pre-push |

The fleet runs **one** Kafka broker, reachable at `localhost:9092` from the kind cluster; the docker-compose Kafka is retired.
This repository ships no docker-compose file. `cmd/api` needs Postgres **and** Kafka to boot and to be ready: there is no mode
without them. `cmd/mcp` and `cmd/a2a` need Postgres only.

## Build and test

```bash
make help          # list the targets
make build         # go build ./...
make test          # go test ./... -race
make arch-test     # go test ./internal/architecture/... -v
make bdd           # go test ./... -run TestFeatures -v   (45 scenarios in 6 feature files)
make coverage      # tests with -race plus the 90% gate over internal/domain and internal/application
make check         # fmt-check vet build lint test
make check-all     # check + coverage + arch-test + bdd
make integration   # go test -tags=integration ./... -race -count=1 (needs Docker)
```

## Boot refusals

Start the binary with nothing configured. `cmd/api` validates the mandatory environment before it opens a connection or binds the
port, and names the first variable that is missing (`loadRuntime` in `cmd/api/main.go`):

```bash
go build -o order-sourcing ./cmd/api
HTTP_ADDR=127.0.0.1:18080 ./order-sourcing
# exits 1 with the ERROR "service exited with error" and error text starting
# "DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode"
```

The next refusals, in order, are `KAFKA_BROKERS is required: ...`, then `EVENT_PUBLISHER=log is not supported` (only when that
value is set), then `<TOPIC>_CONSUMER_GROUP is required: ...` for the first of the nine groups that is empty.

## Run on Postgres and Kafka

Start a throwaway database and use your Kafka for the broker. The service never creates topics through an admin API: the broker
auto-creates `retailnetwork.order-sourcing.events` and `retailnetwork.order-sourcing.analytics` on the relay's first write, and
each consumed topic exists once its producer has published (a broker with auto-creation switched off needs them created by hand).

```bash
docker run -d --rm --name os-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=os \
  -p 127.0.0.1:55432:5432 postgres:16-alpine

export DATABASE_URL='postgres://postgres:pw@127.0.0.1:55432/os?sslmode=disable'
export KAFKA_BROKERS=localhost:9092
export CUSTOMER_ORDERING_CONSUMER_GROUP=order-sourcing-customer-ordering-dev
export DELIVERY_PROMISE_CONSUMER_GROUP=order-sourcing-delivery-promise-dev
export LAST_MILE_DELIVERY_CONSUMER_GROUP=order-sourcing-last-mile-delivery-dev
export LINEHAUL_TRANSPORTATION_CONSUMER_GROUP=order-sourcing-linehaul-transportation-dev
export NODE_PERFORMANCE_CONSUMER_GROUP=order-sourcing-node-performance-dev
export NODE_TOPOLOGY_CONSUMER_GROUP=order-sourcing-node-topology-dev
export OFFER_AVAILABILITY_CONSUMER_GROUP=order-sourcing-offer-availability-dev
export SHIPPING_AND_TRACKING_CONSUMER_GROUP=order-sourcing-shipping-and-tracking-dev
export VENDOR_CONNECTIVITY_CONSUMER_GROUP=order-sourcing-vendor-connectivity-dev
export HTTP_ADDR=127.0.0.1:18080
./order-sourcing
```

The group ids are yours to choose (the code has none); a fresh group replays its topic from the first offset, so the read models
converge by themselves. Migrations run at boot. The startup log is JSON on stdout; the messages are, in order, `telemetry
configured`, nine `consumer running` lines (`consumer`, `topic`, `group_id`, `brokers`), `outbox relay running`, `queue sweep
running` and `http server listening`, with retries from `internal/bootretry` before them if Postgres is slow. With no Collector on
`localhost:4317` nothing else changes: export never blocks. The [Observability](/docs/operations/observability) page lists every
message.

`/readyz` is ready only while Postgres answers and a Kafka broker answers a metadata request; topics are not checked, so it is
`200` before any topic exists. If the database is up and the broker is unreachable, the process stays live and the probe answers
503 `application/problem+json` with type slug `dependency-unavailable` and detail `not ready: kafka`; `/healthz` stays 200. Stop
the service with Ctrl-C: it flips `/readyz` to 503, waits `SHUTDOWN_DRAIN_DELAY` (5 s), drains HTTP, stops the consumers, the
sweep and the relay, and closes the pool.

The read-only servers need only the database:

```bash
DATABASE_URL="$DATABASE_URL" MCP_ADDR=127.0.0.1:18090 go run ./cmd/mcp
DATABASE_URL="$DATABASE_URL" A2A_ADDR=127.0.0.1:18093 A2A_AGENT_URL=http://127.0.0.1:18093/ go run ./cmd/a2a
```

## First calls

REST, MCP and A2A are unauthenticated. Probes and the list (an empty database answers an empty array):

```bash
curl -s localhost:18080/healthz                 # {"status":"ok"}
curl -s localhost:18080/readyz                  # {"status":"ready"} once Postgres and Kafka are up
curl -s 'localhost:18080/assignments?state=QUEUED&limit=10'   # {"assignments":[]} until an order arrives
curl -si localhost:18080/assignments/fa-unknown # 404 application/problem+json, type slug not-found
```

Assignments are created only by a consumed event, never through REST. Publish a `CustomerOrderPlaced` CloudEvent to
`retailnetwork.customer-ordering.events` (structured mode, key = order id, header `content-type:
application/cloudevents+json; charset=UTF-8`). The envelope and the `data` fields below are what the handler decodes
(`internal/adapters/inbound/kafka/handlers.go`); the event `id` must be unique because the inbox keys on it:

```json
{
  "specversion": "1.0",
  "id": "evt-demo-1",
  "source": "/retailnetwork/customer-ordering",
  "type": "com.retailnetwork.commerce.customer-ordering.customerorder.CustomerOrderPlaced",
  "subject": "ord-1",
  "time": "2026-10-31T12:00:00Z",
  "datacontenttype": "application/json",
  "data": {
    "orderId": "ord-1",
    "destinationPostalPrefix": "941",
    "groups": [
      {
        "groupNo": 1,
        "promiseId": "prm-1",
        "tier": "STANDARD",
        "witnessNodeId": "node-1",
        "deliverByLatest": "2026-11-05T18:00:00Z",
        "lines": [{"lineNo": 1, "catalogItemId": "sku-1", "quantity": 2}]
      }
    ]
  }
}
```

With empty read models no node can serve the group, so `QueueAssignment` creates the assignment `UNSOURCEABLE` at once
(`internal/application/usecases/queue.go`); it appears in `GET /assignments?orderId=ord-1` and a `FulfillmentQueued` row
(analytics topic) and a `FulfillmentUnsourceable` row (events topic) are written to `outbox_events`. To see a `QUEUED` assignment,
the sibling topics (`node-topology`, `offer-availability`, `linehaul-transportation`, `last-mile-delivery`) must have delivered
a node, a listing, departures, a wave and a service area for the prefix first; the end-to-end test `cmd/api/e2e_integration_test.go`
builds exactly such a world.

```bash
docker exec os-pg psql -U postgres -d os -At \
  -c "select id,topic,event_type,subject,attempts,published_at is not null from outbox_events"
```

The relay publishes those rows within `OUTBOX_RELAY_INTERVAL` (1 s by default) and sets `published_at`. While Kafka is unreachable
`attempts` grows by one per failed pass and the row is parked at `OUTBOX_MAX_ATTEMPTS` (20); see the
[Runbook](/docs/operations/runbook#outbox-relay).

Re-optimise a `QUEUED` assignment (the only write on REST). `Idempotency-Key` is mandatory; a repeat with the same key replays
the first answer for 24 hours:

```bash
curl -s -X POST 'localhost:18080/assignments/fa-REPLACE-ME/reoptimisations' \
  -H 'Idempotency-Key: demo-key-1' -H 'Content-Type: application/json' \
  -d '{"assignmentVersion": 1}'
```

Without the header the answer is `400` with type slug `idempotency-key-required`; on a `LOCKED` assignment it is `409`
`assignment-locked`; an `assignmentVersion` that is not the current one is `409` `stale-assignment-version`. All errors are
`application/problem+json` ([Troubleshooting](/docs/operations/troubleshooting#rest-problem-types)).

### MCP

Streamable HTTP at `/mcp` (and `/`). A client such as the MCP Inspector or any MCP SDK works; raw JSON-RPC needs both accept types:

```bash
curl -s localhost:18090/healthz
curl -s -X POST localhost:18090/mcp -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

The two tools are `list_assignments` and `get_assignment` ([MCP tools](/docs/mcp/tools)).

### A2A

```bash
curl -s localhost:18093/.well-known/agent-card.json     # the Agent Card: name, skills, the interface URL from A2A_AGENT_URL
curl -s localhost:18093/healthz
```

JSON-RPC 2.0 is served on `/` and `/a2a`; the method used by the repository's own tests is `SendMessage`
(`internal/adapters/inbound/a2a/skills_test.go`) with a data part such as
`{"skill":"find_assignments","arguments":{"orderId":"ord-1"}}`. Use an A2A client (`a2aproject/a2a-go/v2` `a2aclient`) rather than
hand-written JSON-RPC; the skills are in [API reference](/docs/api-reference).

Remove the container when done: `docker rm -f os-pg`.

## Drive the domain by hand

The domain packages run without any server. Create a throwaway program **inside the module** (the packages are under `internal/`);
a directory starting with `_` is ignored by `./...`. The aggregate API is in `internal/domain/assignment` (`assignment.Queue`,
`Release`, `Lock`, `MarkShipped`, `Cancel`, ...) and the pure search in `internal/domain/sourcing` (`sourcing.Search` over a
`sourcing.World`). The BDD scenarios in `features/*.feature` are the executable examples of the same rules.

## Building these docs

```bash
cd docs
npm ci
npm run build         # also fails on broken links and anchors
npm run gen-api-docs  # regenerate docs/api-reference/rest from apis/openapi.yaml
```

The REST reference pages are generated from `apis/openapi.yaml`; run the generator after any spec change and commit the result,
because `sidebars.ts` imports the generated `sidebar.ts`.
