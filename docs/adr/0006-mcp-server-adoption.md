# ADR 0006: a read-only MCP server

## Status

Accepted (2026-10-24).

## Context

Every other bounded context of the fleet that exposes an HTTP API also ships an
MCP server so that agents (warehouse-ops-agent first) can read its data without
a bespoke REST client. order-sourcing had none: its two reads (ListAssignments,
GetAssignment) were reachable only over REST. The service already has the read
use cases behind its REST adapter (`usecases.Queries`), so an MCP adapter is
another driving adapter over them, not new domain.

## Decision

- **A second driving adapter, a separate binary.** `internal/adapters/inbound/mcp`
  is built on the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`
  v1.8.0) and served over Streamable HTTP only (no stdio, no SSE) by `cmd/mcp`,
  mounted at `/mcp` and `/` with an open `/healthz`. It is a second deployable
  beside `cmd/api`; the Helm chart gets an mcp Deployment, Service and
  (off by default) Gateway API HTTPRoute, all behind `mcp.enabled`.
- **Two read tools over the existing queries, nothing else.**
  `list_assignments(state?, nodeId?, orderId?, limit?)` and
  `get_assignment(assignmentId)` — one per read GET of `apis/openapi.yaml`,
  the SAME `usecases.Queries` the REST adapter calls. Arguments and results
  are lowerCamelCase like the REST contract; typed errors carry the REST
  problem slug (`not-found`, `validation-failed`) as the prefix of the tool
  error. `POST /assignments/{id}:reoptimise` stays REST-only: asking for an
  immediate re-optimisation is a planner's deliberate act, not an agent's
  read, and every other lifecycle change arrives as a consumed Kafka event
  handled by `cmd/api`.
- **Read-only is structural.** The adapter's `Deps` holds exactly the Queries
  struct and nothing that can write; the adapter and `cmd/mcp` import no
  outbox, no event sink, no Kafka adapter and no sweep. `cmd/mcp` never dials
  Kafka and never runs the consumers, the relay or the virtual-queue sweep.
  `internal/architecture`'s `TestMCPAdapterDependencyRule` pins the imports
  and the `Deps` fields; `TestToolSurface` pins the exact tool set (budget 2),
  the read-only annotations and the absence of write verbs in tool names;
  `TestToolRegistryGolden` pins the whole advertised registry;
  `TestReadsEmitNothing` and the integration test prove the reads add no
  outbox row in a real Postgres.
- **No sibling calls, no auth.** The server reads this context's own tables
  only, calls no other context over REST or MCP, and has no auth layer (the
  fleet-wide revert of 2026-09-11; `TestNoAuthMiddlewareReintroduced`). Access
  control is the in-cluster ClusterIP boundary.
- **Boot.** `DATABASE_URL` is mandatory (no in-memory mode); the binary runs
  the idempotent embedded migrations on start like `cmd/api` does (with
  `MIGRATIONS_DATABASE_URL` for a direct DSN behind PgBouncer), wrapped in
  `internal/bootretry` so an injected pod's reset first dial retries, and it
  drains gracefully on SIGINT/SIGTERM.

## Consequences

- Agents can answer "where will order X ship from, which node holds group 2,
  why was node Y excluded" without REST glue.
- Adding a tool is a reviewed act: the tool budget, the curated list and the
  golden file all change together (an amendment of this ADR).
- Re-optimising an assignment stays a REST operation; the assignment lifecycle
  stays with the consumers and the sweep in `cmd/api`.
