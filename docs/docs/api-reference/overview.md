---
id: overview
title: API overview
sidebar_label: API overview
slug: /api-reference
description: "Order Sourcing interfaces: REST, Kafka events, MCP and A2A, with the endpoint table and the contract conventions."
---

# API overview

`order-sourcing` has four interfaces. Three are read-only. The REST writes are the planner's "re-optimise now" and the operator's
`PUT /sourcing-policy` (ADR 0014); everything
else that changes an assignment arrives as a Kafka event. All of REST, MCP and A2A are **unauthenticated** by fleet decision
(`TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns under `internal/adapters/inbound`).

| Interface | Source of truth | Binary and port | Notes |
| --- | --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, version 0.1.0) | `cmd/api`, `HTTP_ADDR` default `:8080`; through Kong `/api/order-sourcing` (prefix stripped) | Five operations plus `/healthz` and `/readyz`. The pages under *REST* in the sidebar are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. The spec names one server, `http://localhost:8080`. |
| Events | `apis/asyncapi.yaml` (AsyncAPI 3.0.0, version 1.0.0) | `cmd/api` consumers and relay | Fifteen published events, 34 consumed types: the [event catalogue](/docs/api-reference/events); every producing command on [Domain events](/docs/ddd/domain-events). |
| MCP | [ADR 0006](/docs/adr/0006-mcp-server-adoption), `internal/adapters/inbound/mcp` | `cmd/mcp`, `MCP_ADDR` default `:8090`; Kong `/api/mcp/order-sourcing` | Streamable HTTP on `/mcp` (and `/`). Two read-only tools, no write tool: `list_assignments`, `get_assignment`. See [MCP tools](/docs/mcp/tools). |
| Reports | `apis/openapi.yaml` tag `reports`, [ADR 0013](/docs/adr/0013-analytics-read-side-and-the-sourcing-decision-fact) | `cmd/os-reports`, `HTTP_ADDR` default `:8092`; Kong `/api/order-sourcing/reports`, rewritten to `/reports` | Four read-only GETs over the separate analytical database that `cmd/os-projector` (admin `:8091`) feeds from this context's own two topics. |
| A2A | [ADR 0009](/docs/adr/0009-a2a-inbound-adapter), `internal/adapters/inbound/a2a` | `cmd/a2a`, `A2A_ADDR` default `:8093`; Kong `/api/a2a/order-sourcing` | JSON-RPC 2.0 on `/` and `/a2a`, Agent Card at `/.well-known/agent-card.json`. Two read-only skills, no write skill. Details below. |

The binaries and their ports are on [Architecture](/docs/overview/architecture); real calls are in the
[Quickstart](/docs/overview/quickstart).

## REST endpoints

| Tag | Endpoint | Operation id | Success | Errors |
| --- | --- | --- | --- | --- |
| Assignments | `GET /assignments` | `listAssignments` | `200 {items: [...], nextCursor}`, ordered by `assignmentId` ascending; filters `state`, `nodeId`, `orderId` combine with AND; `limit` (1 to 500, default 100) and `cursor` page it; an empty match is `200` with an empty `items` array | `400 invalid-limit`, `400 invalid-cursor`, `400 unknown-query-parameter`; `422 validation-failed` for an unknown `state` |
| Assignments | `GET /assignments/{assignmentId}` | `getAssignment` | `200`, the assignment with its release history, excluded nodes and current candidate | `404 not-found` |
| Sourcing policy | `GET /sourcing-policy` | `getSourcingPolicy` | `200`, the current policy `{version, crossRegionSurchargeCents, maxCostDeviationPercent, publishedAt}`; `version: 0` with both levers 0 and no `publishedAt` when nothing was published | none beyond `405`, `500` |
| Sourcing policy | `PUT /sourcing-policy` | `putSourcingPolicy` | `200`, the published version n+1; body `{crossRegionSurchargeCents, maxCostDeviationPercent}` (whole document, an omitted field is 0), header `Idempotency-Key` required because every call publishes a new version | `400 idempotency-key-required` / `idempotency-key-invalid` / `malformed-request`, `409` (`idempotency-key-reused`, `concurrent-update`), `422 validation-failed` outside 0..100000 / 0..200 |
| Assignments | `POST /assignments/{assignmentId}/reoptimisations` | `reoptimiseAssignment` | `200`, the assignment after re-optimisation (unchanged when the best candidate is the current one); optional body `{"assignmentVersion": n}`; header `Idempotency-Key` required | `400 idempotency-key-required` / `idempotency-key-invalid`, `404 not-found`, `409` (`idempotency-key-reused`, `assignment-locked`, `release-in-flight`, `node-excluded`, `stale-assignment-version`, `illegal-transition`, `concurrent-update`), `422` (`candidate-breaks-promise`, `validation-failed`) |
| reports | `GET /reports/sourcing-mix?from=&to=` | `getSourcingMixReport` | `200`, per UTC day of release: `groupsReleased`, `inRegionShare` (+ `unknownRegionCount`), `meanLegs`, `reassignmentRate`, `unsourceableCount`. Served by `cmd/os-reports` on `:8092` (ADR 0013) | `400 invalid-report-range` |
| reports | `GET /reports/order-splits?from=&to=` | `getOrderSplitsReport` | `200`, per UTC day of `FulfillmentQueued`: `ordersQueued`, `groupsPerOrder`, `splitOrderShare` | `400 invalid-report-range` |
| reports | `GET /reports/cost-to-serve?from=&to=` | `getCostToServeReport` | `200`, per UTC day over the last decision per assignment: `costCompleteShare`, `costPerUnitCents`, `p50CostCentsPerGroup`, `p90CostCentsPerGroup` | `400 invalid-report-range` |
| reports | `GET /reports/freshness` | `getReportFreshness` | `200`, `asOf` and `lagSeconds` of the analytics projection (both `null` before the first event) | none |
| Health | `GET /healthz`, `GET /readyz` | none in the spec | `200 {"status":"ok"}` / `200 {"status":"ready"}`; `/readyz` is `503` while draining or when Postgres or the Kafka broker is unreachable | see [Troubleshooting](/docs/operations/troubleshooting) |

Source: `internal/adapters/inbound/http/server.go` registers exactly these routes: `/healthz`, `/readyz`, `GET /assignments`,
`GET /assignments/{id}`, `POST /assignments/{id}/reoptimisations`, `GET /sourcing-policy` and `PUT /sourcing-policy`. `/healthz` and `/readyz` are not in `apis/openapi.yaml`.
Assignments are never created through REST: a `CustomerOrderPlaced` event creates them.

## Conventions in the contract

- **Errors** are RFC 9457 `application/problem+json`. The `type` is
  `https://errors.order-sourcing.retail-network.example/<slug>` (the spec examples show the bare slug). The slugs the code
  produces: `malformed-request`, `idempotency-key-required`, `idempotency-key-invalid`, `idempotency-key-reused`, `invalid-limit`, `invalid-cursor`, `unknown-query-parameter`, `validation-failed`, `not-found`, `assignment-locked`,
  `release-in-flight`, `node-excluded`, `stale-assignment-version`, `candidate-breaks-promise`, `illegal-transition`,
  `concurrent-update`, `method-not-allowed`, `resource-not-found` (an unknown path) and `internal-error`. The mapping from domain errors is on
  [Use cases](/docs/ddd/use-cases#errors-and-the-problem-types-they-become) and, with remedies, on
  [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
- **`Idempotency-Key`** is mandatory on the reoptimise route (1 to 128 printable ASCII characters; anything else is `400 idempotency-key-invalid`). The key is global:
  it is looked up by key alone and compared by fingerprint (method + path + SHA-256 of the body). The same request replays the first
  answer with `Idempotency-Replayed: true`; the same key with another request is `409 idempotency-key-reused`. It is implemented in
  `internal/adapters/inbound/http/idempotency.go` against the `idempotency_keys` table in the request's own transaction: the first
  answer is stored and replayed for 24 hours. See
  [Troubleshooting](/docs/operations/troubleshooting#idempotency-in-practice).
- **Optimistic concurrency.** An assignment carries `assignmentVersion`, which strictly increases. The optional
  `assignmentVersion` in the reoptimise body is checked against the current one (`409 stale-assignment-version`); 0 or absent
  skips the check.
- **Paging.** `GET /assignments` is keyset-paged by `assignmentId` with `limit` and an opaque `cursor`; the body is `{items, nextCursor}` and `nextCursor` is absent on the last page. Any other query parameter (`after`, `offset`, `nextToken`) is `400 unknown-query-parameter`.
- **No PII.** The context holds the destination postal prefix only, never an address or a person.
- **Auth: none.** Anyone who can reach a port can call it; network policy and the Kong route are the only boundary.

## A2A

`cmd/a2a` is a message-only agent: each call is one request mapped to one answer, with no task lifecycle and no streaming
(`Capabilities.Streaming` is false). The Agent Card is served at `/.well-known/agent-card.json` and advertises the JSON-RPC
interface at `A2A_AGENT_URL`, which must be a URL the clients can dial (in the cluster
`http://<release>-a2a.<ns>.svc.cluster.local:8093/`). It needs only `DATABASE_URL`.

A skill request is the first message part, a data part (or a text part holding the same JSON) of the form
`{"skill": "<id>", "arguments": {...}}`. Unknown fields are rejected on purpose, so a misspelt filter never silently widens the
answer.

| Skill id | Arguments | Same read as |
| --- | --- | --- |
| `find_assignments` | optional `state`, `nodeId`, `orderId`, `limit` (1 to 500, default 100) | `GET /assignments`, MCP `list_assignments` |
| `assignment_detail` | `assignmentId` (required) | `GET /assignments/{id}`, MCP `get_assignment` |

The reply is a message with a data part (`{skill, data}` where `data` holds `assignmentList` or `assignment`, in the REST body
shape) and a text part with a one-line summary. A failure is answered **in the message** as a problem
(`{skill, problem: {type, title, detail, status}}`), never as a JSON-RPC error: `malformed-request`, an unknown skill, an
invalid argument, `not-found` (404), `validation-failed` (422) or `internal-error` (500, no infrastructure text in the detail).
A NUL character in an argument is refused before it reaches Postgres.

Source: `internal/adapters/inbound/a2a/skills.go` (`Card`, `answer`, `findAssignments`, `assignmentDetail`),
`mapping.go`, `cmd/a2a/router.go`. The architecture tests `TestA2AAgentIsReadOnly` and `TestA2AHasNoOutboundClient` keep the agent
read-only and free of any A2A client: this context calls no sibling, and facts between contexts still travel as CloudEvents on
Kafka.

## Where code and spec differ

- The spec describes `/healthz` and `/readyz` nowhere; the router serves them (see
  [Runbook](/docs/operations/runbook#health)).
- The spec lists only `http://localhost:8080` as a server; the cluster route through Kong is `/api/order-sourcing`.
