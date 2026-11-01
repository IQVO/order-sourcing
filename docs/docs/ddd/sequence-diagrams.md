---
id: sequence-diagrams
title: Sequence diagrams
sidebar_label: Sequence diagrams
description: "UML sequence diagrams for the Order Sourcing command use cases as built: inbound adapter, use case, aggregate, repository, outbox and inbox in one transaction, relay and Kafka, with idempotency, version checks and error branches."
---

# Sequence diagrams

One diagram per command use case on [Use cases](/docs/ddd/use-cases). The participants:

| Participant | Component |
| --- | --- |
| adapter | `internal/adapters/inbound/kafka` consumer, `internal/adapters/inbound/http` handler, or the sweep worker in `cmd/api` |
| use case | `internal/application/usecases.<Command>` |
| aggregate | `assignment.Assignment` (`FulfillmentAssignment`) |
| repo + outbox | `internal/adapters/outbound/postgres`: the assignment repository, `outbox_events`, `processed_events` and `idempotency_keys`, all joined to **one** transaction by `UnitOfWork` |
| relay | `internal/adapters/outbound/outbox` with the Kafka sink |
| Kafka | `retailnetwork.order-sourcing.events` (or `.analytics`) |

Every command runs through `writer.mutate` or the equivalent in `usecases/writer.go`: claim the inbox row (Kafka triggers), load,
call one domain method, save, append the events to the outbox, commit. The save is an optimistic compare-and-swap on `version`;
a lost race is `ErrConcurrentUpdate`, and `transact` re-runs the whole attempt once on fresh state before the error escapes. A
command that changes nothing (the version did not move) writes nothing. Event names are short; the full type is
`com.retailnetwork.commerce.order-sourcing.assignment.<EventName>`. The relay half (outbox to Kafka) is the same in every diagram,
so it is drawn in full in diagram 1.

Source: `internal/application/usecases/*.go`, `internal/domain/assignment/commands.go`,
`internal/adapters/inbound/kafka/consumer.go`.

## 1. QueueAssignment

```mermaid
sequenceDiagram
    autonumber
    participant AD as kafka consumer
    participant UC as Queue<br/>Assignment
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    AD->>UC: CustomerOrderPlaced
    UC->>R: BEGIN, claim CloudEvents id in processed_events
    alt already processed
        R-->>UC: not first
        UC-->>AD: ack, no change
    else new
        loop each group
            UC->>R: find by orderId, groupNo
            alt assignment exists
                Note over UC: skip the group
            else none
                Note over UC: load World, search, best candidate and releaseAt
                UC->>A: Queue(constraint, lines, best, releaseAt)
                alt no candidate
                    A-->>UC: UNSOURCEABLE, FulfillmentQueued and FulfillmentUnsourceable
                else candidate
                    A-->>UC: QUEUED, version 1, FulfillmentQueued
                end
                UC->>R: insert assignment, append outbox rows
                opt SAME_DAY group with a candidate
                    UC->>A: Release(best, lines, now)
                    A-->>UC: RELEASED, FulfillmentReleasedToNode
                    UC->>R: update assignment, append outbox row
                end
            end
        end
        UC->>R: COMMIT
        UC-->>AD: ack
    end
    RL->>R: claim unpublished rows, FOR UPDATE SKIP LOCKED
    RL->>K: produce, key assignmentId
    RL->>R: mark published_at
```

Source: `usecases/queue.go` (`QueueAssignment.execute`, `queueGroup`, `releaseNow`), `domain/assignment/assignment.go` (`Queue`).
The unique key on `(order_id, group_no)` also rejects a racing duplicate insert (`ErrDuplicateGroup`, counted as a failed
outcome). All groups of one event commit in one transaction; a crash before `COMMIT` leaves nothing and the event is redelivered.

## 2. ReoptimiseAssignment

```mermaid
sequenceDiagram
    autonumber
    participant AD as http or sweep
    participant UC as Reoptimise<br/>Assignment
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    AD->>UC: reoptimise(assignmentId, assignmentVersion?)
    opt http with a known Idempotency-Key
        UC->>R: idempotency_keys lookup
        R-->>AD: replay stored response
    end
    UC->>R: BEGIN, load assignment
    alt not found
        UC-->>AD: 404 not-found
    else found
        alt state is not QUEUED
            UC->>A: Reoptimise(nil, expectedVersion)
            A-->>UC: ErrReleaseInFlight, ErrAssignmentLocked or ErrIllegalTransition
            UC-->>AD: 409 with that problem slug
        else QUEUED
            Note over UC: search without excluded nodes
            UC->>A: Reoptimise(best, releaseAt, expectedVersion)
            alt expectedVersion differs
                A-->>UC: ErrStaleAssignmentVersion
                UC-->>AD: 409 stale-assignment-version
            else no candidate
                A-->>UC: UNSOURCEABLE, FulfillmentUnsourceable
            else same candidate and release point
                A-->>UC: no events, version unchanged
                UC-->>AD: 200 assignment, nothing written
            else node or path changed
                A-->>UC: new candidate, releaseAt, version + 1
                UC->>R: update where version matches, outbox FulfillmentReassigned if the node changed
                UC-->>AD: 200 assignment
            end
        end
    end
    RL->>K: FulfillmentReassigned or FulfillmentUnsourceable
```

Source: `usecases/commands.go` (`ReoptimiseAssignment.Execute`), `domain/assignment/commands.go` (`Reoptimise`, `guardMovable`),
`inbound/http/server.go` (`handleReoptimise`), `inbound/http/idempotency.go`. The Idempotency-Key row is stored in the same
transaction as the request. The sweep calls the same use case with expected version 0 and skips a row that returns a lock,
in-flight or concurrent-update error.

## 3. The sweep and ReleaseToNode

```mermaid
sequenceDiagram
    autonumber
    participant SW as sweep worker
    participant UC as Release<br/>ToNode
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    SW->>R: Due(now, 100) QUEUED with releaseAt due
    R-->>SW: assignment ids
    loop each due id
        SW->>UC: Execute(id)
        UC->>R: BEGIN, load
        alt not QUEUED
            UC-->>SW: no-op, a second firing
        else QUEUED
            Note over UC: fresh World, search, resolve listings of the node
            alt no candidate left
                UC->>A: Release(nil)
                A-->>UC: UNSOURCEABLE, FulfillmentUnsourceable
            else candidate
                UC->>A: Release(best, lines, now)
                A-->>UC: RELEASED, releaseNo n, requiredShipBy
                UC->>R: update where version matches, outbox FulfillmentReleasedToNode
            end
            UC->>R: COMMIT
        end
    end
    SW->>R: Queued(after, 100) page of QUEUED ids
    loop each queued id
        SW->>UC: Reoptimise(id) as in diagram 2
    end
    RL->>K: FulfillmentReleasedToNode or FulfillmentUnsourceable
```

Source: `usecases/sweep.go` (`SweepQueue.Execute`), `usecases/commands.go` (`ReleaseToNode.Execute`), `cmd/api/main.go`
(`startSweep`, `sweepOnce`). One pass runs every `SWEEP_INTERVAL`. A row another writer changed, or that already moved on, is
counted as skipped. `vendor-connectivity` issues the purchase order idempotently on `assignmentId` + `releaseNo`, so a republished
event issues no second purchase order.

## 4. HandleNodeRefusal

```mermaid
sequenceDiagram
    autonumber
    participant AD as kafka consumer
    participant UC as HandleNode<br/>Refusal
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    AD->>UC: PurchaseOrderRejected or PurchaseOrderExpired
    UC->>R: BEGIN, claim CloudEvents id
    alt already processed
        UC-->>AD: ack
    else new
        UC->>R: load assignmentId
        Note over UC: search without the refusing node and the earlier exclusions
        UC->>A: RefuseRelease(releaseNo, outcome, reasonCode, best, releaseAt)
        alt state LOCKED, SHIPPED or DELIVERED
            A-->>UC: ErrAssignmentLocked
            UC-->>AD: rollback, warn, ack
        else releaseNo is not the pending release
            A-->>UC: ErrReleaseNotPending
            UC-->>AD: rollback, warn, ack
        else a candidate meets deliverByLatest
            A-->>UC: QUEUED, node excluded, version + 1
            UC->>R: update, outbox FulfillmentReassigned
        else none left
            A-->>UC: UNSOURCEABLE, excluded nodes listed
            UC->>R: update, outbox FulfillmentUnsourceable
        end
        UC->>R: COMMIT, inbox row with the change
        UC-->>AD: ack
    end
    RL->>K: FulfillmentReassigned or FulfillmentUnsourceable
```

Source: `usecases/commands.go` (`HandleNodeRefusal.Execute`), `domain/assignment/commands.go` (`RefuseRelease`),
`inbound/kafka/consumer.go` (`neverApplicable`). A re-queued assignment whose new `releaseAt` is already past is released by the
next sweep tick (diagram 3).

## 5. LockAssignment

```mermaid
sequenceDiagram
    autonumber
    participant AD as kafka consumer
    participant UC as Lock<br/>Assignment
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    AD->>UC: PurchaseOrderAccepted
    UC->>R: BEGIN, claim CloudEvents id, load assignmentId
    alt already processed
        UC-->>AD: ack
    else new
        UC->>A: Lock(releaseNo, acceptedAt)
        alt committed by the same release
            A-->>UC: no events
            UC->>R: COMMIT inbox row only
        else release is pending
            A-->>UC: LOCKED, version + 1
            UC->>R: update where version matches, outbox FulfillmentLocked
            UC->>R: COMMIT with inbox row
        else locked by another release
            A-->>UC: ErrAssignmentLocked
            UC-->>AD: rollback, warn, ack
        else CANCELLED
            A-->>UC: ErrIllegalTransition
            UC-->>AD: rollback, warn, ack
        end
    end
    RL->>K: FulfillmentLocked
```

Source: `usecases/commands.go` (`LockAssignment.Execute`), `domain/assignment/commands.go` (`Lock`). The `CANCELLED` branch is the
known residual "acceptance after cancellation": the acceptance is only logged and the vendor may still ship the group
([Bounded context canvas](/docs/ddd/bounded-context-canvas#open-questions)).

## 6. MarkShipped and MarkDelivered

```mermaid
sequenceDiagram
    autonumber
    participant AD as kafka consumer
    participant UC as MarkShipped<br/>MarkDelivered
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    AD->>UC: VendorShipmentConfirmed
    UC->>R: BEGIN, claim id, load
    UC->>A: MarkShipped(trackingNumbers, shippedAt)
    alt LOCKED
        A-->>UC: SHIPPED, version + 1
        UC->>R: update, outbox FulfillmentShipped, COMMIT
    else already SHIPPED or DELIVERED
        A-->>UC: no events
    else other state
        A-->>UC: ErrIllegalTransition, warn, ack
    end
    AD->>UC: PackageDelivered, one per package
    UC->>R: BEGIN, claim id, load
    UC->>A: MarkDelivered(trackingNumber, deliveredAt)
    alt tracking number not on the shipment
        A-->>UC: validation error, dead-lettered
    else package already recorded
        A-->>UC: no events
    else not the last package
        A-->>UC: progress saved, version + 1, no event
    else last package
        A-->>UC: DELIVERED, FulfillmentDelivered
        UC->>R: update, outbox row on the analytics topic, COMMIT
    end
    RL->>K: FulfillmentShipped, then FulfillmentDelivered on analytics
```

Source: `usecases/commands.go` (`MarkShipped`, `MarkDelivered`), `domain/assignment/commands.go`. `FulfillmentDelivered` goes only
to `retailnetwork.order-sourcing.analytics`.

## 7. CancelAssignment

```mermaid
sequenceDiagram
    autonumber
    participant AD as kafka consumer
    participant UC as Cancel<br/>Assignment
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    AD->>UC: CustomerOrderCancelled (orderId, groupNos, reason)
    UC->>R: BEGIN, claim CloudEvents id
    loop each groupNo
        UC->>R: find by orderId, groupNo
        alt never queued here
            Note over UC: skip
        else found
            UC->>A: Cancel(reason, now)
            alt QUEUED, RELEASED or UNSOURCEABLE
                A-->>UC: CANCELLED, version + 1
                UC->>R: update, outbox FulfillmentCancelled
            else already CANCELLED
                A-->>UC: no events
            else LOCKED, SHIPPED or DELIVERED
                A-->>UC: ErrCancelAfterLock
                UC->>A: RefuseCancel(now)
                A-->>UC: state unchanged, version + 1
                UC->>R: update, outbox FulfillmentCancelRefused
            end
        end
    end
    UC->>R: COMMIT
    RL->>K: FulfillmentCancelled or FulfillmentCancelRefused
```

Source: `usecases/cancel_revise.go` (`CancelAssignment.Execute`, `cancelOne`), `domain/assignment/commands.go` (`Cancel`,
`RefuseCancel`). All groups of one event are decided in one transaction. A pending release stays as it is: `vendor-connectivity`
cancels the purchase order on `FulfillmentCancelled`.

## 8. ApplyRevisedPromise

```mermaid
sequenceDiagram
    autonumber
    participant AD as kafka consumer
    participant UC as ApplyRevised<br/>Promise
    participant A as Fulfillment<br/>Assignment
    participant R as repo + outbox
    participant RL as relay
    participant K as Kafka
    AD->>UC: PromiseRevised (promiseId, revisionNo, deliverByLatest, nodeId)
    UC->>R: BEGIN, claim CloudEvents id, find by promiseId
    alt unknown promise
        UC-->>AD: not-found, warn, ack
    else found
        opt QUEUED or UNSOURCEABLE and revisionNo is higher
            Note over UC: search under the new constraint with no exclusions
        end
        UC->>A: ApplyRevisedPromise(revision, best, releaseAt)
        alt revisionNo not above the applied one
            A-->>UC: ErrStaleRevision
            UC->>R: COMMIT inbox row only
        else DELIVERED or CANCELLED
            A-->>UC: ErrIllegalTransition, warn, ack
        else applied
            A-->>UC: constraint revised, exclusions reset, version + 1
            UC->>R: update, outbox FulfillmentConstraintRevised
            opt state QUEUED and the node changed
                UC->>R: outbox FulfillmentReassigned (PROMISE_REVISED)
            end
            opt state QUEUED and no candidate
                UC->>R: outbox FulfillmentUnsourceable
            end
            opt state UNSOURCEABLE and a candidate exists
                Note over A: re-queued, no further event
            end
        end
    end
    RL->>K: FulfillmentConstraintRevised and the events above
```

Source: `usecases/cancel_revise.go` (`ApplyRevisedPromise.Execute`, `apply`), `domain/assignment/commands.go`
(`ApplyRevisedPromise`, `reapply`). In `RELEASED`, `LOCKED` and `SHIPPED` only the constraint changes: the node already
committed. A re-queued assignment is released by diagram 3.
