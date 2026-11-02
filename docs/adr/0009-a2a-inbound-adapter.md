# ADR 0009: an A2A inbound adapter (read-only)

## Status

Accepted (2026-10-29). Follows [ADR 0006](./0006-mcp-server-adoption.md) (the
read-only MCP adapter this one is derived from) and
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) (CloudEvents on
every Kafka message). Mirrors the reference rollout in workforce-management
(ADR 0039) and wes-work-planning (ADR 0046).

## Context

The fleet is adopting the Agent2Agent (A2A) protocol so that a bounded context
can **delegate** a goal to another context's agent. order-sourcing already
exposes its two reads over REST and MCP (`usecases.Queries`:
`ListAssignments`, `GetAssignment`). Three transports now carry three different
things and must not overlap:

| Transport | Carries |
| --- | --- |
| Kafka + CloudEvents | **facts** between contexts (ADR 0004) |
| MCP | an agent's **tools** |
| A2A | **delegation** of a goal to another context's agent, never data sync |

## Decision

- **A third driving adapter, a separate binary.**
  `internal/adapters/inbound/a2a` is built on the official A2A Go SDK
  (`github.com/a2aproject/a2a-go/v2` v2.7.0) and served by `cmd/a2a`: JSON-RPC
  2.0 over HTTP, port **8093**, the Agent Card at
  `/.well-known/agent-card.json`, the endpoint mounted at `/` and `/a2a`, an
  open `/healthz`. It is a third deployable beside `cmd/api` and `cmd/mcp`; the
  Helm chart gets an a2a Deployment, Service and (off by default) Gateway API
  HTTPRoute (`/api/a2a/order-sourcing` rewritten to `/`), all behind
  `a2a.enabled` (default off).
- **The write gate: the agent is READ-ONLY.** A2A skills may change state only
  if the context's MCP surface already offers that write. The MCP charter of
  ADR 0006 is read-only (no write tool, `TestToolSurface` fails the build on a
  write verb), so this agent offers no write either: delegation must never
  widen what the context exposes. `POST /assignments/{id}/reoptimisations` stays
  REST-only and every other lifecycle change arrives as a consumed Kafka event
  handled by `cmd/api`.
- **Two curated, goal-shaped skills, derived from the two MCP tools** (not one
  per endpoint):

  | Skill | Kind | What it does |
  | --- | --- | --- |
  | `find_assignments` | read | which shipment groups match: `state?`, `orderId?`, `nodeId?`, `limit?` (1..500, default 100), ordered by assignmentId; the same `ListAssignments` read as `GET /assignments` and `list_assignments` |
  | `assignment_detail` | read | one assignment in full by `assignmentId`: promise, lines, candidate and cost, release point and history, excluded nodes, lock and shipment facts; the same `GetAssignment` read as `GET /assignments/{assignmentId}` and `get_assignment` |

  A request is a message whose first part is `{"skill":"<id>","arguments":{...}}`
  (a data part, or a text part holding the same JSON). The argument names and
  the result bodies are the REST/MCP ones (lowerCamelCase); domain structs
  never cross the boundary.
- **Message-only, answered in-band.** Both skills are request/response reads,
  so the agent has no Task lifecycle. Domain rejections and malformed requests
  are **answered** as RFC 7807-shaped `problem`s inside the message envelope,
  never raised as JSON-RPC errors, with the **same classification and slugs as
  the REST adapter** (`internal/adapters/inbound/http/errors.go`), restricted to
  what a read can raise: `404 not-found` (unknown id), `422 validation-failed`
  (unknown state, limit outside 1..500: the REST status of `shared.ErrValidation`),
  `500 internal-error` (anything else, with a generic detail: DSNs and SQL text
  never reach the caller). Request-shape failures are `400`:
  `malformed-request` (unusable part, not JSON, unknown field, wrong type,
  trailing data, a NUL character in a filter), `unknown-skill`,
  `invalid-argument` (missing `assignmentId`). A NUL in `assignmentId` is a
  `404 resource-not-found`, as REST answers a NUL in the URL path. No `503` or
  `409` is reachable from a read.
- **Strict input.** Unknown argument names are rejected, not ignored, and so is
  trailing data: a misspelled `ordrId` must not silently drop a filter and
  answer with every order. (Go's `encoding/json` matches names
  case-insensitively, so `orderid` still reaches `orderId`.) The two skills
  take the same canonical names as the MCP tools; no deprecated alias exists in
  this context's errors or arguments, and a test pins that an infrastructure
  failure leaks no text.
- **Read-only is structural.** The adapter's `Deps` holds exactly
  `Queries *usecases.Queries`; the adapter and `cmd/a2a` import no outbox, no
  event sink, no Kafka adapter and no sweep, and `cmd/a2a` never dials Kafka,
  never runs the consumers, the relay or the sweep (no `KAFKA_BROKERS`, no
  `EVENT_PUBLISHER`). `internal/architecture` pins it:
  `TestA2AAdapterDependencyRule` (depends only on application and domain;
  nothing depends on it), `TestA2AAgentIsReadOnly` (no write use case in `Deps`
  or body), `TestA2AHasNoOutboundClient` (no `a2aclient` in production code:
  this context calls no sibling) and `TestA2ABinaryIsKafkaFreeAndReadOnly`.
  The skill ids carry no write verb (`TestCardOffersNoWriteSkill`).
- **Tests over the real wire.** The skills are driven through `httptest` and
  the SDK's own client (`a2aclient.NewFromCard`) with a test-local envelope
  struct, so contract drift fails the suite. `cmd/a2a` has boot-config
  refusals, router and card tests, and a `//go:build integration` test that
  reads what the domain's write path persisted in a real Postgres through both
  skills and proves the reads add no `outbox_events` row.
- **Observability.** One OTel span per skill run (`a2a.skill <id>`, attribute
  `a2a.skill`), like `mcp.tool <name>`; the router carries the fleet
  middleware of ADR 0008.
- **Unauthenticated, no sibling calls.** The card declares no security schemes:
  the surface is unauthenticated by the same fleet-wide decision as REST and MCP
  (the revert of 2026-09-11; `TestNoAuthMiddlewareReintroduced` scans this
  adapter too). Access control is the in-cluster ClusterIP boundary.
- **Boot.** `DATABASE_URL` and `A2A_AGENT_URL` are mandatory, each a boot error
  naming the variable raised before telemetry, any connection and the listener.
  `A2A_AGENT_URL` must be an absolute http(s) URL (the card advertises what
  clients dial); the chart defaults it to
  `http://<fullname>-a2a.<namespace>.svc.cluster.local:8093/` and
  `a2a.agentUrl` overrides it. `A2A_ADDR` defaults to `:8093`. The binary runs
  the idempotent embedded migrations on start like `cmd/mcp`.

## Consequences

- A delegating agent can ask "where does order X ship from, which node holds
  group 2, why was node Y excluded" with the A2A goal-shaped contract, with
  the same outcomes a REST caller sees.
- A third deployable (`/app/a2a`) in the same image; the Dockerfile builds every
  `cmd/*`, so no build change beyond the documented `EXPOSE 8093`.
- Adding a skill, above all a write, is a reviewed act: it needs the MCP
  surface to offer it first (an amendment of ADR 0006), then this ADR, the
  `Deps` pin and the card tests change together. A write skill would also need
  the transactional outbox in `cmd/a2a`, an integration test proving
  UNPUBLISHED outbox rows, and the relay stays in `cmd/api`.
- Port 8093 is the fleet's A2A port (8080 is the api, 8090 the MCP server).
