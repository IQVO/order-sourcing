---
id: introduction
title: Introduction
sidebar_label: Introduction
description: "What Order Sourcing owns and does not own: the late choice of the vendor node and path for each shipment group, its virtual queue, and how to find your way around this documentation."
---

# Order Sourcing

**Order Sourcing** (`order-sourcing`, code `OS`) is a bounded context of the retail network: area **Network**, subdomain class
**Core** (CloudEvents tier segment `commerce`). It decides, as late as the promise allows, which node and path will actually fulfil
each shipment group of a customer order. It owns one aggregate, **`FulfillmentAssignment`**, and the **virtual queue** behind it.
Assignments wait in that queue, where each candidate (node, path, cost) is re-optimised against current capacity until its
release point. At release the group is sent to exactly one vendor node, which for a vendor node means a purchase order issued
through `vendor-connectivity`. A rejection or an expiry moves the group to another node, the node's acceptance **locks** the
choice for good, and when no node can still meet the promise's `deliverByLatest` the group becomes `UNSOURCEABLE` so that
`delivery-promise` can re-promise. Sourcing never breaks a promise silently.

This repository is a service of the retail network, not of the warehouse-systems fleet; the fleet reaches the network only through
the `vendor-connectivity` Vendor API. This site documents the service from its code: every identifier, route, variable and event
type is taken from the repository on `develop`.

## Why the decision is separate from the promise

At checkout `delivery-promise` tells the shopper a date and proves it with one **witness path**: a concrete vendor node, its pickup
departure, the linehaul legs and a delivery wave that can carry the package in time. That proof shows the promise *can* be kept; it
does not decide *how*. Between checkout and the moment a node must start work the network keeps changing: other orders land on the
same cutoff, a departure fills up or is cancelled, a scorecard drops, a vendor sends new inventory, a cheaper in-region node
appears. Sending every group to its witness node at once would freeze each order's routing at its worst-informed moment.

The release point is computed, not configured: for every tier but `SAME_DAY` it is the pickup cutoff of the best candidate minus
the node's handling time minus a 30 minute vendor acknowledgement budget, and never earlier than now; a `SAME_DAY` group is released
at once (`internal/domain/sourcing/sourcing.go`). The lock point is the vendor's acceptance of the purchase order. Where the
operator picks its own stock the natural lock point is the pick; here the nodes are vendor-operated and the network cannot see the
pick, so the acceptance is the first moment the vendor commits.

## What it owns

| Capability | Where it lives |
| --- | --- |
| Queue one assignment per shipment group of a placed order; an unservable group is `UNSOURCEABLE` at once | `QueueAssignment`, [use cases](/docs/ddd/use-cases) |
| Search the cheapest candidate that still meets `deliverByLatest`: `cost = 100 per leg + 10 per hour of wait + 1000 cross-region + scorecard penalty (A 0, B 50, C 200, D 500) + 500 book pressure`, ties to the witness node | `internal/domain/sourcing` |
| Hold the virtual queue, re-optimise on every sweep and on demand, release at `releaseAt` | `SweepQueue`, `ReoptimiseAssignment`, `ReleaseToNode` |
| Reassign on refusal or expiry (the refusing node is excluded for that promise revision) and lock on acceptance | `HandleNodeRefusal`, `LockAssignment` |
| Follow the group through `SHIPPED` and `DELIVERED`, record a moved pickup departure | `MarkShipped`, `MarkDelivered`, `RecordPickupMoved` |
| Decide cancels (`FulfillmentCancelled` before the lock, `FulfillmentCancelRefused` after it) and apply revised promises in every non-terminal state | `CancelAssignment`, `ApplyRevisedPromise` |
| Maintain local read models of the network (five views stored in ten `rm_*` tables: nodes, lanes, departures, waves, service areas, regions, listings, discontinued items, scorecards, book pressure) from nine sibling topics | `Projector`, [Integration](/docs/ecosystem/integration) |
| Publish eight integration events and two analytics-only events through a transactional outbox | [Events](/docs/api-reference/events) |
| Serve a read-only REST, MCP and A2A surface plus one planner action | [API overview](/docs/api-reference), [MCP tools](/docs/mcp/tools) |
| A small read-only operator remote (`web/`, `order_sourcing_mfe`) mounted by the warehouse console | `web/`, [ADR 0007](/docs/adr/0007-console-remote) |

## What it does not own

| Not here | Where it lives |
| --- | --- |
| Computing the promise, holding promise-book capacity, judging kept or broken, splitting a cart into shipment groups | `delivery-promise` |
| The purchase-order protocol: `ackBy`, fill-or-kill acknowledgement, labels, shipment confirmation | `vendor-connectivity` |
| Picking, packing and stock placement inside a vendor node | The vendor (for the warehouse-systems fleet, `network-fulfillment` through the Vendor API) |
| Booking the package on the pickup departure, building loads | `linehaul-transportation` |
| The tracking timeline, labels and the ship-to address (no PII ever enters this context) | `shipping-and-tracking`, `customer-ordering` |
| Customer cancellation rules and the customer-facing status | `customer-ordering` |
| Returns, payments, notifications | other contexts |

It calls no other service: everything it knows about the network is a local read model fed by sibling events, and it exposes no
client to any sibling over REST, MCP or A2A.

## Surface at a glance

| Interface | Port | Detail |
| --- | --- | --- |
| REST (`cmd/api`) | 8080 | `GET /assignments`, `GET /assignments/{assignmentId}`, `POST /assignments/{assignmentId}:reoptimise`, `GET /healthz`, `GET /readyz`; no authentication; errors are RFC 9457 `application/problem+json` |
| Kafka (`cmd/api`) | n/a | consumes 32 event types from nine topics, publishes 8 types on `retailnetwork.order-sourcing.events` and 2 on `retailnetwork.order-sourcing.analytics`, CloudEvents 1.0 |
| MCP (`cmd/mcp`) | 8090 | Streamable HTTP, read-only, tools `list_assignments` and `get_assignment` |
| A2A (`cmd/a2a`) | 8093 | JSON-RPC 2.0 on `/` and `/a2a`, Agent Card at `/.well-known/agent-card.json`, read-only skills `find_assignments` and `assignment_detail` |

## Where to go next

| You want to | Read |
| --- | --- |
| Understand the layout and the data stores | [Architecture](/docs/overview/architecture), [Runtime](/docs/overview/runtime) |
| Build and run it locally | [Quickstart](/docs/overview/quickstart) |
| Configure and deploy it | [Configuration](/docs/operations/configuration), [Runbook](/docs/operations/runbook), [Before the first release](/docs/operations/before-first-release) |
| Watch and debug it | [Observability](/docs/operations/observability), [Troubleshooting](/docs/operations/troubleshooting) |
| Test it | [Testing](/docs/development/testing) |
| Learn the domain | [Ubiquitous language](/docs/ddd/ubiquitous-language), [Use cases](/docs/ddd/use-cases), [Subdomain classification](/docs/ddd/subdomain-classification), [DDD artifacts](/docs/ddd/ddd-artifacts) |
| Integrate with it | [API overview](/docs/api-reference), [Events](/docs/api-reference/events), [Integration](/docs/ecosystem/integration), [MCP tools](/docs/mcp/tools) |
| Know why it is built this way | [ADR index](/docs/adr) |
