# Domain model: ubiquitous language, aggregates, events, use cases

Source of truth: retail-network-docs `model/model.yaml` (context `order-sourcing`). Invariants, transitions
and every assumption the model left open (A1-A23) are in
`docs/adr/0002-aggregates-invariants-and-assumptions.md`; the consumed contracts and local read models are in
`docs/adr/0003-consumed-contracts-and-local-read-models.md`.

## Ubiquitous Language (use these exact names)

- **FulfillmentAssignment** (`assignmentId`, `asg-<uuid>`) — the decision of which node and path will fulfil ONE
  shipment group of an order (`orderId` + `groupNo`). Created by `CustomerOrderPlaced`.
- **Candidate** — the current best node, path (`pickupDepartureId`, onward `legs`, `deliveryWaveId`), `eta` and
  `cost`. Re-computed until the release point.
- **VirtualQueue** — the set of `QUEUED` assignments waiting for their `releaseAt`, the latest moment the
  promise still allows a release (`internal/domain/sourcing`).
- **Release** — one send of the group to a node (`releaseNo`, outcome `PENDING`, `ACCEPTED`, `REJECTED`,
  `EXPIRED`). At most one is PENDING.
- **PromiseConstraint** — `promiseId`, `tier`, `deliverByLatest`, `witnessNodeId`, `revisionNo`. Node exclusions
  reset when `revisionNo` changes.
- **Excluded node** — a node that rejected or let a release expire; never released to again for the same
  revision.
- **AssignmentCost** — integer cost points (100 per departure, 10 per hour, cross-region penalty 1000,
  book-pressure 500, scorecard grade A 0 / B 50 / C 200 / D 500 from the 30-day window; `PROVISIONAL` adds none).
- **NetworkGraphView** — the local read models fed by sibling events (`internal/domain/network`): nodes, lanes,
  departures, waves, service areas, listings, scorecards, book pressure.

## Aggregates

- **FulfillmentAssignment** (`internal/domain/assignment`): states `QUEUED`, `RELEASED`, `LOCKED`, `SHIPPED`,
  `DELIVERED` (terminal), `UNSOURCEABLE`, `CANCELLED` (terminal). Invariants by their model names:
  `ErrCandidateBreaksPromise`, `ErrAssignmentLocked` (the node never changes after LOCKED),
  `ErrNodeExcluded`, `ErrReleaseInFlight`, `ErrStaleAssignmentVersion`, `ErrCancelAfterLock` (answered with
  `FulfillmentCancelRefused`), `ErrReleaseNotPending`, `ErrStaleRevision` (both ignored by consumers).
- **Candidate search** (`internal/domain/sourcing`): pure `Search`/`Best` over a `World` value; no I/O.
- **Network read models** (`internal/domain/network`): value types with last-write-wins rules, not aggregates.
- `internal/domain/shared`: `Event`, `Header`, `ErrValidation`, `ErrIllegalTransition`, `ErrInvalidSnapshot`.

The aggregate has `Snapshot()`, a validating `Restore(Snapshot)` and an optimistic `Version()`; `now` and ids are
passed in. `ports.ErrNotFound` and `ports.ErrConcurrentUpdate` are port errors, not domain errors. The domain
imports nothing outside `internal/domain`.

## Domain events (`internal/domain/assignment/events.go`)

Ten events, entity `assignment`, subject = `assignmentId`. On `retailnetwork.order-sourcing.events`:
`FulfillmentReleasedToNode`, `FulfillmentReassigned`, `FulfillmentLocked`, `FulfillmentShipped`,
`FulfillmentUnsourceable`, `FulfillmentCancelled`, `FulfillmentCancelRefused`, `FulfillmentConstraintRevised`.
On `.analytics` only: `FulfillmentQueued`, `FulfillmentDelivered`.

## Key use cases (`internal/application/usecases`)

One per command and query; a command runs in ONE `ports.UnitOfWork` (load, domain call, save, outbox append) and
retries once on `ports.ErrConcurrentUpdate`.

- `QueueAssignment` (`queue.go`), `ReoptimiseAssignment`, `ReleaseToNode`, `HandleNodeRefusal`, `LockAssignment`,
  `MarkShipped`, `MarkDelivered`, `RecordPickupMoved`, `RecordProcessed` (`commands.go`);
  `CancelAssignment`, `ApplyRevisedPromise` (`cancel_revise.go`); `SweepQueue` (`sweep.go`, the release timer).
- `Projector` (`projector.go`) maintains the read models from sibling events.
- Queries (`queries.go`): `ListAssignments`, `GetAssignment`. DTOs in `internal/application/views`, filters in
  `internal/application/repository`.
- Ports (`internal/application/ports/ports.go`) are interfaces only; the memory doubles in
  `internal/adapters/outbound/memory` are test doubles and pass the same suite as Postgres
  (`internal/adapters/outbound/repocontract`).
