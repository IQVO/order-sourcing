# Architecture decision records

The decisions of `order-sourcing`, in the order they were taken. The ADR bodies
are immutable; a changed decision gets a new ADR that supersedes the old one.
The *Status* column is copied from each ADR's own `## Status` section.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](./0001-order-sourcing-bounded-context.md) | Order Sourcing is a bounded context of the retail network | Accepted (bootstrap) |
| [0002](./0002-aggregates-invariants-and-assumptions.md) | the FulfillmentAssignment aggregate, its invariants and the assumptions the model left open | Accepted (2026-10-23) |
| [0003](./0003-consumed-contracts-and-local-read-models.md) | consumed contracts, local read models and relationships | Accepted (2026-10-23) |
| [0004](./0004-cloudevents-envelope-and-type-catalogue.md) | CloudEvents envelope and type catalogue | Accepted (2026-10-23) |
| [0005](./0005-outbox-relay-attempt-cap-sweep-and-shutdown.md) | outbox relay, attempt cap, queue sweep and shutdown order | Accepted (2026-10-23) |
| [0006](./0006-mcp-server-adoption.md) | a read-only MCP server | Accepted (2026-10-24) |
| [0007](./0007-console-remote.md) | a web console remote | Accepted (2026-10-24) |
| [0008](./0008-fleet-observability-standard.md) | the fleet observability standard (logs, metrics, traces) | Accepted (2026-10-26) |
| [0009](./0009-a2a-inbound-adapter.md) | an A2A inbound adapter (read-only) | Accepted (2026-10-29) |
