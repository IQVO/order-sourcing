---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
description: "Order Sourcing is a Core subdomain of the retail network (CloudEvents tier commerce): the reasoning, what would move it, and its neighbours' verdicts."
---

# Subdomain classification

`order-sourcing` is a **Core** subdomain of the **Network** area. Its CloudEvents tier segment is `commerce`
(`com.retailnetwork.commerce.order-sourcing.assignment.<EventName>`, see
`internal/adapters/kafka/cloudevents/cloudevents.go`). The verdict is the one in the retail-network table of the warehouse-docs
aggregator (`network/docs/strategic-design/subdomain-classification.md` on `origin/main`), and it matches the strategic
classification of [ADR 0001](/docs/adr/0001-order-sourcing-bounded-context). The neighbour rows below are copied from that
table, not recalled.

## Why Core

- **Differentiation.** Late assignment with re-optimisation until a lock is where cost to serve is decided order by order, and
  where the promise made at checkout is kept or handed back. The decision structure is specific to this network: the virtual
  queue and its release point, the `AssignmentCost` ranking (legs, wait, cross-region, scorecard and promise-book pressure),
  exclusion of refusing nodes, and the lock on the vendor's acceptance
  ([Use cases](/docs/ddd/use-cases#how-a-candidate-is-chosen)). Buying it would mean conforming to someone else's idea of this
  network.
- **Model complexity.** One aggregate, but a rich one: seven states, a release history with at most one pending release, an
  exclusion list that resets with each promise revision, and per-package delivery. It reacts to 32 consumed event types from nine
  topics and keeps five event-fed views in ten `rm_*` tables, because it calls no sibling. Two loops cross a context boundary
  (release, purchase order, acceptance or refusal with `vendor-connectivity`; unsourceable, re-promise, re-queue with
  `delivery-promise`).
- **Business model:** cost-to-serve reduction and speed protection. **Evolution:** custom-built, with the penalty weights still
  tunable constants (`internal/domain/sourcing/sourcing.go`).

What would move it: if the weights settle into fixed defaults that rarely change, parts of `AssignmentCost` could drift toward
Product. The structure (late release, lock on acceptance, hand back when unsourceable) is what keeps the context Core. See the
[Core domain chart](/docs/ddd/core-domain-chart).

## Neighbours (from the network table)

| Bounded context | Classification | Tier | Relation to this context |
| --- | --- | --- | --- |
| `delivery-promise` | Core | `commerce` | upstream (`PromiseRevised`, `PromiseBookExhausted`) and downstream (reads `FulfillmentUnsourceable`, `FulfillmentLocked`, `FulfillmentShipped`) |
| `customer-ordering` | Generic | `commerce` | upstream (`CustomerOrderPlaced`, `CustomerOrderCancelled`) and downstream (reflects sourcing facts) |
| `node-topology` | Supporting | `logistics` | upstream (nodes, lanes, service areas, regions) |
| `offer-availability` | Supporting | `commerce` | upstream (listings, discontinued items) |
| `vendor-connectivity` | Supporting | `partner` | upstream (purchase-order outcomes) and downstream (consumes releases and cancels) |
| `linehaul-transportation` | Supporting | `logistics` | upstream (departures, booking moves) and downstream (consumes releases, reassignments, locks, cancels) |
| `last-mile-delivery` | Supporting | `logistics` | upstream (delivery waves) |
| `node-performance` | Supporting | `partner` | upstream (scorecards) |
| `shipping-and-tracking` | Generic | `logistics` | upstream (`PackageDelivered`) |
| `seller-inbound` | Supporting | `partner` | no edge |

The aggregator's network table counts two Core, seven Supporting and two Generic contexts; this page lists only the contexts
this one touches or deliberately excludes. The aggregator table is the authority if the two ever differ.

## Tier is not classification

The tier segment groups contexts by area of the business (`commerce`, `logistics`, `partner`); it is not the Core, Supporting or
Generic verdict. For this context the two axes read: tier `commerce`, class Core.

## Discrepancy with the aggregator

The aggregator's chart and canvas pages count 29 consumed event types, 9 commands and 8 published events. The code on `develop`
consumes 32 types and publishes 10 (eight integration events, two analytics-only), including `BookingMoved`, `BookingFailed`,
`DeliveryWaveCancelled`, `FulfillmentCancelRefused` and `FulfillmentConstraintRevised`
([Event catalogue](/docs/api-reference/events)). The verdict does not depend on the count.
