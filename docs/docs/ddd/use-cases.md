---
id: use-cases
title: Use cases
sidebar_label: Use cases
description: "Every application use case of Order Sourcing: trigger, inputs, invariants checked, events raised, and how a change commits."
---

# Use cases

The application layer is `internal/application/usecases`. Every command runs in **one** `ports.UnitOfWork` and has one
shape (`writer.go`): claim the CloudEvents id in the inbox (Kafka triggers only), load the aggregate, call exactly one domain
method, save with the optimistic-concurrency check and append the raised events to the outbox. The aggregate, its events and the
inbox row commit or roll back together. A `ports.ErrConcurrentUpdate` re-runs the whole attempt once on fresh state before the
conflict is reported (`transact`). Nothing here calls a sibling context synchronously: reads use the event-fed local read
models (`ports.ReadModels`, `ports.WorldReader`).

The aggregate is `FulfillmentAssignment` (package `internal/domain/assignment`, one per `(orderId, groupNo)`), with the states
`QUEUED`, `RELEASED`, `LOCKED`, `SHIPPED`, `DELIVERED`, `UNSOURCEABLE` and `CANCELLED`. There are **15 use cases**: 10 commands
on the aggregate, 1 inbox-only recorder, the queue sweep, the read-model projector and 2 queries.

Source: `internal/application/usecases/*.go`, `internal/domain/assignment/commands.go`,
`internal/adapters/inbound/kafka/handlers.go`.

## Triggers at a glance

Kafka types below are the CloudEvents `type` suffix; the full type is
`com.retailnetwork.<tier>.<producer>.<entity>.<Name>` (see [Integration](/docs/ecosystem/integration)).

| # | Use case (file) | Trigger | Inbound adapter |
| --- | --- | --- | --- |
| 1 | `QueueAssignment` (`queue.go`) | `CustomerOrderPlaced` | Kafka, `retailnetwork.customer-ordering.events` |
| 2 | `ReoptimiseAssignment` (`commands.go`) | `POST /assignments/{id}:reoptimise`; the queue sweep | REST; sweep worker in `cmd/api` |
| 3 | `ReleaseToNode` (`commands.go`) | `releaseAt` reached (the sweep) | sweep worker in `cmd/api` |
| 4 | `HandleNodeRefusal` (`commands.go`) | `PurchaseOrderRejected`, `PurchaseOrderExpired` | Kafka, `retailnetwork.vendor-connectivity.events` |
| 5 | `LockAssignment` (`commands.go`) | `PurchaseOrderAccepted` | Kafka, `retailnetwork.vendor-connectivity.events` |
| 6 | `MarkShipped` (`commands.go`) | `VendorShipmentConfirmed` | Kafka, `retailnetwork.vendor-connectivity.events` |
| 7 | `MarkDelivered` (`commands.go`) | `PackageDelivered` | Kafka, `retailnetwork.shipping-and-tracking.events` |
| 8 | `RecordPickupMoved` (`commands.go`) | `BookingMoved` | Kafka, `retailnetwork.linehaul-transportation.events` |
| 9 | `CancelAssignment` (`cancel_revise.go`) | `CustomerOrderCancelled` | Kafka, `retailnetwork.customer-ordering.events` |
| 10 | `ApplyRevisedPromise` (`cancel_revise.go`) | `PromiseRevised` | Kafka, `retailnetwork.delivery-promise.events` |
| 11 | `RecordProcessed` (`commands.go`) | `BookingFailed` | Kafka, `retailnetwork.linehaul-transportation.events` |
| 12 | `SweepQueue` (`sweep.go`) | timer, `SWEEP_INTERVAL` (default 15 s) | `startSweep` in `cmd/api/main.go` |
| 13 | `Projector` (`projector.go`) | 22 sibling event types, see below | Kafka, seven topics |
| 14 | `Queries.ListAssignments` (`queries.go`) | `GET /assignments`; MCP `list_assignments`; A2A `find_assignments` | REST, MCP, A2A |
| 15 | `Queries.GetAssignment` (`queries.go`) | `GET /assignments/{id}`; MCP `get_assignment`; A2A `assignment_detail` | REST, MCP, A2A |

## Use cases in detail

| # | Inputs | Invariants checked | Events raised |
| --- | --- | --- | --- |
| 1 | `orderId`, `destinationPostalPrefix`, `groups[]` (`groupNo`, `promiseId`, `tier`, `witnessNodeId`, `deliverByLatest`, `lines[]`) | Per group, in one transaction: a group already queued for `(orderId, groupNo)` is skipped, so a repeated order is a no-op. Ids, tier, line bounds (at most 200 lines) are validated by `assignment.Queue` (`shared.ErrValidation`). The best candidate comes from the pure search over a fresh `World` snapshot; its `eta` must not be after `deliverByLatest` (`ErrCandidateBreaksPromise`). **No candidate at all** creates the assignment directly in `UNSOURCEABLE`. A `SAME_DAY` group with a candidate is released at once, in the same transaction. | `FulfillmentQueued` (analytics topic only); `FulfillmentUnsourceable` when no candidate; for `SAME_DAY`, `FulfillmentReleasedToNode`. Adds one count to `sourcing.groups.sourced` per created group (`outcome` = the lower-case state), or one count with `outcome=failed` and a `reason` when the command fails. |
| 2 | assignment id; optional `assignmentVersion` in the REST body (0 or absent skips the check); `Idempotency-Key` header over REST | State must be `QUEUED`: `RELEASED` is `ErrReleaseInFlight`, `LOCKED`/`SHIPPED`/`DELIVERED` is `ErrAssignmentLocked`, the others `shared.ErrIllegalTransition`. A non-zero `assignmentVersion` must equal the current one (`ErrStaleAssignmentVersion`). The candidate must not be an excluded node and not break the promise. An empty candidate set moves the assignment to `UNSOURCEABLE`. If the search returns the same candidate and the same release point, nothing is written and no event is raised (the version does not move). | `FulfillmentReassigned` when the node changes (`reason` `REOPTIMISED`); `FulfillmentUnsourceable` (`NO_CANDIDATE`); none when unchanged |
| 3 | assignment id | A no-op unless the assignment is `QUEUED` (a second timer firing finds `RELEASED` and stops). The candidate is re-searched against fresh read models; the lines are resolved to the node's `listingId`s and must match the group's lines exactly. No candidate left: `UNSOURCEABLE`. Otherwise a `Release` is appended (`releaseNo` = previous + 1, `requiredShipBy` = the pickup cutoff, outcome `PENDING`) and the state is `RELEASED`. | `FulfillmentReleasedToNode`; `FulfillmentReassigned` if the re-check picked another node (`RELEASE_RECHECK`); `FulfillmentUnsourceable` |
| 4 | `assignmentId`, `releaseNo`, outcome (`REJECTED` or `EXPIRED`), `reasonCode` (trimmed, at most 200 printable characters) | Refused with `ErrAssignmentLocked` once `LOCKED` or later. The release must be the pending last one (`ErrReleaseNotPending` otherwise: a late outcome of an old release). The refusing node is added to the exclusion list (`REJECTED:<reasonCode>` or `EXPIRED`) and the search is re-run without excluded nodes. A candidate that meets `deliverByLatest`: state back to `QUEUED` with a new `releaseAt`. None: `UNSOURCEABLE`. | `FulfillmentReassigned` (`PURCHASE_ORDER_REJECTED` or `PURCHASE_ORDER_EXPIRED`); `FulfillmentUnsourceable` (`NODE_REFUSED`) |
| 5 | `assignmentId`, `releaseNo`, `acceptedAt` | The release must be the pending one. A repeat for the release that already locked the assignment returns no events. A different release on a committed assignment is `ErrAssignmentLocked`; a `CANCELLED` one is `ErrIllegalTransition`. Sets `LOCKED`, `lockedAt` (falls back to now) and the pickup departure. | `FulfillmentLocked` (`lockReason` `PURCHASE_ORDER_ACCEPTED`) |
| 6 | `assignmentId`, `trackingNumbers` (1 to 100, each at most 64 printable characters), `shippedAt` | State must be `LOCKED` (`ErrIllegalTransition` otherwise); a repeat on `SHIPPED`/`DELIVERED` is a no-op. | `FulfillmentShipped` |
| 7 | `assignmentId`, `trackingNumber`, `deliveredAt` | State must be `SHIPPED`; the tracking number must be on the shipment (`shared.ErrValidation`); a repeat is a no-op. The group becomes `DELIVERED` only when its **last** package is delivered; earlier packages only save progress. | `FulfillmentDelivered` (analytics topic only), on the last package |
| 8 | `assignmentId`, `fromDepartureId`, `toDepartureId` | State must be `LOCKED`. A move to the current departure, or from a departure that is not the current one, changes nothing. The node never changes. | none (the version moves) |
| 9 | `orderId`, `groupNos[]`, optional `reason` (default `UNSPECIFIED`) | Per group, in one transaction: a group this service never queued is skipped. `QUEUED`, `RELEASED` and `UNSOURCEABLE` cancel; a pending release stays as it is (vendor-connectivity cancels the purchase order). `LOCKED` and later is `ErrCancelAfterLock`, which the use case turns into a refusal: the state stays, the version moves. A repeat on `CANCELLED` is a no-op. | `FulfillmentCancelled`; `FulfillmentCancelRefused` (`CANCEL_AFTER_LOCK`) |
| 10 | `promiseId`, `revisionNo`, `deliverByLatest`, `nodeId` (becomes the witness when not empty) | Looked up by `promiseId` (`FindByPromise`). `deliverByLatest` is required. A revision not above the applied `revisionNo` is `ErrStaleRevision` and is ignored. `DELIVERED` and `CANCELLED` are `ErrIllegalTransition`. In every other state the revision is applied and the exclusion list is reset; only in `QUEUED` and `UNSOURCEABLE` is the search re-run under the new constraint (an `UNSOURCEABLE` group with a candidate goes back to `QUEUED`; a `QUEUED` one with none becomes `UNSOURCEABLE`). | `FulfillmentConstraintRevised`; then `FulfillmentReassigned` (`PROMISE_REVISED`) or `FulfillmentUnsourceable` where the search changes the outcome |
| 11 | CloudEvents id and type | Only claims the inbox row: `BookingFailed` needs no state change because the departure's own cancellation or capacity events drive re-optimisation. | none |
| 12 | `after` cursor, `limit` (page of 100 in `cmd/api`) | One pass: up to `limit` due releases (`QUEUED` with `releaseAt <= now`), then up to `limit` re-optimisations after the cursor. Rows another writer changed (`ErrConcurrentUpdate`), rows gone (`ErrNotFound`), and rows that moved on (`ErrAssignmentLocked`, `ErrReleaseInFlight`) count as skipped; other errors are collected, logged and the tick continues. | those of use cases 2 and 3 |
| 13 | one of the 22 projected event types | Each method is one unit of work: inbox claim plus one row change. Newer revision wins (`profileRevision`, `assignmentVersion`), older instants are ignored (`asOf`, `computedAt`), any arrival order converges. No domain rule beyond that, so no event is raised. | none |
| 14 | optional `state`, `nodeId`, `orderId`, `limit` | `state` must be one of the seven states; `limit` 1 to 500 (default 100), ordered by id ascending; anything else is `shared.ErrValidation` (422 over REST). | none |
| 15 | assignment id | Unknown id is `ports.ErrNotFound` (404 over REST). | none |

Which events go to which topic is on [Domain events](/docs/ddd/domain-events) and the
[event catalogue](/docs/api-reference/events). Eight of the ten events are on `retailnetwork.order-sourcing.events`;
`FulfillmentQueued` and `FulfillmentDelivered` are analytics-only (`Stream()` in `internal/domain/assignment/events.go`).

## The projected events (use case 13)

| Topic | Event types projected |
| --- | --- |
| `retailnetwork.node-topology.events` | `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired`, `LaneOpened`, `LaneClosed`, `ServiceAreaAssigned`, `RegionDefined` |
| `retailnetwork.linehaul-transportation.events` | `DepartureScheduled`, `DepartureCapacityChanged`, `DepartureCancelled` |
| `retailnetwork.last-mile-delivery.events` | `DeliveryWaveScheduled`, `DeliveryWaveCapacityChanged`, `DeliveryWaveCancelled` |
| `retailnetwork.offer-availability.events` | `ListingCreated`, `ListingAvailabilityChanged`, `ListingSuppressed`, `ListingRestored`, `CatalogItemDiscontinued` |
| `retailnetwork.node-performance.events` | `NodeScorecardUpdated` |
| `retailnetwork.delivery-promise.events` | `PromiseBookExhausted` |

Nine consumed topics carry 32 handled event types: 10 reach a command use case (rows 1, 4 to 11 above) and 22 are projected.

## How a candidate is chosen

`internal/domain/sourcing.Search` is a pure function of the `World` snapshot and the request:

- A node qualifies when it is eligible (an `ACTIVE` vendor-operated fulfilment centre with a declared profile), not excluded, ships every line in full (listing present and not suppressed, item not discontinued, quantity available) and, for the `SAME_DAY` tier, is a `SUB_SAME_DAY` or `MICRO` node.
- A path starts at a usable departure of the node whose pickup cutoff is not before `now + handlingMinutes`, follows at most
  five departures (none for `SAME_DAY`), respects the sort dwell at intermediate nodes, and ends at a delivery wave of the
  destination's service-area station whose induct cutoff is not before the last arrival and whose window ends no later than
  `deliverByLatest`. The candidate `eta` is that window end.
- Cost: 100 per onward leg, 10 per hour between departure and wave start, 1000 for a node outside the destination's region,
  a scorecard penalty on the 30-day window (A 0, B 50, C 200, D 500; no scorecard or `PROVISIONAL` adds nothing), and 500 when
  the promise book is under pressure for that node and pickup departure.
- Order: lowest cost, then the witness node, then the lower node id, then the lower pickup departure id.
- `releaseAt`: now for `SAME_DAY`; otherwise the pickup cutoff minus the node's handling time minus a 30 minute acknowledgement
  budget, never before now.

Source: `internal/domain/sourcing/sourcing.go` (`Search`, `option`, `penalties`, `explore`, `finish`).

## Errors and the problem types they become

The REST adapter maps each error in `internal/adapters/inbound/http/errors.go` (first match wins); full symptoms and remedies are
in [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types). Problem `type` URIs are
`https://errors.order-sourcing.retail-network.example/<slug>`.

| Error | Status | Problem slug |
| --- | --- | --- |
| `assignment.ErrAssignmentLocked` | 409 | `assignment-locked` |
| `assignment.ErrReleaseInFlight` | 409 | `release-in-flight` |
| `assignment.ErrNodeExcluded` | 409 | `node-excluded` |
| `assignment.ErrStaleAssignmentVersion` | 409 | `stale-assignment-version` |
| `assignment.ErrCandidateBreaksPromise` | 422 | `candidate-breaks-promise` |
| `ports.ErrNotFound` | 404 | `not-found` |
| `ports.ErrConcurrentUpdate` | 409 | `concurrent-update` |
| `shared.ErrIllegalTransition` | 409 | `illegal-transition` |
| `shared.ErrValidation` | 422 | `validation-failed` |
| malformed body or query | 400 | `malformed-request` |
| missing `Idempotency-Key` on the reoptimise route | 400 | `idempotency-key-required` |
| anything else | 500 | `internal-error` |

`assignment.ErrCancelAfterLock`, `ErrReleaseNotPending` and `ErrStaleRevision` have no REST route that can raise them; they
only occur on the Kafka path.

## What the consumers do with a failure

`Consumer.HandleMessage` (`internal/adapters/inbound/kafka/consumer.go`):

- Not a valid CloudEvent, or a type with no handler: logged or skipped, acknowledged.
- `errBadPayload` or `shared.ErrValidation`: the event can never be applied. With a DLQ configured it is dead-lettered at once
  to `<topic>.dlq`; otherwise it is skipped.
- `ports.ErrNotFound`, `ErrIllegalTransition`, `ErrAssignmentLocked`, `ErrReleaseNotPending`, `ErrStaleRevision`,
  `ErrStaleAssignmentVersion`: the aggregate has moved on, a retry cannot help. Logged at WARN and acknowledged; the unit of work
  rolled back, so no inbox row is written for that event id. (`ApplyRevisedPromise` swallows `ErrStaleRevision` itself and
  commits the inbox row.)
- Anything else (database down): returned, so the loop retries with backoff and eventually dead-letters.

## Where the code differs from the earlier design text

The aggregator's design pack (`warehouse-docs`, `network/contexts/order-sourcing`) was written before the build. The code
differs in these places, and the code is the reference:

- A group with no candidate is created `UNSOURCEABLE` at once (`assignment.Queue`, line 69), not queued.
- A promise revision is applied in every non-terminal state, not only from `UNSOURCEABLE`, and it raises
  `FulfillmentConstraintRevised` (`assignment.ApplyRevisedPromise`).
- `MarkDelivered` is per tracking number; the group delivers when the last package does.
- `BookingMoved` and `BookingFailed` are consumed (use cases 8 and 11).
- Reoptimisation of a non-`QUEUED` assignment reports the state's own error (409), not a single "not queued" error.
