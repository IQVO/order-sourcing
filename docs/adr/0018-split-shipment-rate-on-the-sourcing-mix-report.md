# ADR 0018: the split-shipment rate on the sourcing-mix report

## Status

Accepted (2026-11-18). Extends ADR 0013 (the analytics read side and the
sourcing-decision fact) and reads the consolidation fields ADR 0015 already put
on that fact. Additive: no integration payload, no AsyncAPI document, no OLTP
table, no existing report field and no route changes. One nullable column
(migration `0002_split_shipment_package`) in the analytical database only, one
new object (`splitShipment`) on each day of `GET /reports/sourcing-mix`, one
new method on the report reader port.

## Context

KPI baseline v2 (N0.3) asks how often an order is split into several shipments
as it is actually SOURCED, not as it is queued. ADR 0013's `order-splits`
report counts the groups an order was queued with (the planner's grouping, one
transaction at queue time). It says nothing about what the sourcing decision
did with them: two groups can leave from one node in one package after a
carrier/follower merge (ADR 0015), or can leave from two nodes. The split
shipment rate is the share of sourced orders whose goods leave in more than one
shipment.

What the sourcing-decision fact (`FulfillmentSourcingDecided`, ADR 0013 and
0015) already carries is enough: `assignmentId`, `orderId`, `groupNo?`,
`releaseNo`, `nodeId`, and, on a follower of a merged release,
`carrierAssignmentId`. The projector decoded `carrierAssignmentId` already
(ADR 0015 uses it to book a merged package's cost once) but did not store it.
No producer-side change is needed: the event, its golden bytes, the AsyncAPI
document and the ADR 0004 catalogue are untouched.

Alternatives weighed:

- **Reuse `order-splits`.** It is queue-time and counts groups, so a merged
  pair of groups counts as split. Rejected: it is a different question, and its
  fields must not change meaning.
- **Count assignments per order from `sourcing_decisions` (groups, ignoring the
  merge).** Rejected: a follower absorbed into its order's carrier ships in the
  same package, so counting it would call an unsplit order split.
- **Count distinct `nodeId` per order.** Rejected as the definition: two groups
  sourced from one node and NOT merged are two purchase orders and two tracking
  numbers (ADR 0015 context), a split shipment from one node. It is, however,
  implied (see 2).
- **A second event or a producer field (`isSplit`).** Rejected: the existing
  fact decides it.

## Decision

### 1. Vocabulary

- A **decision** (of an order) is a sourced ORDER: an `orderId` with at least
  one `FulfillmentSourcingDecided` fact. Orders with no decision (unsourceable
  groups, groups still queued) are not decisions.
- The **current release** of an assignment is its decision with the highest
  `releaseNo` (the same rule as cost-to-serve).
- The **shipment** (package) of a current release is `carrierAssignmentId` when
  the fact names a carrier, else its own `assignmentId`. A follower therefore
  shares its carrier's shipment, and a re-released follower (a new release with
  no carrier) has its own.
- **Shipment groups of a decision** = the number of distinct shipments among the
  current releases of the order's assignments.

### 2. The split definition (exact)

A decision is a **split decision** when it has **2 or more shipment groups**
(shipments, 1.). The "2 or more fulfilling nodes" condition is not a separate
test: a follower is always released at its carrier's node (ADR 0015 c2), so two
nodes imply two shipments. Two groups merged into one carrier count as ONE
shipment group and the decision is not split.

`splitShare` = split decisions / decisions, `null` when the day has 0
decisions.

### 3. The day and the distribution

- The day of a decision is the UTC day of the order's FIRST sourcing decision
  (the lowest `decidedAt` over every release of every assignment of the order),
  so an order never moves between days; the same convention as `order-splits`
  (an order belongs to the day of its first fact).
- Only decisions the projector has seen are counted: groups of an order are
  released by the sweep at their own due times, so an order that releases its
  second group on a later day is counted on its first day and moves from the
  `1` bucket to the `2` bucket when the second decision arrives. The latest
  days are therefore provisional by up to the release spread of one order;
  `/reports/freshness` tells how far the projection is behind.
- `groupsPerDecision` is the distribution of the shipment groups of a decision,
  bounded to FOUR buckets, in this order, always all four present:
  `"1"`, `"2"`, `"3"`, `"4+"` (the last holds 4 or more). The bucket counts sum
  to `decisions`; `splitDecisions` is the sum of the last three.

### 4. What the day object gains

Each day of `GET /reports/sourcing-mix` gains one additive object:

```
splitShipment: {
  decisions:         int  >= 0
  splitDecisions:    int  >= 0
  splitShare:        number 0..1 | null
  groupsPerDecision: [ {bucket: "1"|"2"|"3"|"4+", decisions: int >= 0} x4 ]
}
```

(camelCase on the wire, ADR 0010; the KPI brief's `split_shipment`,
`split_decisions`, `split_share` are these fields.) A day that appears only
because of an unsourceable declaration, or a day whose orders began before the
range, carries `decisions: 0`, `splitDecisions: 0`, `splitShare: null` and four
zero buckets. A day with no release and no unsourceable declaration is omitted,
exactly as before. Every existing field of the day is unchanged.

The block rides on `sourcing-mix` and not on `order-splits` because its source
is the sourcing decision, which is what `sourcing-mix` is bucketed on.

### 5. Read model

- Migration `analytics/migrations/0002_split_shipment_package` (additive, with a
  down file): `sourcing_decisions.carrier_assignment_id TEXT NULL`
  (`CHECK (carrier_assignment_id <> '')`) and the index
  `idx_sourcing_decisions_order (order_id)`. Rows projected before the
  migration have NULL, i.e. "own shipment"; the projector's earliest-offset
  replay (ADR 0013, 4) rebuilds them, because a republished decision upserts
  the column.
- Projector fold: `Decision.CarrierAssignmentID` is read from the fact and
  stored by the `(assignment_id, release_no)` upsert. Nothing else of the
  projection changes.
- Reader: SQL only counts: per UTC day of the order's first decision, the
  orders grouped by their number of distinct shipments. The bucket cap, the
  day join, `decisions`, `splitDecisions` and `splitShare` live in
  `internal/analytics/report` (pure, in `make mutation`). The in-memory twin
  implements the same rule and runs the same contract suite.

### 6. Collection-point decisions: not reported

`collection_point_decisions` is NOT added. Nothing in this context's
sourcing-decision fact, its aggregate, its ADRs or its consumed contracts
carries a collection-point or pickup-point indicator: the "pickup" of ADR 0002
(A12), 0015 and 0016 is the departure that collects the goods from the
fulfilling node, not a destination collection point. Adding one would need the
destination's delivery method, which is another context's data, fed to this
one by a consumed event and a decision made here with it; neither exists. No
cross-context call is added. When customer-ordering or delivery-promise
publish such an indicator and order-sourcing consumes it into its own read
model, a later ADR can add the counter.

### 7. No PII

Only counts and ids already on the analytical store (`assignmentId`,
`orderId`, `nodeId`) are used; the response carries neither.

## Consequences

- Existing consumers of `GET /reports/sourcing-mix` keep working: a field is
  added, none is renamed, retyped or removed. A client with a strict
  `additionalProperties: false` response parser would need the new property
  (the document does not forbid additional properties).
- Pinned copies of `apis/openapi.yaml` in other repositories stay valid until
  they refresh; they are listed in the pull request.
- **Rollout order.** The projector applies migration `0002` at boot (ADR 0013,
  5); the reports binary reads the new column. Deploy the projector first. A
  reports binary ahead of the migration answers a 500 problem on
  `sourcing-mix` until the projector has restarted; an old projector against
  the migrated schema keeps working (the column is nullable and its insert does
  not name it).
- The split rate is only as complete as the projected decisions: facts
  published before ADR 0013 have no decision (as for the other sourcing
  reports).
- A decision is an order, a reassignment can move a group to another shipment
  later; the report reads the current releases, so a day's figure is the state
  of the projection now, not a frozen snapshot.
