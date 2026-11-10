# ADR 0014: a versioned sourcing policy

## Status

Accepted (2026-11-09). Extends ADR 0011 (the comparator, clause 4) and ADR 0004
(catalogue). Refines gap G26. Additive and default-off: until a policy is
published the ordering, the events and the REST views are unchanged. The split
parts of the original contract are NOT implemented (see "What is not decided
here").

## Context

Money-denominated cost (ADR 0011) made the cross-region penalty only a
tie-break behind cents, so a cheaper cross-region lane always wins. The
operator needs a lever to trade a bounded cost deviation for in-region
fulfilment. The lever must be versioned and auditable (which policy was in
force when a group was released), it must not add a synchronous call to the
search, and it must be inert until somebody publishes it. delivery-promise
already solved the same shape with its `PromisePolicy` aggregate (one current
version, every publish creates version n+1, versions are kept); this context
copies that model.

## Decision

1. **The aggregate.** `SourcingPolicy` is a single versioned document: one
   current version, every publish creates version n+1, versions are kept,
   version 0 is the built-in default (nothing published). It is modelled exactly
   like delivery-promise's `PromisePolicy`: a Snapshot struct, `New`-equivalent
   `Publish`, a validating `Restore` and a `Snapshot()` builder, a repository
   port (`Current`, `Save`) with an in-memory double and a Postgres adapter that
   pass one repocontract suite, and a migration. Fields, all integers:
   - `crossRegionSurchargeCents` (0..100000, default 0): added to the effective
     cost of a candidate whose node region differs from the delivery area region
     (both known).
   - `maxCostDeviationPercent` (0..200, default 0): the regional budget of
     clause 3.
2. **Effective cost.** `effectiveCostCents = estimatedCostCents + surcharges`.
   Surcharges apply even when the estimate is incomplete (incomplete stays
   reported, not acted on, ADR 0011 #5).
3. **The comparator** extends ADR 0011 #4; nothing else about it changes. Let
   `cheapest` be the minimum `effectiveCostCents` among the candidates. Budget =
   `cheapest + floor(cheapest * maxCostDeviationPercent / 100)`. Among
   candidates within the budget an IN-REGION candidate beats a cross-region one;
   then effective cents, then the legacy ordinal tie-break and the id tie-breaks
   exactly as today. Candidates over the budget follow, by effective cents. The
   budget rule is active only when `maxCostDeviationPercent > 0`: with
   `maxCostDeviationPercent = 0` and the surcharge 0 the ordering is
   byte-identical to today (a table test over the search fixtures proves it).
   "In-region" needs both regions known, as for the legacy cross-region penalty;
   a candidate with an unknown region counts as in-region.
4. **The policy is an Input to the pure search.** The sourcing use case reads
   the current policy once per search through the repository and passes it in
   `sourcing.Request`; the domain makes no call. The version read is stamped on
   the candidate and so on the release.
5. **REST.** `GET /sourcing-policy` returns the current version, or the defaults
   with `version: 0` when nothing was published. `PUT /sourcing-policy`
   validates the ranges (422 `validation-failed`, problem+json), publishes
   version n+1 and returns it. No MCP change.
6. **The analytics-only event `SourcingPolicyPublished`.** Type
   `com.retailnetwork.commerce.order-sourcing.policy.SourcingPolicyPublished`,
   topic `retailnetwork.order-sourcing.analytics`, dataschema
   `urn:retailnetwork:order-sourcing:analytics:SourcingPolicyPublished:v1`,
   subject (and Kafka key) = `policy`, data = the policy fields + `version` +
   `publishedAt`. It is raised in the same transaction as the save (outbox).
7. **`FulfillmentReleasedToNode`** gains optional `policyVersion` (integer,
   present only when a policy version >= 1 was in force, omitted otherwise) and
   optional `effectiveCostCents` (present only when a surcharge changed the cost,
   that is when it differs from the estimate's cents; omitted otherwise).
   Additive and optional; consumers decode tolerantly, so no consumer change is
   expected. The REST candidate view carries both under the same omission rules,
   and the analytics-only `FulfillmentSourcingDecided` carries both too (it
   already carries `estimatedCostCents`).
8. **Metrics.** An observable gauge `ordersourcing_sourcing_policy_version`
   (the version last read) and a counter
   `ordersourcing_budget_preferred_total{kind="region"}` incremented, once per
   release, when the budget rule changed the winner relative to the pure
   cheapest ordering.

## What is not decided here (split surcharge, single-node preference)

The contract also asked for `splitSurchargeCents` (added once per additional
shipment when an order's lines are sourced from more than one node) and a
single-node-beats-split preference in the budget. Order-sourcing has no notion
of splitting an order across nodes: customer-ordering decides the shipment
groups, this context assigns each group on its own (one `FulfillmentAssignment`
per `orderId` + `groupNo`), and `ResolveLines` already requires ONE node to ship
every line of the group. A search never compares a single-node plan against a
split plan, so there is nothing for such a surcharge or preference to act on
without inventing an order-level planner. Both parts are therefore NOT built,
`splitSurchargeCents` is NOT a field of the policy, and the budget metric has
only the `region` kind. Revisit when sourcing plans across the groups of one
order.

## Consequences

- Behaviour is unchanged until an operator publishes a policy with
  `PUT /sourcing-policy`. A policy with both fields 0 is the default.
- With `maxCostDeviationPercent = 20` a cross-region lane that is cheapest
  loses to an in-region one inside 120 % of its effective cost and wins outside
  it; the surcharge makes the cross-region lane dearer in the cents themselves.
- The persisted candidate (jsonb column) now also carries the estimate, its
  completeness, the effective cost and the policy version, all optional; rows
  written before this ADR decode with zeros, so no data migration touches the
  assignments table. Migration 0005 only adds `sourcing_policies`.
- Every publish raises one analytics event; the projector ignores it
  (acknowledged silently) until a report needs it.
- Deploy: no environment variable, topic or chart change; the analytics topic
  already exists.
