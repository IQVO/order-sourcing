---
id: ddd-artifacts
title: DDD artifacts (ddd-crew)
sidebar_label: DDD artifacts index
description: "The strategic and tactical design of Order Sourcing drawn with the ddd-crew tools plus UML and ER diagrams, each derived from the code and ADRs on develop."
---

# DDD artifacts (ddd-crew)

The strategic and tactical design of `order-sourcing`, drawn with the [ddd-crew](https://github.com/ddd-crew) tools plus UML and
ER diagrams. Every diagram is Mermaid, derived from the code and ADRs on `develop`, and carries a `Source:` line naming the files
it came from. The same pack is held in the aggregator
([warehouse-docs, network contexts](https://iqvo.github.io/warehouse-docs/network/contexts/order-sourcing)); that copy was written
before the service was built and this one follows the code.

| Artifact | ddd-crew tool / notation | What it shows |
| --- | --- | --- |
| [Core domain chart](/docs/ddd/core-domain-chart) | [Core Domain Charts](https://github.com/ddd-crew/core-domain-charts) | Where this context sits on business differentiation versus model complexity, and why it is Core. |
| [Bounded context canvas](/docs/ddd/bounded-context-canvas) | [Bounded Context Canvas v5](https://github.com/ddd-crew/bounded-context-canvas) | Purpose, classification, roles, every inbound and outbound message, business decisions, what is still open. |
| [Context map](/docs/ddd/context-map) | [Context Mapping](https://github.com/ddd-crew/context-mapping) | This context's slice of the network map: pattern, channel and messages per edge. |
| [Aggregate design canvas](/docs/ddd/aggregate-design-canvas) | [Aggregate Design Canvas v1.1](https://github.com/ddd-crew/aggregate-design-canvas) | `FulfillmentAssignment`: states, invariants, policies, commands, events; the five read models. |
| [Domain message flow](/docs/ddd/domain-message-flow) | [Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling) | Happy path, refusal and re-promise, shipment and delivery, and cancellation across contexts. |
| [EventStorming](/docs/ddd/eventstorming) | [EventStorming glossary and cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet) | Process-level boards with the sticky-note colours, plus the hotspot inventory. |
| [Ubiquitous language](/docs/ddd/ubiquitous-language) | Glossary | Every term with the code identifier that implements it, and the neighbour terms it borrows. |
| [Class diagrams](/docs/ddd/class-diagram) | UML class diagram | The aggregate and its value objects, the ten events, the search types, and the hexagonal ports and adapters. |
| [Entity-relationship diagram](/docs/ddd/entity-relationship) | ER diagram | The Postgres schema created by `0001_order_sourcing_schema.up.sql`: 14 tables. |
| [Sequence diagrams](/docs/ddd/sequence-diagrams) | UML sequence diagram | Queue, re-optimise, sweep and release, refusal, lock, ship and deliver, cancel and revise, with error branches. |
| [Domain events](/docs/ddd/domain-events) | Event catalogue | The ten published events with typed fields and consumers, and the 32 consumed types. |

Related pages outside the pack: [Use cases](/docs/ddd/use-cases), [Subdomain classification](/docs/ddd/subdomain-classification),
[Integration](/docs/ecosystem/integration) and the [event catalogue](/docs/api-reference/events).

:::note Sources of truth
The code wins. Domain rules come from `internal/domain/assignment`, `sourcing` and `network`, behaviour from
`internal/application/usecases`, the envelope from `internal/adapters/kafka/cloudevents`, the schema from
`internal/adapters/outbound/postgres/migrations`, the REST contract from `apis/openapi.yaml`, the event contract from
`apis/asyncapi.yaml` and decisions from the ADRs in `docs/adr/`. Where a page and the code disagree, the page is wrong.
:::

:::info Not in this pack
The MCP server ([ADR 0006](/docs/adr/0006-mcp-server-adoption)), the A2A agent
([ADR 0009](/docs/adr/0009-a2a-inbound-adapter)) and the web remote ([ADR 0007](/docs/adr/0007-console-remote), `web/`) are built,
but they are read surfaces over the model and add no aggregate, entity or event of their own, so the pack has no pages for them.
They are described on the [API overview](/docs/api-reference) and [MCP tools](/docs/mcp/tools).
:::
