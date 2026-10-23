# ADR 0004: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-23).

## Context

Every Kafka message in the retail network is a CloudEvents 1.0 event in structured
mode (retail-network-docs `model/model.yaml` `conventions.cloudevents`; the repo
rule `.claude/rules/fleet/cloudevents.md`). This ADR is the catalogue of every
type order-sourcing publishes. `TestEventCatalogueMatchesContract` checks it
against `apis/asyncapi.yaml`. The consumed types are listed in ADR 0003.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID persisted with the outbox row (stable across
  relay retries; consumers dedupe on it); `source`
  `/retailnetwork/order-sourcing`; `subject` the `assignmentId`; `time` the
  instant the change was committed, in UTC; `datacontenttype`
  `application/json`; `dataschema`
  `urn:retailnetwork:order-sourcing:<stream>:<EventName>:v1`.
- Kafka value = the JSON event format; header
  `content-type: application/cloudevents+json; charset=UTF-8`; Kafka key = the
  subject, so all events of one assignment stay ordered on one partition.
- `data` is camelCase; optional fields are omitted when unset; arrays are always
  present; timestamps are RFC 3339 UTC.
- Built and decoded only through `internal/adapters/kafka/cloudevents`.
- `<entity>` is the raising aggregate, lowercase, no separators: `assignment`
  for every event. Tier is `commerce`.

### Published types

Eight types go to `retailnetwork.order-sourcing.events`. `FulfillmentQueued` and
`FulfillmentDelivered` are analytics only in the model (consumers: analytics
only), so by `conventions.analytics_rule` they go ONLY to
`retailnetwork.order-sourcing.analytics` (assumption A2 of ADR 0002) and never to
the events topic.

```text
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentQueued stream: analytics
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode stream: events
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReassigned stream: events
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentLocked stream: events
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped stream: events
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentDelivered stream: analytics
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentUnsourceable stream: events
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled stream: events
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelRefused stream: events
type: com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentConstraintRevised stream: events
```

| Event | Raised when | `data` |
|---|---|---|
| `FulfillmentQueued` | QueueAssignment is saved | `{assignmentId, orderId, groupNo, promiseId, deliverByLatest, lines[{lineNo, catalogItemId, quantity}]}` |
| `FulfillmentReleasedToNode` | ReleaseToNode: QUEUED to RELEASED | `{assignmentId, releaseNo, orderId, groupNo, nodeId, pickupDepartureId, requiredShipBy, promisedDeliverByLatest, tier, lines[{catalogItemId, listingId, quantity}]}` |
| `FulfillmentReassigned` | the candidate node changes | `{assignmentId, fromNodeId, toNodeId, reason, assignmentVersion}` |
| `FulfillmentLocked` | LockAssignment: RELEASED to LOCKED | `{assignmentId, orderId, groupNo, promiseId, nodeId, pickupDepartureId, lockedAt, lockReason}` (promiseId, groupNo, orderId: contracts-delta E1) |
| `FulfillmentShipped` | MarkShipped: LOCKED to SHIPPED | `{assignmentId, orderId, groupNo, nodeId, trackingNumbers[], shippedAt}` |
| `FulfillmentDelivered` | MarkDelivered: the last package is delivered | `{assignmentId, orderId, groupNo, deliveredAt}` |
| `FulfillmentUnsourceable` | the candidate set is empty | `{assignmentId, orderId, groupNo, promiseId, excludedNodes[{nodeId, reason}], reason}` |
| `FulfillmentCancelled` | CancelAssignment before the lock | `{assignmentId, orderId, groupNo, reason}` |
| `FulfillmentCancelRefused` | a cancel request after the lock (E11) | `{assignmentId, orderId, groupNo, reason}` |
| `FulfillmentConstraintRevised` | ApplyRevisedPromise (E12) | `{assignmentId, revisionNo, deliverByLatest}` |

Payload rules:

- The `data` field lists are the model's `publishes[].data` lists plus the
  additions of contracts-delta (E1, E11, E12). Wire types are recorded in
  `apis/asyncapi.yaml`.
- Reason vocabularies (the model leaves them open, ADR 0002 A14):
  `FulfillmentReassigned.reason` is one of `REOPTIMISED`, `RELEASE_RECHECK`,
  `PURCHASE_ORDER_REJECTED`, `PURCHASE_ORDER_EXPIRED`, `PROMISE_REVISED`;
  `FulfillmentLocked.lockReason` is `PURCHASE_ORDER_ACCEPTED`;
  `FulfillmentUnsourceable.reason` is `NO_CANDIDATE` or `NODE_REFUSED`;
  `FulfillmentCancelRefused.reason` is `CANCEL_AFTER_LOCK`.
- A breaking payload change is a new `.v2` type and dataschema, never a
  mutation of v1.

## Consequences

- Consumers need nothing but this catalogue and `apis/asyncapi.yaml`; there is
  no shared Go code.
- Adding a published type requires a line here in the same commit as its
  asyncapi message, or the architecture fitness test fails.
