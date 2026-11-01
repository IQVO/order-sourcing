---
id: domain-events
title: Domain events
sidebar_label: Domain events
description: "Every CloudEvents type Order Sourcing publishes (ten, entity assignment) with topic, key, typed data fields, producing command and consumers, and every event it consumes with what it does and how it stays idempotent."
---

# Domain events

The envelope rules, the topic-by-topic list of all 42 types and the delivery guarantees are on the
[event catalogue](/docs/api-reference/events). This page adds, per published event, the typed `data` fields, the producing command
and the consumers, and, per consumed event, what the handler does and how it stays idempotent. Source:
`internal/domain/assignment/events.go`, `internal/adapters/outbound/kafka/encoder.go` (the wire structs),
`internal/adapters/inbound/kafka/handlers.go`.

## Envelope

Every Kafka message is a CloudEvents 1.0 event in structured JSON mode.

| Attribute | Value for this context |
| --- | --- |
| `specversion` | `1.0` |
| `id` | a UUID minted once by the encoder and stored in the outbox row, so a redelivery carries the same `id` |
| `source` | `/retailnetwork/order-sourcing` |
| `type` | `com.retailnetwork.commerce.order-sourcing.assignment.<EventName>` |
| `subject` | the `assignmentId` |
| `time` | when the transition happened, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:retailnetwork:order-sourcing:<events\|analytics>:<EventName>:v1` |
| Kafka topic | `retailnetwork.order-sourcing.events`, or `retailnetwork.order-sourcing.analytics` for the two analytics-only events |
| Kafka key | the `assignmentId`, so one assignment's events stay in order on one partition |

Every event leaves through the transactional outbox, written in the same transaction as the aggregate.

## Published

| Event | Topic | Producing command | Consumers (read on their `origin/develop`) |
| --- | --- | --- | --- |
| `FulfillmentQueued` | analytics | `QueueAssignment` | none known |
| `FulfillmentReleasedToNode` | events | `ReleaseToNode`, `QueueAssignment` for `SAME_DAY` | `vendor-connectivity`, `linehaul-transportation` |
| `FulfillmentReassigned` | events | `ReoptimiseAssignment`, `ReleaseToNode`, `HandleNodeRefusal`, `ApplyRevisedPromise` (node changed) | `linehaul-transportation` |
| `FulfillmentLocked` | events | `LockAssignment` | `delivery-promise`, `customer-ordering`, `linehaul-transportation` |
| `FulfillmentShipped` | events | `MarkShipped` | `delivery-promise`, `customer-ordering` |
| `FulfillmentDelivered` | analytics | `MarkDelivered` (last package) | none known |
| `FulfillmentUnsourceable` | events | `QueueAssignment`, `ReoptimiseAssignment`, `ReleaseToNode`, `HandleNodeRefusal`, `ApplyRevisedPromise` | `delivery-promise`, `customer-ordering` |
| `FulfillmentCancelled` | events | `CancelAssignment` | `vendor-connectivity`, `linehaul-transportation`, `customer-ordering` |
| `FulfillmentCancelRefused` | events | `CancelAssignment` (after the lock) | `customer-ordering` |
| `FulfillmentConstraintRevised` | events | `ApplyRevisedPromise` | none found |

The consumer column is from the handler files named on [Integration](/docs/ecosystem/integration).

### FulfillmentQueued

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId` | string | The new assignment |
| `orderId` | string | The customer order |
| `groupNo` | integer | The shipment group within the order |
| `promiseId` | string | The promise this assignment must keep |
| `deliverByLatest` | date-time | The binding deadline |
| `lines` | array | `lineNo`, `catalogItemId`, `quantity`; no `listingId` yet because no node is chosen |

### FulfillmentReleasedToNode

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId` | string | The assignment |
| `releaseNo` | integer | 1 for the first release, plus 1 for each later one; with `assignmentId` it is the purchase order's idempotency key |
| `orderId`, `groupNo` | string, integer | The group |
| `nodeId` | string | The vendor node released to |
| `pickupDepartureId` | string | The pickup departure the group must make |
| `requiredShipBy` | date-time | That departure's cutoff at this node |
| `promisedDeliverByLatest` | date-time | The promise's `deliverByLatest` |
| `tier` | string | `SAME_DAY`, `NEXT_DAY`, `TWO_DAY` or `STANDARD` |
| `lines` | array | The whole group (fill-or-kill): `catalogItemId`, `listingId` of this node, `quantity` |

No ship-to data: the release carries no PII.

### FulfillmentReassigned

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId` | string | The assignment |
| `fromNodeId`, `toNodeId` | string | The node it leaves and the new candidate |
| `reason` | string | `REOPTIMISED`, `RELEASE_RECHECK`, `PURCHASE_ORDER_REJECTED`, `PURCHASE_ORDER_EXPIRED` or `PROMISE_REVISED` |
| `assignmentVersion` | integer | The version after the change, so consumers can drop stale copies |

### FulfillmentLocked

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId`, `orderId`, `groupNo` | string, string, integer | The group |
| `promiseId` | string | The promise |
| `nodeId` | string | The node that accepted |
| `pickupDepartureId` | string | The pickup departure the package will make |
| `lockedAt` | date-time | The acceptance time (falls back to the processing time) |
| `lockReason` | string | `PURCHASE_ORDER_ACCEPTED` |

### FulfillmentShipped

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId`, `orderId`, `groupNo` | string, string, integer | The group |
| `nodeId` | string | The shipping node |
| `trackingNumbers` | array of string | One per package, 1 to 100 |
| `shippedAt` | date-time | From `VendorShipmentConfirmed` |

### FulfillmentDelivered

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId`, `orderId`, `groupNo` | string, string, integer | The group |
| `deliveredAt` | date-time | From the last `PackageDelivered` |

### FulfillmentUnsourceable

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId`, `orderId`, `groupNo` | string, string, integer | The group |
| `promiseId` | string | The promise to revise |
| `excludedNodes` | array | `nodeId`, `reason` of every node that refused, so the re-promise can avoid them |
| `reason` | string | `NO_CANDIDATE` or `NODE_REFUSED` |

### FulfillmentCancelled and FulfillmentCancelRefused

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId`, `orderId`, `groupNo` | string, string, integer | The group |
| `reason` | string | From `CustomerOrderCancelled.reason` (default `UNSPECIFIED`); `CANCEL_AFTER_LOCK` for the refusal |

### FulfillmentConstraintRevised

| Field | Type | Meaning |
| --- | --- | --- |
| `assignmentId` | string | The assignment |
| `revisionNo` | integer | The applied promise revision |
| `deliverByLatest` | date-time | The new binding deadline |

### Example envelope

The most important event: the release that becomes a purchase order. The identifiers are illustrative (the same values as the
example in `apis/asyncapi.yaml`).

```json
{
  "specversion": "1.0",
  "id": "3f6c2a9e-8b1d-4c7a-9e55-0d2f6b7a1c66",
  "source": "/retailnetwork/order-sourcing",
  "type": "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode",
  "subject": "asg-7f3c2d10",
  "time": "2026-10-22T19:01:00Z",
  "datacontenttype": "application/json",
  "dataschema": "urn:retailnetwork:order-sourcing:events:FulfillmentReleasedToNode:v1",
  "data": {
    "assignmentId": "asg-7f3c2d10",
    "releaseNo": 1,
    "orderId": "ord-51a0c9",
    "groupNo": 1,
    "nodeId": "node-fc-ws-sor-01",
    "pickupDepartureId": "dep-pk-20261022-1800-fc-ws-sor-01",
    "requiredShipBy": "2026-10-22T21:00:00Z",
    "promisedDeliverByLatest": "2026-10-24T23:00:00Z",
    "tier": "TWO_DAY",
    "lines": [
      {"catalogItemId": "cat-000123", "listingId": "lst-000123-fc-ws-sor-01", "quantity": 2}
    ]
  }
}
```

## Consumed

Every consumer is idempotent on the CloudEvents `id` through the inbox table `processed_events`, written in the same transaction
as the change it causes, and dispatches on the **full** `type` string, never on a suffix. Domain-level idempotency comes on top,
per row below.

### Events that drive the aggregate

| Producer | Event | Fields used | What this context does | Domain idempotency |
| --- | --- | --- | --- | --- |
| `customer-ordering` | `CustomerOrderPlaced` | `orderId`, `destinationPostalPrefix`, `groups[]` | `QueueAssignment` per group; `SAME_DAY` groups are released at once | one assignment per `(orderId, groupNo)`; a repeat finds it and does nothing |
| `customer-ordering` | `CustomerOrderCancelled` | `orderId`, `groupNos`, `reason` | `CancelAssignment` for each listed group | an already `CANCELLED` assignment is left as is; a group never queued here is skipped |
| `delivery-promise` | `PromiseRevised` | `promiseId`, `revisionNo`, `deliverByLatest`, `nodeId` | `ApplyRevisedPromise` on the assignment of that promise | a `revisionNo` not above the applied one is ignored |
| `vendor-connectivity` | `PurchaseOrderAccepted` | `assignmentId`, `releaseNo`, `acceptedAt` | `LockAssignment` | only the pending `releaseNo` locks; a repeat for the locking release is a no-op |
| `vendor-connectivity` | `PurchaseOrderRejected` | `assignmentId`, `releaseNo`, `reasonCode` | `HandleNodeRefusal` with outcome `REJECTED` | only for the pending release; an older `releaseNo` is ignored |
| `vendor-connectivity` | `PurchaseOrderExpired` | `assignmentId`, `releaseNo` | `HandleNodeRefusal` with outcome `EXPIRED` | as above |
| `vendor-connectivity` | `VendorShipmentConfirmed` | `assignmentId`, `trackingNumbers`, `shippedAt` | `MarkShipped` | a repeat on `SHIPPED` or `DELIVERED` is a no-op |
| `shipping-and-tracking` | `PackageDelivered` | `assignmentId`, `trackingNumber`, `deliveredAt` | `MarkDelivered` | a repeat of a recorded package is a no-op; the last package delivers |
| `linehaul-transportation` | `BookingMoved` | `assignmentId`, `fromDepartureId`, `toDepartureId` | `RecordPickupMoved` on a `LOCKED` assignment | a move to the current departure, or from another one, changes nothing |
| `linehaul-transportation` | `BookingFailed` | `assignmentId` | recorded as processed only; the departure's own events drive re-optimisation | inbox |

### Events that feed read models

| Producer | Events | Read model | Idempotency |
| --- | --- | --- | --- |
| `delivery-promise` | `PromiseBookExhausted` | `PromiseBookPressureView` (`rm_book_pressure`) | upsert by `bookId` |
| `node-topology` | `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired` | `NodeProfileView` (`rm_nodes`) | upsert by `nodeId`; a lower `profileRevision` is ignored; a status with an older instant is ignored |
| `node-topology` | `LaneOpened`, `LaneClosed`, `ServiceAreaAssigned`, `RegionDefined` | `NetworkGraphView` | upsert by `laneId`, `postalPrefix` (newer `assignmentVersion` wins), `regionId` |
| `linehaul-transportation` | `DepartureScheduled`, `DepartureCapacityChanged`, `DepartureCancelled` | `NetworkGraphView` (`rm_departures`) | upsert by `departureId`; any arrival order converges |
| `last-mile-delivery` | `DeliveryWaveScheduled`, `DeliveryWaveCapacityChanged`, `DeliveryWaveCancelled` | `NetworkGraphView` (`rm_waves`) | upsert by `waveId` |
| `offer-availability` | `ListingCreated`, `ListingAvailabilityChanged`, `ListingSuppressed`, `ListingRestored`, `CatalogItemDiscontinued` | `AvailabilityView` | upsert by `listingId` / `catalogItemId`; an older `asOf` is ignored |
| `node-performance` | `NodeScorecardUpdated` | `ScorecardView` (`rm_scorecards`) | upsert by `(nodeId, window)`; an older `computedAt` is ignored |

A read-model change does not itself move an assignment. Queued assignments pick it up at the next re-optimisation sweep, and a
release re-checks the candidate against fresh read models.

Full types for every row are on the [event catalogue](/docs/api-reference/events#consumed).
