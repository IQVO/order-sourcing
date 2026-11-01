---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
description: "Process-level EventStorming boards for Order Sourcing: intake and the virtual queue, release, the node's answer, re-promise and cancellation, shipment and delivery, with the hotspots that were resolved by the build and those still open."
---

# EventStorming (process level)

Uses the ddd-crew [EventStorming glossary and cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet)
notation. Every sticky is an element of the code: a command of `FulfillmentAssignment`, an event in
`internal/domain/assignment/events.go` or a consumed type in `internal/adapters/inbound/kafka/handlers.go`. Events from other
contexts are orange stickies next to the pink system that publishes them. The aggregate sticky is repeated on each board so
lines do not cross. Hotspots are gaps or risks; the inventory at the end says which the build resolved.

## Legend

| Sticky | Colour | Meaning here |
| --- | --- | --- |
| Command | blue `#a5d8ff` | A command of `FulfillmentAssignment` |
| Aggregate | yellow `#ffec99` | `FulfillmentAssignment` |
| Domain event | orange `#ffc078` | An event this context publishes, or a neighbour event it consumes |
| Policy | lilac `#d0bfff` | A policy or a business rule |
| Read model | green `#b2f2bb` | One of the five read models |
| External system | pink `#fcc2d7` | A sibling context |
| Hotspot | red `#ff8787` | An open question or risk |
| Actor | white `#ffffff` | A person or this context's own timer |

## 1. Intake and the virtual queue

```mermaid
flowchart TB
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
    classDef actor fill:#ffffff,stroke:#495057,color:#000
    CO["customer-ordering"]:::external
    E0["CustomerOrderPlaced"]:::event
    P0["Policy: one assignment<br/>per shipment group"]:::policy
    C1["QueueAssignment"]:::command
    A1["FulfillmentAssignment"]:::aggregate
    E1["FulfillmentQueued"]:::event
    E1b["FulfillmentUnsourceable<br/>when no candidate"]:::event
    P1["Policy: VirtualQueue<br/>releaseAt = cutoff - handling<br/>- 30 min ack budget"]:::policy
    RM1["NodeProfileView"]:::readmodel
    RM2["NetworkGraphView"]:::readmodel
    RM3["AvailabilityView"]:::readmodel
    RM4["ScorecardView"]:::readmodel
    RM5["PromiseBookPressureView"]:::readmodel
    P2["Policy: AssignmentCost<br/>legs + wait + cross-region<br/>+ scorecard + book pressure"]:::policy
    T1["Sweep timer"]:::actor
    PL["Network-ops planner"]:::actor
    C2["ReoptimiseAssignment"]:::command
    E2["FulfillmentReassigned"]:::event
    LH["linehaul-transportation"]:::external
    H3["Hotspot: cross-region is a<br/>penalty, not a filter"]:::hotspot
    H1["Hotspot: promise-book hold<br/>stays on the witness node"]:::hotspot
    CO --> E0 --> P0 --> C1 --> A1 --> E1
    A1 --> E1b
    A1 --> P1
    RM1 --> P2
    RM2 --> P2
    RM3 --> P2
    RM4 --> P2
    RM5 --> P2
    P2 --> C1
    P2 --> C2
    T1 --> C2
    PL --> C2
    C2 --> A1
    A1 --> E2 --> LH
    P2 -.- H3
    E2 -.- H1
```

Source: `internal/adapters/inbound/kafka/handlers.go` (`CustomerOrdering`), `usecases/queue.go`, `usecases/search.go`,
`domain/sourcing/sourcing.go`, `domain/assignment/commands.go` (`Queue`, `Reoptimise`). Omitted: the 22 events that feed the five
read models ([Use cases](/docs/ddd/use-cases#the-projected-events-use-case-13)); a re-optimisation that keeps the same candidate,
which raises nothing.

## 2. Release

```mermaid
flowchart TB
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef actor fill:#ffffff,stroke:#495057,color:#000
    T2["Sweep: releaseAt due"]:::actor
    P3["Policy: SAME_DAY<br/>releases immediately"]:::policy
    C3["ReleaseToNode"]:::command
    A1["FulfillmentAssignment"]:::aggregate
    P4["Policy: eta must meet<br/>deliverByLatest, node<br/>not excluded, lines in full"]:::policy
    E3["FulfillmentReleasedToNode"]:::event
    E4["FulfillmentUnsourceable"]:::event
    VC["vendor-connectivity<br/>IssueOnRelease"]:::external
    LH["linehaul-transportation"]:::external
    DP["delivery-promise<br/>RepromiseOnRisk"]:::external
    CO["customer-ordering<br/>status"]:::external
    T2 --> C3
    P3 --> C3
    C3 --> A1 --> P4
    P4 -->|candidate ok| E3
    P4 -->|none left| E4
    E3 --> VC
    E3 --> LH
    E4 --> DP
    E4 --> CO
```

Source: `usecases/commands.go` (`ReleaseToNode`), `usecases/queue.go` (`releaseNow`), `domain/assignment/commands.go` (`Release`).
Omitted: `ErrReleaseInFlight` (a second release while one is pending), shown on
[Sequence diagrams](/docs/ddd/sequence-diagrams).

## 3. The node's answer: lock or refusal

```mermaid
flowchart TB
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
    VC["vendor-connectivity"]:::external
    E5["PurchaseOrderAccepted"]:::event
    P5["Policy: LockOnCommit"]:::policy
    C4["LockAssignment"]:::command
    A1["FulfillmentAssignment"]:::aggregate
    E6["FulfillmentLocked"]:::event
    OUT1["delivery-promise,<br/>customer-ordering,<br/>linehaul BookOnLock"]:::external
    H11["Hotspot: pickup departure<br/>cancelled after the lock"]:::hotspot
    E7["PurchaseOrderRejected"]:::event
    E8["PurchaseOrderExpired"]:::event
    P6["Policy: ReassignOnRefusal<br/>exclude node, re-queue"]:::policy
    C5["HandleNodeRefusal"]:::command
    A2["FulfillmentAssignment"]:::aggregate
    E9["FulfillmentReassigned"]:::event
    E10["FulfillmentUnsourceable"]:::event
    VC --> E5 --> P5 --> C4 --> A1 --> E6 --> OUT1
    E6 -.- H11
    VC --> E7
    VC --> E8
    E7 --> P6
    E8 --> P6
    P6 --> C5 --> A2
    A2 -->|candidate left| E9
    A2 -->|none left| E10
```

Source: `usecases/commands.go` (`LockAssignment`, `HandleNodeRefusal`), `domain/assignment/commands.go` (`Lock`, `RefuseRelease`,
`pendingRelease`). The `releaseNo` of the outcome must be the pending release; an older outcome is logged and ignored.
`node-performance` also reads the three purchase-order events for the scorecard. `BookingMoved` repoints the pickup departure
of a locked assignment (`MovePickup`) without an event.

## 4. Re-promise and cancellation

```mermaid
flowchart TB
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
    DP["delivery-promise"]:::external
    E11["PromiseRevised"]:::event
    C6["ApplyRevisedPromise"]:::command
    A1["FulfillmentAssignment<br/>constraint revised,<br/>exclusions reset"]:::aggregate
    E11b["FulfillmentConstraintRevised"]:::event
    CO["customer-ordering"]:::external
    E12["CustomerOrderCancelled"]:::event
    P7["Policy: cancel only<br/>before LOCKED"]:::policy
    C7["CancelAssignment"]:::command
    A2["FulfillmentAssignment"]:::aggregate
    E13["FulfillmentCancelled"]:::event
    E13b["FulfillmentCancelRefused"]:::event
    OUT2["vendor-connectivity<br/>CancelOnSourcingCancel,<br/>linehaul-transportation"]:::external
    H4["Hotspot: acceptance lands<br/>after the cancellation"]:::hotspot
    DP --> E11 --> C6 --> A1 --> E11b
    CO --> E12 --> P7 --> C7 --> A2
    A2 -->|before the lock| E13 --> OUT2
    A2 -->|after the lock| E13b
    E13 -.- H4
```

Source: `usecases/cancel_revise.go`, `domain/assignment/commands.go` (`ApplyRevisedPromise`, `Cancel`, `RefuseCancel`).

## 5. Shipment and delivery

```mermaid
flowchart TB
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    VC["vendor-connectivity"]:::external
    E14["VendorShipmentConfirmed"]:::event
    C8["MarkShipped"]:::command
    A1["FulfillmentAssignment"]:::aggregate
    E15["FulfillmentShipped"]:::event
    CO["customer-ordering,<br/>delivery-promise"]:::external
    ST["shipping-and-tracking"]:::external
    E16["PackageDelivered<br/>one per package"]:::event
    C9["MarkDelivered"]:::command
    A2["FulfillmentAssignment"]:::aggregate
    E17["FulfillmentDelivered<br/>analytics only, last package"]:::event
    VC --> E14 --> C8 --> A1 --> E15 --> CO
    ST --> E16 --> C9 --> A2 --> E17
```

Source: `usecases/commands.go` (`MarkShipped`, `MarkDelivered`), `domain/assignment/commands.go`.

## Hotspot inventory

Resolved by the build (the aggregator's earlier boards listed them as open):

| Hotspot | Resolution in the code |
| --- | --- |
| No `releaseAt` when no candidate is left | A group with no candidate is `UNSOURCEABLE` at once, in `Queue`, `Reoptimise`, `Release` and `RefuseRelease` (`ReasonNoCandidate`, `ReasonNodeRefused`) |
| `ApplyRevisedPromise` raises no event | It always raises `FulfillmentConstraintRevised` |
| `UNSOURCEABLE` cannot be cancelled | `Cancel` allows `QUEUED`, `RELEASED` and `UNSOURCEABLE` |
| Exclusion per promise revision without `revisionNo` | `PromiseConstraint.RevisionNo`; a higher revision resets the list |
| Multi-package delivery | The group delivers when the last package does; each `PackageDelivered` is remembered |
| Revision of an assignment that is not `UNSOURCEABLE` | Applied in every non-terminal state; the node is only re-searched in `QUEUED` and `UNSOURCEABLE` |
| Destination not on the aggregate | `destinationPostalPrefix` is on the aggregate and in the table |

Still open:

| Hotspot | Where it comes from |
| --- | --- |
| Promise-book hold stays on the witness node | The assigned node may differ from the witness, but nothing moves the capacity hold on the `delivery-promise` side |
| Cross-region as penalty versus rule | Business decision 5 reads as a filter; `CrossRegionPenalty` is 1000 points |
| Acceptance after cancellation | A `PurchaseOrderAccepted` for a `CANCELLED` assignment is `ErrIllegalTransition` and only logged; the vendor may still ship |
| Pickup departure cancelled after the lock | `DepartureCancelled` updates the read model; no transition handles a locked assignment whose departure is gone |
