# 1. Order Sourcing is a bounded context of the retail network

Status: Accepted (bootstrap). Design source: retail-network-docs `model/model.yaml`, context `order-sourcing`, and its ADRs RN-0001 and RN-0007.

## Context

The retail network is modelled as ten bounded contexts, each its own service and database. Decides, as late as the promise allows, which node and path will actually fulfil each shipment group. Keeps assignments in a virtual queue, re-optimises them against current capacity and cost until the release point, sends the group to the node, reassigns on rejection or expiry, and locks the assignment once the node commits.

## Decision

- This repo owns the aggregates FulfillmentAssignment; the invariants and error names are those of the model.
- Business decisions of the context:
- The assigned node may differ from the promise's witness node; the only hard constraint is that the assigned path still meets deliverByLatest.
- An assignment is free to move while QUEUED; after release it moves only on rejection or expiry; after the node commits it is LOCKED for good.
- A group goes to one node in full (fill-or-kill); a partial answer is not a thing.
- If no node can still meet deliverByLatest, the group is UNSOURCEABLE and delivery-promise re-promises; sourcing never breaks a promise silently.
- In-region nodes are preferred; cross-region is allowed only when it is the only way to keep the promise.
- Events use the network CloudEvents namespace `com.retailnetwork.commerce.order-sourcing.<entity>.<Event>` on `retailnetwork.order-sourcing.events`
  (analytics-only events on `.analytics`). Published: FulfillmentQueued, FulfillmentReleasedToNode, FulfillmentReassigned, FulfillmentLocked, FulfillmentShipped, FulfillmentDelivered, FulfillmentUnsourceable, FulfillmentCancelled. No `warehouse.*` topic is consumed or produced.
- No live REST lookup into sibling contexts except the calls the model lists under `sync_calls`.
- No authentication on REST, consistent with the fleet. PII exists only in customer-ordering and shipping-and-tracking.

## Consequences

Relationships and the full consumed-event list are in the model and in the AsyncAPI contract added with the first feature PR.
