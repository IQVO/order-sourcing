# ADR 0011: the money-denominated AssignmentCost

## Status

Accepted (2026-11-05). Supersedes the ordinal AssignmentCost ordering of ADR 0002, A12.

## Context

Roadmap F7 asks for a cost-to-serve in money: until now the search ranked
candidates by an ordinal score (100 per leg, 10 per hour of queueing, 1000
cross-region, 500 book pressure, 50/200/500 by scorecard grade), which cannot
answer "is the cheaper network path also the cheaper one in cents?". The
planner contexts now declare real marginal costs: node-topology publishes
`LaneCostDeclared` (`costPerUnitCents`, what moving ONE package-unit across the
lane costs) and last-mile-delivery publishes `WaveCostDeclared`
(`costPerStopCents`, what ONE delivery stop costs). Both are consumed into
`NetworkGraphView` (ADR 0003). The simpler alternative — keep the ordinal score
and publish it as a pseudo-currency — was rejected: it answers neither the
crossover question nor any budget question, and it would leak an internal
scoring constant into a public contract.

## Decision

1. **The estimate.** `estimatedCostCents` of a candidate path is the sum of the
   declared per-unit lane costs of EVERY departure traversed — the pickup plus
   the onward legs — plus the declared per-stop cost of the final wave. Integer
   cents, no currency (single-currency study), each term planner-declared.
2. **0 versus undeclared.** A declared cost of 0 is legal and contributes 0
   with the estimate still complete. A component without a declaration (a lane
   the view does not know, or knows without a cost declaration; a wave without
   one) contributes 0 AND marks the estimate incomplete
   (`Candidate.CostComplete` false). The distinction lives in the read model
   (`CostDeclaredAt` zero means no declaration seen) and never on the wire.
3. **Per package-unit, no multiplication in v1.** The lane term is per
   package-unit and SHOULD be multiplied by the assignment's package count —
   but neither the World nor the request carries one (`Line.Quantity` counts
   order units, not packages; assumption A24). The v1 estimate is therefore the
   per-unit sum, and `packagesPerOrder` multiplication is added only when a
   package count becomes known.
4. **The comparator.** Options order by `estimatedCostCents` (lower wins)
   first, then the legacy ordinal score of ADR 0002 A12 — legs, hours,
   cross-region, scorecard, book pressure, with today's constants and
   semantics exactly — as ONE tie-break score, then the witness node, node id
   and pickup id as before. The same ordering picks the best onward path of one
   pickup. Money leads; the ordinal terms no longer decide between paths that
   differ in cents.
5. **Incomplete estimates still compete.** The comparator never inspects
   completeness: an incomplete estimate compares by its sum with the 0s
   included. Special-casing it (skip, or rank last) would make an undeclared
   lane look worse than a declared expensive one and would let a missing
   declaration silently veto the only feasible path; the estimate's
   completeness is reported, not acted on.
6. **The release event.** `FulfillmentReleasedToNode` carries optional
   `estimatedCostCents`, omitted when the estimate is incomplete. One field,
   additive and optional, so consumers (vendor-connectivity, which issues the
   purchase order) decode tolerantly and older consumers are unaffected. The
   REST candidate view carries the same field under the same rule.

## Consequences

- The air-versus-truck crossover EMERGES from the monetary comparison: when
  the planner declares a cheaper per-unit cost on the air lane than on the
  ground lane for the same city pair, the search prefers air with no distance,
  mode or speed heuristic anywhere in this context. The crossover is the
  planner's declaration, not an algorithm.
- Node handling cost is EXCLUDED in v1: it is a vendor-contract term, and
  changing the vendor contract is a separate roadmap decision. The estimate
  under-counts by that term until then.
- Candidate quality now depends on declarations being present: a network where
  nothing is declared compares every path at 0 cents (all incomplete) and falls
  through to the legacy tie-break — exactly the pre-F7 behaviour, so rollout is
  safe with declarations arriving gradually.
- The legacy score stays on the wire (`cost`), so dashboards built on it keep
  working; it is now a tie-break column.
- A late `LaneCostDeclared` / `WaveCostDeclared` (before `LaneOpened` /
  `DeliveryWaveScheduled`) still lands in the read model (ADR 0003), so the
  estimate cannot be silently lost to event ordering; it changes at the next
  sweep at the earliest.
