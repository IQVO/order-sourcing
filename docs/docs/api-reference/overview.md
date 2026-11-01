---
id: overview
title: API overview
sidebar_label: API overview
slug: /api-reference
description: "Order Sourcing interfaces: REST, Kafka events, MCP and A2A, with the endpoint table and the contract conventions."
---

# API overview

`order-sourcing` has four interfaces. Three are read-only. The only REST write is the planner's "re-optimise now"; everything
else that changes an assignment arrives as a Kafka event. All of REST, MCP and A2A are **unauthenticated** by fleet decision
(`TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns under `internal/adapters/inbound`).

| Interface | Source of truth | Binary and port | Notes |
| --- | --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, version 0.1.0) | `cmd/api`, `HTTP_ADDR` default `:8080`; through Kong `/api/order-sourcing` (prefix stripped) | Three operations plus `/healthz` and `/readyz`. The pages under *REST* in the sidebar are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. The spec names one server, `http://localhost:8080`. |
| Events | `apis/asyncapi.yaml` (AsyncAPI 3.0.0, version 1.0.0) | `cmd/api` consumers and relay | Ten published events, 32 consumed types: the [event catalogue](/docs/api-reference/events); every producing command on [Domain events](/docs/ddd/domain-events). |
| MCP | [ADR 0006](/docs/adr/0006-mcp-server-adoption), `internal/adapters/inbound/mcp` | `cmd/mcp`, `MCP_ADDR` default `:8090`; Kong `/api/mcp/order-sourcing` | Streamable HTTP on `/mcp` (and `/`). Two read-only tools, no write tool: `list_assignments`, `get_assignment`. See [MCP tools](/docs/mcp/tools). |
| A2A | [ADR 0009](/docs/adr/0009-a2a-inbound-adapter), `internal/adapters/inbound/a2a` | `cmd/a2a`, `A2A_ADDR` default `:8093`; Kong `/api/a2a/order-sourcing` | JSON-RPC 2.0 on `/` and `/a2a`, Agent Card at `/.well-known/agent-card.json`. Two read-only skills, no write skill. Details below. |

The binaries and their ports are on [Architecture](/docs/overview/architecture); real calls are in the
[Quickstart](/docs/overview/quickstart).

## REST endpoints

| Tag | Endpoint | Operation id | Success | Errors |
| --- | --- | --- | --- | --- |
| Assignments | `GET /assignments` | `listAssignments` | `200 {assignments: [...]}`, ordered by `assignmentId` ascending; filters `state`, `nodeId`, `orderId`, `limit` (1 to 500, default 100) combine with AND; an empty match is `200` with an empty array | `422 validation-failed` for an unknown `state`, or a `limit` that is not an integer or outside 1 to 500 |
| Assignments | `GET /assignments/{assignmentId}` | `getAssignment` | `200`, the assignment with its release history, excluded nodes and current candidate | `404 not-found` |
| Assignments | `POST /assignments/{assignmentId}:reoptimise` | `reoptimiseAssignment` | `200`, the assignment after re-optimisation (unchanged when the best candidate is the current one); optional body `{"assignmentVersion": n}`; header `Idempotency-Key` required | `400 idempotency-key-required`, `404 not-found`, `409` (`assignment-locked`, `release-in-flight`, `node-excluded`, `stale-assignment-version`, `illegal-transition`, `concurrent-update`), `422` (`candidate-breaks-promise`, `validation-failed`) |
| Health | `GET /healthz`, `GET /readyz` | none in the spec | `200 {"status":"ok"}` / `200 {"status":"ready"}`; `/readyz` is `503` while draining or when Postgres or the Kafka broker is unreachable | see [Troubleshooting](/docs/operations/troubleshooting) |

Source: `internal/adapters/inbound/http/server.go` registers exactly these routes: `/healthz`, `/readyz`, `GET /assignments`,
`GET /assignments/{id}` and `POST /assignments/{id}:reoptimise`. `/healthz` and `/readyz` are not in `apis/openapi.yaml`.
Assignments are never created through REST: a `CustomerOrderPlaced` event creates them.

## Conventions in the contract

- **Errors** are RFC 9457 `application/problem+json`. The `type` is
  `https://errors.order-sourcing.retail-network.example/<slug>` (the spec examples show the bare slug). The slugs the code
  produces: `malformed-request`, `idempotency-key-required`, `validation-failed`, `not-found`, `assignment-locked`,
  `release-in-flight`, `node-excluded`, `stale-assignment-version`, `candidate-breaks-promise`, `illegal-transition`,
  `concurrent-update`, `method-not-allowed`, `resource-not-found` (an unknown path) and `internal-error`. The mapping from domain errors is on
  [Use cases](/docs/ddd/use-cases#errors-and-the-problem-types-they-become) and, with remedies, on
  [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
- **`Idempotency-Key`** is mandatory on the reoptimise route (1 to 128 characters; a longer key is `422 validation-failed`). It
  is implemented in `internal/adapters/inbound/http/idempotency.go` against the `idempotency_keys` table in the request's own
  transaction: the first answer is stored and replayed for 24 hours. See
  [Troubleshooting](/docs/operations/troubleshooting#idempotency-in-practice).
- **Optimistic concurrency.** An assignment carries `assignmentVersion`, which strictly increases. The optional
  `assignmentVersion` in the reoptimise body is checked against the current one (`409 stale-assignment-version`); 0 or absent
  skips the check.
- **Paging.** `GET /assignments` has a `limit` and no cursor; ordering is by `assignmentId`.
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

- `apis/openapi.yaml` does not list the `400 idempotency-key-required` answer of the reoptimise route; the router returns it
  (`internal/adapters/inbound/http/errors.go`, `idempotency.go`). It lists only `422` for an invalid `limit`, which is what the
  code returns.
- The spec describes `/healthz` and `/readyz` nowhere; the router serves them (see
  [Runbook](/docs/operations/runbook#health)).
- The spec lists only `http://localhost:8080` as a server; the cluster route through Kong is `/api/order-sourcing`.
