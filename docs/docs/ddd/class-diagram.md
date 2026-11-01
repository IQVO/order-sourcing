---
id: class-diagram
title: Class diagram
sidebar_label: Class diagram
description: "UML class diagrams of Order Sourcing as built: the FulfillmentAssignment aggregate with its value objects and enumerations, the ten domain events, the search and read-model types, and the hexagonal ports and adapters."
---

# Class diagram

Go types of `internal/domain` and `internal/application` as they are on `develop`. Fields are unexported in Go and shown with
`-`; methods are shown with `+`. Names are the code's; Go signatures are abbreviated.

## 1. FulfillmentAssignment aggregate

The root, its state and its repository first; then its value objects and enumerations.

```mermaid
classDiagram
    direction TB
    class Assignment {
        <<AggregateRoot>>
        -id ID
        -orderId string
        -groupNo int
        -destination string
        -state State
        -version int64
        -promise PromiseConstraint
        -lines Line[]
        -candidate Candidate
        -releaseAt time.Time
        -releases Release[]
        -excluded ExcludedNode[]
        -lockedAt time.Time
        -lockReason string
        -pickupDeparture string
        -trackingNumbers string[]
        -shippedAt time.Time
        -delivered DeliveredPackage[]
        -deliveredAt time.Time
        -cancelReason string
        +Queue(in, best, releaseAt, now)$ Assignment
        +Reoptimise(best, releaseAt, expectedVersion, now) events
        +Release(best, lines, now) events
        +RefuseRelease(no, outcome, reasonCode, best, releaseAt, now) events
        +Lock(no, acceptedAt, now) events
        +MarkShipped(trackingNumbers, shippedAt, now) events
        +MarkDelivered(trackingNumber, deliveredAt, now) events
        +Cancel(reason, now) events
        +RefuseCancel(now) events
        +ApplyRevisedPromise(rev, best, releaseAt, now) events
        +MovePickup(from, to, now) error
        +Snapshot() Snapshot
    }
    class State {
        <<Enumeration>>
        QUEUED
        RELEASED
        LOCKED
        SHIPPED
        DELIVERED
        UNSOURCEABLE
        CANCELLED
    }
    class AssignmentRepository {
        <<Port>>
        +Get(ctx, id)
        +FindByGroup(ctx, orderId, groupNo)
        +FindByPromise(ctx, promiseId)
        +List(ctx, filter)
        +Due(ctx, now, limit)
        +Queued(ctx, after, limit)
        +Save(ctx, a) error
    }
    Assignment --> State : state
    AssignmentRepository ..> Assignment : persists
```

```mermaid
classDiagram
    direction LR
    class Assignment {
        <<AggregateRoot>>
    }
    class PromiseConstraint {
        <<ValueObject>>
        promiseId string
        tier Tier
        deliverByLatest time.Time
        witnessNodeId string
        revisionNo int
    }
    class Candidate {
        <<ValueObject>>
        nodeId string
        pickupDepartureId string
        pickupCutoffAt time.Time
        legs string[]
        deliveryWaveId string
        eta time.Time
        cost int64
    }
    class Release {
        <<ValueObject>>
        no int
        nodeId string
        releasedAt time.Time
        requiredShipBy time.Time
        outcome Outcome
        reasonCode string
    }
    class ExcludedNode {
        <<ValueObject>>
        nodeId string
        reason string
    }
    class Line {
        <<ValueObject>>
        lineNo int
        catalogItemId string
        listingId string
        quantity int
    }
    class DeliveredPackage {
        <<ValueObject>>
        trackingNumber string
        deliveredAt time.Time
    }
    class Outcome {
        <<Enumeration>>
        PENDING
        ACCEPTED
        REJECTED
        EXPIRED
    }
    class Tier {
        <<Enumeration>>
        SAME_DAY
        NEXT_DAY
        TWO_DAY
        STANDARD
    }
    Assignment *-- PromiseConstraint : promise
    Assignment *-- "1..200" Line : lines
    Assignment *-- "0..1" Candidate : candidate
    Assignment *-- "0..*" Release : releases
    Assignment *-- "0..*" ExcludedNode : excluded
    Assignment *-- "0..*" DeliveredPackage : delivered
    PromiseConstraint --> Tier
    Release --> Outcome
```

Source: `internal/domain/assignment/types.go`, `assignment.go`, `commands.go`, `snapshot.go`, `internal/application/ports/ports.go`.
The aggregate is rebuilt from the database only through `Restore(Snapshot)`, which re-validates every invariant. The six model invariants are the errors `ErrCandidateBreaksPromise`, `ErrAssignmentLocked`, `ErrNodeExcluded`,
`ErrReleaseInFlight`, `ErrStaleAssignmentVersion` and `ErrCancelAfterLock`; `ErrReleaseNotPending` and `ErrStaleRevision`
are additional ones (`errors.go`).

## 2. Domain events

```mermaid
classDiagram
    direction LR
    class Event {
        <<interface>>
        +EventName() string
        +Entity() string
        +SubjectID() string
        +Stream() string
        +AggregateVersion() int64
        +OccurredAt() time.Time
    }
    class FulfillmentQueued {
        <<DomainEvent analytics>>
        +OrderID
        +GroupNo
        +PromiseID
        +DeliverByLatest
        +Lines
    }
    class FulfillmentReleasedToNode {
        <<DomainEvent>>
        +ReleaseNo
        +OrderID
        +GroupNo
        +NodeID
        +PickupDepartureID
        +RequiredShipBy
        +PromisedDeliverByLatest
        +Tier
        +Lines
    }
    class FulfillmentReassigned {
        <<DomainEvent>>
        +FromNodeID
        +ToNodeID
        +Reason
    }
    class FulfillmentLocked {
        <<DomainEvent>>
        +OrderID
        +GroupNo
        +PromiseID
        +NodeID
        +PickupDepartureID
        +LockedAt
        +LockReason
    }
    class FulfillmentShipped {
        <<DomainEvent>>
        +OrderID
        +GroupNo
        +NodeID
        +TrackingNumbers
        +ShippedAt
    }
    class FulfillmentDelivered {
        <<DomainEvent analytics>>
        +OrderID
        +GroupNo
        +DeliveredAt
    }
    class FulfillmentUnsourceable {
        <<DomainEvent>>
        +OrderID
        +GroupNo
        +PromiseID
        +ExcludedNodes
        +Reason
    }
    class FulfillmentCancelled {
        <<DomainEvent>>
        +OrderID
        +GroupNo
        +Reason
    }
    class FulfillmentCancelRefused {
        <<DomainEvent>>
        +OrderID
        +GroupNo
        +Reason
    }
    class FulfillmentConstraintRevised {
        <<DomainEvent>>
        +RevisionNo
        +DeliverByLatest
    }
    Event <|.. FulfillmentQueued
    Event <|.. FulfillmentReleasedToNode
    Event <|.. FulfillmentReassigned
    Event <|.. FulfillmentLocked
    Event <|.. FulfillmentShipped
    Event <|.. FulfillmentDelivered
    Event <|.. FulfillmentUnsourceable
    Event <|.. FulfillmentCancelled
    Event <|.. FulfillmentCancelRefused
    Event <|.. FulfillmentConstraintRevised
```

Source: `internal/domain/shared/shared.go` (`Event`), `internal/domain/assignment/events.go`. Every event embeds `Base`
(`assignmentId`, the aggregate version after the change and the instant); `SubjectID()` returns the `assignmentId`, which becomes
the CloudEvents `subject` and the Kafka key. Only `FulfillmentQueued` and `FulfillmentDelivered` override `Stream()` to the
analytics stream. Wire field names are on the [event catalogue](/docs/api-reference/events).

## 3. The search, the read-model types and the use cases

```mermaid
classDiagram
    direction LR
    class World {
        <<ValueObject sourcing>>
        Nodes map
        Departures Departure[]
        Lanes map
        Waves Wave[]
        Area ServiceArea
        Listings Listing[]
        Discontinued map
        Scorecards map
        Pressure Pressure[]
    }
    class Request {
        <<ValueObject sourcing>>
        Now time.Time
        Tier Tier
        DeliverByLatest time.Time
        WitnessNodeID string
        Lines Line[]
        Excluded ExcludedNode[]
    }
    class Option {
        <<ValueObject sourcing>>
        Candidate Candidate
        ReleaseAt time.Time
    }
    class Search {
        <<DomainFunction>>
        +Search(world, request) Option[]
        +Best(world, request) Option
        +ResolveLines(world, nodeId, lines)
    }
    class Projector {
        <<UseCase>>
        +NodeRegistered()
        +DepartureScheduled()
        +WaveScheduled()
        +ListingAvailabilityChanged()
        +ScorecardUpdated()
        +BookExhausted()
    }
    class SweepQueue {
        <<UseCase>>
        +Execute(ctx, after, limit) SweepResult
    }
    Search ..> World
    Search ..> Request
    Search ..> Option
    Projector ..> World : fills the rm_ tables it is loaded from
    SweepQueue ..> Search : through ReleaseToNode and ReoptimiseAssignment
```

Source: `internal/domain/sourcing/sourcing.go`, `internal/domain/network/network.go` (`Node`, `Lane`, `Departure`, `Wave`,
`ServiceArea`, `Region`, `Listing`, `Scorecard`, `Pressure`), `internal/application/usecases/projector.go`, `sweep.go`. The weights
and the 30 minute ack budget are the constants `LegCost`, `HourCost`, `CrossRegionPenalty`, `BookPressurePenalty` and `AckBudget`.
`ReassignOnRefusal` and `LockOnCommit` of the design are not classes: they are the Kafka handlers calling `HandleNodeRefusal`
and `LockAssignment`.

## 4. Hexagonal ports and adapters

```mermaid
flowchart TB
    subgraph IN["internal/adapters/inbound"]
        HTTP["http<br/>GET /assignments<br/>GET /assignments/id<br/>POST /assignments/id:reoptimise"]
        KC["kafka<br/>one consumer per topic,<br/>dispatch on full type"]
        MCP["mcp<br/>list_assignments, get_assignment"]
        A2A["a2a<br/>find_assignments, assignment_detail"]
    end
    subgraph APP["internal/application/usecases"]
        UC["QueueAssignment, ReoptimiseAssignment, ReleaseToNode,<br/>HandleNodeRefusal, LockAssignment, CancelAssignment,<br/>MarkShipped, MarkDelivered, RecordPickupMoved,<br/>ApplyRevisedPromise, RecordProcessed,<br/>SweepQueue, Projector, Queries"]
    end
    subgraph DOM["internal/domain"]
        D["assignment: aggregate, value objects, errors, events<br/>sourcing: candidate search, AssignmentCost, releaseAt<br/>network: read-model value types"]
    end
    subgraph PORTS["internal/application/ports"]
        P["AssignmentRepository, ReadModels, WorldReader,<br/>EventOutbox, Inbox, UnitOfWork, Clock,<br/>IDGenerator, IdempotencyStore, SourcingMetrics"]
    end
    subgraph OUT["internal/adapters/outbound"]
        PG["postgres<br/>repositories, rm_ tables,<br/>outbox_events, processed_events"]
        KO["kafka + outbox<br/>relay to retailnetwork.order-sourcing.*"]
        TEL["telemetry<br/>sourcing.groups.sourced"]
    end
    HTTP --> UC
    KC --> UC
    MCP --> UC
    A2A --> UC
    UC --> D
    UC --> P
    P --- PG
    P --- KO
    P --- TEL
```

Source: `internal/application/ports/ports.go`, the package layout under `internal/adapters`, and `internal/architecture`
(`TestHexagonalArchitecture`). The in-memory repository (`outbound/memory`) is a test double and appears in no binary
(`TestRuntimeBinaryHasNoInMemoryAdapters`). There is no outbound HTTP client: the context calls no sibling. Path parameters are
written `id` inside the diagram. The layout and the rules are on [Architecture](/docs/overview/architecture).
