---
id: events
title: Event catalogue
sidebar_label: Event catalogue
description: "Every Kafka event Order Sourcing publishes (ten, entity assignment) and consumes (32 types from nine topics): types, topics, data fields, producers and consumers."
---

# Event catalogue

Every Kafka message this service produces or consumes is a CloudEvents 1.0 event in **structured mode**: the Kafka value is the
whole JSON envelope and the header `content-type` is `application/cloudevents+json; charset=UTF-8`. Envelopes are built and
decoded only in `internal/adapters/kafka/cloudevents` ([ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue)). The
contract is `apis/asyncapi.yaml` (AsyncAPI 3.0.0, version 1.0.0); the same events with their producing command are on
[Domain events](/docs/ddd/domain-events), and the topology of the edges is on [Integration](/docs/ecosystem/integration).

## Envelope

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` |
| `id` | a UUID minted once by the encoder and stored with the outbox row, so a relay retry republishes the same `id` |
| `source` | `/retailnetwork/order-sourcing` |
| `type` | `com.retailnetwork.commerce.order-sourcing.assignment.<EventName>` (tier `commerce`, entity `assignment` for every event) |
| `subject` and Kafka key | the `assignmentId`, so one assignment's events stay in order on one partition |
| `time` | the instant of the transition, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:retailnetwork:order-sourcing:<events\|analytics>:<EventName>:v1` |

Source: `internal/adapters/kafka/cloudevents/cloudevents.go` (`New`, `Type`, `DataSchema`),
`internal/adapters/outbound/kafka/encoder.go` (`Encoder.Encode`). `data` is camelCase JSON, timestamps are RFC 3339 UTC, arrays
are always present. A breaking payload change would be a new `.v2` type; every schema version today is 1.

## Published

Ten events, written through the transactional outbox in the same transaction as the aggregate and drained by the always-on
relay in `cmd/api` (at least once). Eight go to `retailnetwork.order-sourcing.events`; the two analytics-only events go **only**
to `retailnetwork.order-sourcing.analytics`.

| Event (`type` suffix after `assignment.`) | Topic | Raised by | `data` |
| --- | --- | --- | --- |
| `FulfillmentQueued` | analytics | `QueueAssignment` | `assignmentId`, `orderId`, `groupNo`, `promiseId`, `deliverByLatest`, `lines[{lineNo, catalogItemId, quantity}]` |
| `FulfillmentReleasedToNode` | events | `ReleaseToNode`, `QueueAssignment` (`SAME_DAY`) | `assignmentId`, `releaseNo`, `orderId`, `groupNo`, `nodeId`, `pickupDepartureId`, `requiredShipBy`, `promisedDeliverByLatest`, `tier`, `lines[{catalogItemId, listingId, quantity}]` |
| `FulfillmentReassigned` | events | `ReoptimiseAssignment`, `ReleaseToNode`, `HandleNodeRefusal`, `ApplyRevisedPromise` (node changed) | `assignmentId`, `fromNodeId`, `toNodeId`, `reason`, `assignmentVersion` |
| `FulfillmentLocked` | events | `LockAssignment` | `assignmentId`, `orderId`, `groupNo`, `promiseId`, `nodeId`, `pickupDepartureId`, `lockedAt`, `lockReason` |
| `FulfillmentShipped` | events | `MarkShipped` | `assignmentId`, `orderId`, `groupNo`, `nodeId`, `trackingNumbers`, `shippedAt` |
| `FulfillmentDelivered` | analytics | `MarkDelivered` (last package) | `assignmentId`, `orderId`, `groupNo`, `deliveredAt` |
| `FulfillmentUnsourceable` | events | `QueueAssignment`, `ReoptimiseAssignment`, `ReleaseToNode`, `HandleNodeRefusal`, `ApplyRevisedPromise` | `assignmentId`, `orderId`, `groupNo`, `promiseId`, `excludedNodes[{nodeId, reason}]`, `reason` |
| `FulfillmentCancelled` | events | `CancelAssignment` | `assignmentId`, `orderId`, `groupNo`, `reason` |
| `FulfillmentCancelRefused` | events | `CancelAssignment` (after the lock) | `assignmentId`, `orderId`, `groupNo`, `reason` (`CANCEL_AFTER_LOCK`) |
| `FulfillmentConstraintRevised` | events | `ApplyRevisedPromise` | `assignmentId`, `revisionNo`, `deliverByLatest` |

Reason vocabularies in the code (`internal/domain/assignment/types.go`): `FulfillmentReassigned.reason` is `REOPTIMISED`,
`RELEASE_RECHECK`, `PURCHASE_ORDER_REJECTED`, `PURCHASE_ORDER_EXPIRED` or `PROMISE_REVISED`; `FulfillmentUnsourceable.reason` is
`NO_CANDIDATE` or `NODE_REFUSED`; `FulfillmentLocked.lockReason` is `PURCHASE_ORDER_ACCEPTED`. Each excluded node's `reason` is
`REJECTED`, `REJECTED:<reasonCode>` or `EXPIRED`.

## Consumed

Nine topics, 32 event types, each under its own consumer group read from the environment (`<PRODUCER>_CONSUMER_GROUP`, see
[Configuration](/docs/operations/configuration)). Dispatch is on the **full** `type`; every other type on those topics is
ignored. The **Effect** column names the use case; the rest feed the five local read models.

| Topic | `type` (after `com.retailnetwork.`) | Effect |
| --- | --- | --- |
| `retailnetwork.customer-ordering.events` | `commerce.customer-ordering.customerorder.CustomerOrderPlaced` | `QueueAssignment` |
| | `commerce.customer-ordering.customerorder.CustomerOrderCancelled` | `CancelAssignment` |
| `retailnetwork.delivery-promise.events` | `commerce.delivery-promise.promise.PromiseRevised` | `ApplyRevisedPromise` |
| | `commerce.delivery-promise.promisebook.PromiseBookExhausted` | book pressure (`rm_book_pressure`) |
| `retailnetwork.vendor-connectivity.events` | `partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted` | `LockAssignment` |
| | `partner.vendor-connectivity.purchaseorder.PurchaseOrderRejected` | `HandleNodeRefusal` (`REJECTED`) |
| | `partner.vendor-connectivity.purchaseorder.PurchaseOrderExpired` | `HandleNodeRefusal` (`EXPIRED`) |
| | `partner.vendor-connectivity.purchaseorder.VendorShipmentConfirmed` | `MarkShipped` |
| `retailnetwork.shipping-and-tracking.events` | `logistics.shipping-and-tracking.journey.PackageDelivered` | `MarkDelivered` |
| `retailnetwork.linehaul-transportation.events` | `logistics.linehaul-transportation.departure.BookingMoved` | `RecordPickupMoved` |
| | `logistics.linehaul-transportation.departure.BookingFailed` | `RecordProcessed` (inbox only) |
| | `logistics.linehaul-transportation.departure.DepartureScheduled`, `DepartureCapacityChanged`, `DepartureCancelled` | departures (`rm_departures`) |
| `retailnetwork.last-mile-delivery.events` | `logistics.last-mile-delivery.wave.DeliveryWaveScheduled`, `DeliveryWaveCapacityChanged`, `DeliveryWaveCancelled` | waves (`rm_waves`) |
| `retailnetwork.node-topology.events` | `logistics.node-topology.node.NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired` | nodes (`rm_nodes`) |
| | `logistics.node-topology.lane.LaneOpened`, `LaneClosed` | lanes (`rm_lanes`) |
| | `logistics.node-topology.servicearea.ServiceAreaAssigned` | service areas (`rm_service_areas`) |
| | `logistics.node-topology.region.RegionDefined` | regions (`rm_regions`) |
| `retailnetwork.offer-availability.events` | `commerce.offer-availability.listing.ListingCreated`, `ListingAvailabilityChanged`, `ListingSuppressed`, `ListingRestored` | listings (`rm_listings`) |
| | `commerce.offer-availability.catalogitem.CatalogItemDiscontinued` | `rm_discontinued_items` |
| `retailnetwork.node-performance.events` | `partner.node-performance.scorecard.NodeScorecardUpdated` | scorecards (`rm_scorecards`) |

Source: `internal/adapters/inbound/kafka/handlers.go` (`Handlers.Topics` and one method per topic).

### Fields read from each consumed event

| Event | `data` fields used |
| --- | --- |
| `CustomerOrderPlaced` | `orderId`, `destinationPostalPrefix`, `groups[{groupNo, promiseId, tier, witnessNodeId, deliverByLatest, lines[{lineNo, catalogItemId, quantity}]}]` |
| `CustomerOrderCancelled` | `orderId`, `groupNos`, `reason` |
| `PromiseRevised` | `promiseId`, `revisionNo`, `deliverByLatest`, `nodeId` (the new witness) |
| `PromiseBookExhausted` | `bookId`, `nodeId`, `pickupDepartureId` |
| `PurchaseOrderAccepted` | `assignmentId`, `releaseNo`, `acceptedAt` |
| `PurchaseOrderRejected`, `PurchaseOrderExpired` | `assignmentId`, `releaseNo`, `reasonCode` (rejected only, trimmed) |
| `VendorShipmentConfirmed` | `assignmentId`, `trackingNumbers`, `shippedAt` |
| `PackageDelivered` | `assignmentId`, `trackingNumber`, `deliveredAt` |
| `BookingMoved` | `assignmentId`, `fromDepartureId`, `toDepartureId` |
| `BookingFailed` | `assignmentId` (only recorded as processed) |
| `DepartureScheduled` | `departureId`, `laneId`, `kind`, `originNodeId`, `destinationNodeId`, `cutoffAt`, `departsAt`, `arrivesAt`, `capacityPackages` |
| `DepartureCapacityChanged` | `departureId`, `remainingPackages` |
| `DepartureCancelled` | `departureId` |
| `DeliveryWaveScheduled` | `waveId`, `stationNodeId`, `inductCutoffAt`, `windowStart`, `windowEnd`, `stopCapacity` |
| `DeliveryWaveCapacityChanged` | `waveId`, `remainingStops` |
| `DeliveryWaveCancelled` | `waveId` |
| `NodeRegistered` | `nodeId`, `nodeType`, `subtype`, `operator`, `regionId` |
| `NodeOperationalProfileDeclared` | `nodeId`, `profileRevision`, `handlingTimeMinutes` |
| `NodeActivated`, `NodeSuspended`, `NodeRetired` | `nodeId` and `activatedAt` / `suspendedAt` / `retiredAt` (falls back to the event `time`) |
| `LaneOpened` | `laneId`, `originNodeId`, `destinationNodeId`, `mode` |
| `LaneClosed` | `laneId` |
| `ServiceAreaAssigned` | `postalPrefix`, `deliveryStationNodeId`, `regionId`, `assignmentVersion` |
| `RegionDefined` | `regionId`, `code`, `name` |
| `ListingCreated` | `listingId`, `catalogItemId`, `nodeId` |
| `ListingAvailabilityChanged` | `listingId`, `catalogItemId`, `nodeId`, `availableQty`, `asOf` |
| `ListingSuppressed`, `ListingRestored` | `listingId` |
| `CatalogItemDiscontinued` | `catalogItemId` |
| `NodeScorecardUpdated` | `nodeId`, `window`, `grade`, `computedAt` |

## Delivery guarantees

- **Publishing** is at least once through the outbox; the CloudEvents `id` is stable across retries, so consumers dedupe on it.
  The relay stops retrying a row after `OUTBOX_MAX_ATTEMPTS` and leaves it for an operator
  ([Runbook](/docs/operations/runbook#outbox-relay)).
- **Consuming** is idempotent on the CloudEvents `id` through `processed_events`, written in the same transaction as the change.
  Failures retry with backoff; a payload that can never be applied is published to `<topic>.dlq`
  ([Runbook](/docs/operations/runbook#dead-letters)). A message that is not a valid CloudEvents 1.0 event, including the retired
  flat envelope, is logged and skipped.
- **Ordering** per assignment holds on the produced topics because the Kafka key is the `assignmentId`. Consumed topics are
  read per partition; the read-model upserts converge in any arrival order (newer revision or instant wins).
- **Topics** are never created by this service through an admin API; the broker auto-creates each on first write.
