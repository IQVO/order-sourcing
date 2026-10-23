# ADR 0002: the FulfillmentAssignment aggregate, its invariants and the assumptions the model left open

## Status

Accepted (2026-10-23).

## Context

The model (`contexts[order-sourcing]`) has one aggregate, `FulfillmentAssignment`
(root `assignmentId`), eight events, six invariants and nine commands. The Model
Gap Register decisions G5, G10, G12, G13, G14, G20 and G26 and contracts-delta
E1, E9 to E13 change or add to it. This ADR fixes what is built. Assumptions are
numbered and final unless the orchestrator overrules them.

## Decision

### Aggregate and packages

- `internal/domain/assignment`: the aggregate, states `QUEUED`, `RELEASED`,
  `LOCKED`, `SHIPPED`, `DELIVERED`, `UNSOURCEABLE`, `CANCELLED`; value objects
  `PromiseConstraint{promiseId, tier, deliverByLatest, witnessNodeId, revisionNo}`,
  `Candidate{nodeId, pickupDepartureId, pickupCutoffAt, legs[], deliveryWaveId, eta, cost}`,
  `Release{releaseNo, nodeId, releasedAt, requiredShipBy, outcome, reasonCode}`,
  `ExcludedNode{nodeId, reason}`, `Line{lineNo, catalogItemId, listingId, quantity}`.
- `internal/domain/network`: the pure value types and merge rules of the five
  read models (ADR 0003). `internal/domain/sourcing`: the pure candidate search,
  the `AssignmentCost` policy and the `VirtualQueue` release point.
- `internal/domain/shared`: `Event`, `Header`, `ErrValidation`,
  `ErrIllegalTransition`, `ErrInvalidSnapshot`, id checks.
- `ErrNotFound` and `ErrConcurrentUpdate` are the repository sentinels of
  `internal/application/ports`, not domain errors.

### Transitions as built

```text
[*] -> QUEUED                  QueueAssignment
QUEUED -> QUEUED               ReoptimiseAssignment (same or other node)
QUEUED -> RELEASED             ReleaseToNode
QUEUED -> UNSOURCEABLE         ReleaseToNode, ReoptimiseAssignment, QueueAssignment (empty candidate set, A4)
RELEASED -> QUEUED             HandleNodeRefusal (a candidate is left)
RELEASED -> UNSOURCEABLE       HandleNodeRefusal (none left)
RELEASED -> LOCKED             LockAssignment
LOCKED -> SHIPPED              MarkShipped
SHIPPED -> DELIVERED           MarkDelivered (last package)
QUEUED|RELEASED|UNSOURCEABLE -> CANCELLED   CancelAssignment (G10 adds UNSOURCEABLE)
UNSOURCEABLE -> QUEUED         ApplyRevisedPromise (G13)
any non-terminal -> same       ApplyRevisedPromise (constraint only, G13)
```

`DELIVERED` and `CANCELLED` are terminal.

### Invariants and their errors

| Rule | Error (problem slug) | Enforced by |
| --- | --- | --- |
| a released candidate's `eta <= deliverByLatest` | `ErrCandidateBreaksPromise` (`candidate-breaks-promise`, 422) | every method that sets a candidate |
| no change of node after LOCKED | `ErrAssignmentLocked` (`assignment-locked`, 409) | Reoptimise, Release, RefuseRelease on LOCKED, SHIPPED, DELIVERED |
| a refusing node is not released to again for the same promise revision | `ErrNodeExcluded` (`node-excluded`, 409) | every method that sets a candidate |
| at most one release PENDING | `ErrReleaseInFlight` (`release-in-flight`, 409) | Reoptimise and Release while RELEASED |
| `assignmentVersion` strictly increases | `ErrStaleAssignmentVersion` (`stale-assignment-version`, 409) | the optional caller version of Reoptimise; the repository check raises `ErrConcurrentUpdate` (A22) |
| cancel only before LOCKED | `ErrCancelAfterLock` (`cancel-after-lock`, 409) | Cancel on LOCKED, SHIPPED, DELIVERED |

The aggregate also raises `ErrReleaseNotPending` (a late outcome of an old
release; the consumer records it and moves on) and wraps `ErrValidation` and
`ErrIllegalTransition` of `shared`.

### Assumptions (model gaps)

- **A1** `assignmentId` is `asg-<uuid>` minted by the use case; a unique key on
  (`orderId`, `groupNo`) makes a repeated `CustomerOrderPlaced` a no-op.
- **A2** `FulfillmentQueued` and `FulfillmentDelivered` are "analytics only" in the
  model; by `conventions.analytics_rule` they go ONLY to
  `retailnetwork.order-sourcing.analytics`. The other eight go ONLY to `.events`.
- **A3** The aggregate keeps `destinationPostalPrefix` from `CustomerOrderPlaced`
  (G26). The prefix is matched EXACTLY to a `ServiceArea.postalPrefix`, as
  node-topology's A11 does.
- **A4** (G26) The candidate set empties: `QueueAssignment`, `ReoptimiseAssignment`,
  the release re-check and a refusal all move the assignment to `UNSOURCEABLE`
  at once and raise `FulfillmentUnsourceable`. At queueing the assignment is
  first saved `QUEUED` (`FulfillmentQueued`) and then `UNSOURCEABLE`, in one
  transaction. Reason `NO_CANDIDATE`, or `NODE_REFUSED` after a refusal.
- **A5** Re-optimising: `RELEASED` is `ErrReleaseInFlight`; `LOCKED`, `SHIPPED`,
  `DELIVERED` are `ErrAssignmentLocked`; `UNSOURCEABLE` and `CANCELLED` are
  `ErrIllegalTransition` (409 `illegal-transition`), the register's shared type.
  An unknown id is `not-found` (404), not `assignment-not-found`.
- **A6** (G10) `CancelAssignment` is also legal from `UNSOURCEABLE`; the trigger is
  `CustomerOrderCancelled` after `PromiseUnkeepable` reached customer-ordering.
- **A7** (G12) `CustomerOrderCancelled` is a request. From `QUEUED`, `RELEASED`
  or `UNSOURCEABLE` the result is `FulfillmentCancelled`. From `LOCKED`,
  `SHIPPED` or `DELIVERED` the aggregate keeps its state, bumps its version and
  raises `FulfillmentCancelRefused` (`CANCEL_AFTER_LOCK`). A repeated request on
  a `CANCELLED` assignment is a no-op. A `PurchaseOrderAccepted` that arrives
  for a `CANCELLED` assignment is recorded and logged, nothing else: the model
  does not say how to undo the vendor's acceptance (open, see model_gaps).
- **A8** A multi-package group is `DELIVERED` when the LAST of its
  `trackingNumbers` is delivered (contracts-delta E1); each `PackageDelivered`
  is remembered; `deliveredAt` is the last package's. A tracking number that is
  not on the shipment is `ErrValidation` (logged by the consumer).
- **A9** (G13) `ApplyRevisedPromise` applies in every non-terminal state when
  `revisionNo` is higher than the applied one (else ignored). It replaces
  `deliverByLatest` and `witnessNodeId` when present, resets the node
  exclusions, and ALWAYS raises `FulfillmentConstraintRevised`. In
  `UNSOURCEABLE` it re-queues when a candidate exists and otherwise stays
  `UNSOURCEABLE` silently (no second `FulfillmentUnsourceable`, delivery-promise
  answers with `PromiseUnkeepable`). In `QUEUED` it re-searches (a changed node
  raises `FulfillmentReassigned` with reason `PROMISE_REVISED`; an empty set is
  `UNSOURCEABLE`). In `RELEASED`, `LOCKED` and `SHIPPED` only the constraint
  changes: the node already committed.
- **A10** Exclusions are scoped to the promise revision (`revisionNo`, initially 0).
- **A11** Candidate search (`sourcing.Search`): a node is eligible when it is an
  `ACTIVE` `FULFILLMENT_CENTER` of operator `VENDOR` with a declared profile and
  every line in full (listing present, not suppressed, item not discontinued,
  `availableQty >= quantity`). The pickup is a non-cancelled, non-full departure
  originating at the node with `cutoffAt >= now + handlingTime`, on an OPEN or
  unknown lane. From the pickup, onward departures leave the arrival node with
  `cutoffAt >= arrival + sortDwell` (0 when undeclared), at most 5 departures
  per path, no node visited twice, until the destination's delivery station
  (from `ServiceAreaAssigned`) is reached; then the earliest non-cancelled wave
  with remaining stops and `inductCutoffAt >= arrival` gives `eta = windowEnd`.
  `SAME_DAY` needs subtype `SUB_SAME_DAY` or `MICRO` and a pickup that goes
  straight to the station (no legs). One candidate per (node, pickup departure),
  the cheapest path. `legs` are the departure ids after the pickup.
- **A12** `AssignmentCost` in integer cost points: 100 per departure after the
  pickup, plus 10 per hour from the pickup departure to the wave start
  (rounded down), plus 1000 `crossRegionPenalty` when the node's region differs
  from the destination's, plus the scorecard penalty (A15), plus 500
  `bookPressurePenalty` (A16). Ties: the witness node, then the lower
  `nodeId`, then the lower pickup departure id.
- **A13** `vendorAckBudget` is 30 minutes (the worked example of the model).
  `releaseAt = max(now, pickupCutoff - handlingTime - ackBudget)`; `SAME_DAY`
  releases immediately (`releaseAt = now`).
- **A14** Reason vocabularies are in ADR 0004. `ExcludedNode.reason` is
  `REJECTED:<reasonCode>` or `EXPIRED`; `Release.reasonCode` is the vendor's.
- **A15** (G20) Scorecard penalty from the `30d` window only: A 0, B 50, C 200,
  D 500; `PROVISIONAL` and a missing scorecard 0. The `7d` window is stored and
  ignored.
- **A16** The book-pressure view is binary: a `PromiseBookExhausted` for
  (`nodeId`, `pickupDepartureId`) adds the penalty to candidates that use
  exactly that node and pickup. Nothing clears it before the departure passes.
- **A17** (G14) `BookingMoved` points a `LOCKED` assignment's `pickupDepartureId`
  at the new departure (no event; the node never changes). `BookingFailed` is
  recorded and logged; the revision that delivery-promise issues is applied by A9.
- **A18** The release timer and the re-optimisation sweep run inside the service
  (`SWEEP_INTERVAL`, default 30 s; each pass handles a bounded batch of
  `QUEUED` rows ordered by `releaseAt`). Several instances may run: the version
  check turns a race into `ErrConcurrentUpdate`, the loser skips the row.
- **A19** Linehaul's `BookingMoved` and `BookingFailed` use entity `departure` in
  the type; the producer has not published its AsyncAPI yet. Re-check later.
- **A20** (G5) `DeliveryWaveCancelled` is an integration event of last-mile
  (`wave`), consumed into `NetworkGraphView`.
- **A21** Bounds: at most 200 lines per group, quantities 1 to 2147483647, ids
  1 to 64 characters, `groupNo` 1 to 2147483647 (plain literal constants).
- **A22** `ErrStaleAssignmentVersion` is a caller version mismatch (REST body
  `assignmentVersion`); `ErrConcurrentUpdate` is the repository's compare and
  swap failure. A Kafka-driven use case retries once on `ErrConcurrentUpdate` from
  a fresh load, then lets the consumer redeliver.
- **A23** `PromiseRevised.nodeId` is the new witness node and optional; absent
  keeps the witness.

## Consequences

- Every rule above has a table-driven test; `Restore` re-validates all of them.
- `model.yaml` is not edited; the orchestrator back-fills the model from the final report.
