---
id: domain-message-flow
title: Domain message flow
sidebar_label: Domain message flow
description: "Domain Message Flow scenarios for Order Sourcing: placement to lock, refusal to re-promise, shipment to delivery, and cancellation, using the messages the code sends and consumes."
---

# Domain message flow

Following ddd-crew [Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling). Participants are
contexts and external systems. Every arrow is a message that exists in the code, prefixed `cmd:` (command), `qry:` (query) or
`evt:` (event). Commands handled inside this context are shown as notes, not arrows. The network-level view with worked times is
in the aggregator's [order assignment model](https://iqvo.github.io/warehouse-docs/network/network-model/order-assignment-model).

Event names are short; the full types are
`com.retailnetwork.commerce.order-sourcing.assignment.<EventName>` for this context and are on the
[event catalogue](/docs/api-reference/events). A number after a release or purchase-order event is its `releaseNo`; a hyphen at a
line end only wraps a long name (`Fulfillment-ReleasedToNode` is `FulfillmentReleasedToNode`).

Source for the flows: `internal/adapters/inbound/kafka/handlers.go` (what is consumed), `internal/domain/assignment/commands.go`
(what each command raises), the downstream handlers named on [Integration](/docs/ecosystem/integration). The vendor-side steps
(`vendor-connectivity` issuing and acknowledging the purchase order) are that context's and are shown as read from its design;
they are not in this repository.

## 1. Happy path: placed, queued, released, locked

```mermaid
sequenceDiagram
    autonumber
    participant CO as customer-<br/>ordering
    participant OS as order-<br/>sourcing
    participant VC as vendor-<br/>connectivity
    participant V as vendor<br/>system
    participant LH as linehaul-<br/>transportation
    participant DP as delivery-<br/>promise
    CO->>OS: evt: Customer-<br/>OrderPlaced
    Note over OS: QueueAssignment<br/>per group, QUEUED
    Note over OS: sweep re-optimises<br/>until releaseAt
    Note over OS: ReleaseToNode,<br/>RELEASED
    OS->>VC: evt: Fulfillment-<br/>ReleasedToNode 1
    OS->>LH: evt: FulfillmentReleasedToNode 1
    Note over VC: IssueOnRelease,<br/>PO ISSUED
    V->>VC: qry: GET /vendor/<br/>purchase-orders
    V->>VC: cmd: POST /vendor/<br/>acknowledgements<br/>ACCEPT
    Note over VC: PO ACCEPTED
    VC->>OS: evt: PurchaseOrder-<br/>Accepted 1
    Note over OS: LockAssignment,<br/>LOCKED
    OS->>DP: evt: FulfillmentLocked
    OS->>CO: evt: Fulfillment-<br/>Locked
    OS->>LH: evt: FulfillmentLocked
    Note over LH: BookOnLock
```

Omitted: `FulfillmentQueued` (analytics only, raised with `QueueAssignment`). The vendor system is any vendor's integration; for
vendor `V-WS` it is the fleet's `network-fulfillment`, which talks only to `vendor-connectivity`. `SAME_DAY` groups skip the
sweeps: `ReleaseToNode` runs inside the same transaction as `QueueAssignment`. A `FulfillmentReassigned` is raised by a sweep that
changes the node before release (consumer `linehaul-transportation`).

## 2. Failure path: rejected, reassigned, expired, unsourceable, re-promised

```mermaid
sequenceDiagram
    autonumber
    participant V as vendor<br/>system
    participant VC as vendor-<br/>connectivity
    participant OS as order-<br/>sourcing
    participant LH as linehaul-<br/>transportation
    participant DP as delivery-<br/>promise
    participant CO as customer-<br/>ordering
    Note over OS: RELEASED to A,<br/>releaseNo 1
    V->>VC: cmd: POST /vendor/<br/>acknowledgements<br/>REJECT
    VC->>OS: evt: PurchaseOrder-<br/>Rejected 1
    Note over OS: HandleNodeRefusal,<br/>A excluded,<br/>QUEUED on B
    OS->>LH: evt: Fulfillment-<br/>Reassigned A to B
    Note over OS: sweep releases to B
    OS->>VC: evt: Fulfillment-<br/>ReleasedToNode 2
    OS->>LH: evt: Fulfillment-<br/>ReleasedToNode 2
    Note over VC: no ack before<br/>ackBy, expired
    VC->>OS: evt: PurchaseOrder-<br/>Expired 2
    Note over OS: B excluded,<br/>no candidate left,<br/>UNSOURCEABLE
    OS->>DP: evt: FulfillmentUnsourceable<br/>excludedNodes A, B
    OS->>CO: evt: FulfillmentUnsourceable
    Note over DP: RepromiseOnRisk,<br/>RevisePromise
    DP->>OS: evt: PromiseRevised<br/>revisionNo 1
    Note over OS: ApplyRevisedPromise,<br/>exclusions reset,<br/>QUEUED again
    OS->>LH: evt: FulfillmentConstraintRevised
```

Omitted: `PurchaseOrderRejected` and `PurchaseOrderExpired` are also read by `node-performance` for the scorecard. If nothing is
feasible even at the loosest tier, `delivery-promise` sends no `PromiseRevised`; it reports the promise broken later and
`customer-ordering` may cancel the group, which this context accepts for an `UNSOURCEABLE` assignment (`Cancel` allows it).
`FulfillmentConstraintRevised` is drawn with no known reader (no consumer was found in the four downstream clones, see
[Integration](/docs/ecosystem/integration)). If the revised promise yields no candidate, the assignment stays `UNSOURCEABLE`
silently, with no second `FulfillmentUnsourceable`.

## 3. Shipped and delivered

```mermaid
sequenceDiagram
    autonumber
    participant V as vendor<br/>system
    participant VC as vendor-<br/>connectivity
    participant OS as order-<br/>sourcing
    participant CO as customer-<br/>ordering
    participant ST as shipping-and-<br/>tracking
    participant DP as delivery-<br/>promise
    Note over OS: LOCKED
    V->>VC: cmd: POST /vendor/<br/>shipping-labels
    V->>VC: cmd: POST /vendor/<br/>shipment-confirmations
    VC->>OS: evt: VendorShipment-<br/>Confirmed
    Note over OS: MarkShipped,<br/>SHIPPED
    OS->>CO: evt: Fulfillment-<br/>Shipped
    OS->>DP: evt: Fulfillment-<br/>Shipped
    Note over ST: linehaul and<br/>last mile
    ST->>OS: evt: PackageDelivered<br/>one per package
    ST->>CO: evt: Package-<br/>Delivered
    Note over OS: MarkDelivered per package,<br/>DELIVERED on the last,<br/>FulfillmentDelivered<br/>to analytics only
```

Omitted: label issuing inside the network, the loads and scans between handover and delivery, and `PackageDelivered` to
`delivery-promise` for the judgement. A `BookingMoved` from `linehaul-transportation` repoints the pickup departure of a `LOCKED`
assignment without any event of this context.

## 4. Customer cancels

```mermaid
sequenceDiagram
    autonumber
    participant CO as customer-<br/>ordering
    participant OS as order-<br/>sourcing
    participant VC as vendor-<br/>connectivity
    participant LH as linehaul-<br/>transportation
    Note over CO: cancel requested
    CO->>OS: evt: CustomerOrderCancelled<br/>groupNos
    alt QUEUED, RELEASED or UNSOURCEABLE
        Note over OS: CancelAssignment,<br/>CANCELLED
        OS->>VC: evt: Fulfillment-<br/>Cancelled
        OS->>LH: evt: FulfillmentCancelled
        OS->>CO: evt: FulfillmentCancelled
        Note over VC: CancelOnSourcing-<br/>Cancel, PO CANCELLED
    else LOCKED, SHIPPED or DELIVERED
        OS->>CO: evt: FulfillmentCancelRefused,<br/>CANCEL_AFTER_LOCK
    end
```

Omitted: if the vendor's acceptance is already being processed when the cancellation lands, `vendor-connectivity` cannot cancel an
accepted purchase order (its own rule). An acceptance that reaches this context after it cancelled a `RELEASED` assignment is
`ErrIllegalTransition`, logged and acknowledged, so the vendor may still ship the group: the known residual on the
[bounded context canvas](/docs/ddd/bounded-context-canvas#open-questions).
