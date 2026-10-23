# ADR 0003: consumed contracts, local read models and relationships

## Status

Accepted (2026-10-23).

## Context

order-sourcing consumes thirty-two event types from nine contexts and makes no
synchronous call (`sync_calls: []`). The model forbids a live lookup: sibling
data is an event-fed local read model.

## Decision

### Relationships

| Producer | Relationship | Consumed (full types in `apis/asyncapi.yaml`) |
| --- | --- | --- |
| customer-ordering | Customer/Supplier | `CustomerOrderPlaced`, `CustomerOrderCancelled` |
| delivery-promise | Customer/Supplier | `PromiseRevised`, `PromiseBookExhausted` |
| vendor-connectivity | Customer/Supplier | `PurchaseOrderAccepted`, `PurchaseOrderRejected`, `PurchaseOrderExpired`, `VendorShipmentConfirmed` |
| shipping-and-tracking | Conformist | `PackageDelivered` |
| node-topology | Conformist | `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired`, `LaneOpened`, `LaneClosed`, `ServiceAreaAssigned`, `RegionDefined` |
| offer-availability | Conformist | `ListingCreated`, `ListingAvailabilityChanged`, `ListingSuppressed`, `ListingRestored`, `CatalogItemDiscontinued` |
| linehaul-transportation | Conformist | `DepartureScheduled`, `DepartureCapacityChanged`, `DepartureCancelled`, `BookingMoved`, `BookingFailed` |
| last-mile-delivery | Conformist | `DeliveryWaveScheduled`, `DeliveryWaveCapacityChanged`, `DeliveryWaveCancelled` |
| node-performance | Conformist | `NodeScorecardUpdated` |

The planner (UI) is the REST client of the three routes.

### Local read models

Five projections, tables prefixed `rm_`, rebuildable by replaying the topics,
never reserving or deciding anything:

- `NodeProfileView` (`rm_nodes`): type, subtype, operator, region, status,
  handling time, sort dwell, profile revision. A profile with a lower
  `profileRevision` is ignored; a status event older than the stored status
  instant is ignored; `RETIRED` is final.
- `NetworkGraphView` (`rm_lanes`, `rm_departures`, `rm_waves`, `rm_service_areas`,
  `rm_regions`): upsert by id; `ServiceAreaAssigned` keeps the newer
  `assignmentVersion`; a capacity event for an unknown departure or wave
  creates a stub that the schedule event completes (events may arrive in any order).
- `AvailabilityView` (`rm_listings`, `rm_discontinued_items`): an availability
  with an older `asOf` is ignored.
- `ScorecardView` (`rm_scorecards`): by (`nodeId`, `window`); an older
  `computedAt` is ignored.
- `PromiseBookPressureView` (`rm_book_pressure`): by `bookId`.

A read-model change does not move an assignment; queued assignments pick it up
at the next sweep. The search reads one consistent snapshot per assignment
(`ports.NetworkReader`), restricted to the destination's station, the nodes
that could ship and the lines' items.

### Consumers

- One consumer per producer topic, each with a mandatory group variable
  (`CUSTOMER_ORDERING_CONSUMER_GROUP`, `DELIVERY_PROMISE_CONSUMER_GROUP`, ...,
  nine in all). A missing one is a boot error naming it.
- Idempotent on the CloudEvents `id`: the inbox row (`processed_events`) is
  written in the same transaction as the effect. Dispatch on the full type
  string; an unknown type of a consumed topic is ignored (offset committed).
- Tolerant decoding: unknown fields are ignored; a message that is not a valid
  CloudEvents 1.0 event, or whose required fields are missing, is deterministic
  poison: it goes to `<topic>.order-sourcing.dlq` once and is acknowledged.
  Retryable errors (database down, `ErrConcurrentUpdate` after the one retry)
  are not committed and the message is redelivered.
- Domain refusals a retry cannot fix (`ErrAssignmentLocked`, `ErrReleaseNotPending`,
  an event for an unknown assignment, ...) are logged, the inbox row is written
  and the message is acknowledged.

### Outbox

Every published event is encoded with the CloudEvents helper when the aggregate
is saved and stored in `outbox` in the same transaction. The always-on relay
reads in id order, publishes to the topic of the row's stream and marks it
sent; after `OUTBOX_MAX_ATTEMPTS` failures a row is excluded so later events
overtake it.

## Consequences

- Candidate quality depends on the read models having caught up (documented
  assumption of the model); an empty snapshot yields `UNSOURCEABLE`, which is
  the safe answer.
- Producers that have not published an AsyncAPI yet (all but node-topology)
  are consumed from the model's field lists and the wire conventions; the
  decoders are tolerant, so additive changes cost nothing.
