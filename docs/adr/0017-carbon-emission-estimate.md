# ADR 0017: the carbon estimate of a released path (planner-declared gCO2e, ranking unchanged)

## Status

Accepted (2026-11-14). The emission twin of ADR 0011; changes no ordering.

## Context

Roadmap C9 asks for the carbon footprint of a sourced path next to its money
cost. The planner contexts now declare real marginal emissions exactly as they
declare costs: node-topology publishes `LaneEmissionDeclared` (`laneId`,
`gCo2ePerUnit`, `declaredAt`; per package-unit crossing the lane) and
last-mile-delivery publishes `WaveEmissionDeclared` (`waveId`, `gCo2ePerStop`,
`declaredAt`; per delivery stop). Both are additive mirrors of the cost
declarations ADR 0011 consumes. The alternative — deriving emissions from
distance or mode inside order-sourcing — was rejected: this context owns no
vehicle, fuel or lane geometry, and a planner declaration keeps the number
accountable to the party that operates the leg. A second alternative — ranking
on carbon now — was rejected for this slice: no budget or target exists yet,
and silently reordering the network on a partially declared field would let a
missing declaration veto paths (the failure mode ADR 0011 §5 refuses for
money).

## Decision

1. **The estimate.** `estimatedCo2eGrams` of a candidate path is the sum of the
   declared per-unit lane emissions of EVERY departure traversed — the pickup
   plus the onward legs — plus the declared per-stop emission of the final
   wave. Integer grams of CO2e equivalent, each term planner-declared, per
   package-unit with no multiplication in v1 (the same ADR 0011 §3 rule: the
   World carries no package count).
2. **0 versus undeclared.** A declared emission of 0 is legal and contributes 0
   with the estimate still complete. A component without a declaration (a lane
   the view does not know, or knows without an emission declaration; a wave
   without one) contributes 0 AND marks the estimate incomplete
   (`Candidate.EmissionComplete` false). The distinction lives in the read
   model (`EmissionDeclaredAt` zero means no declaration seen) and never on the
   wire.
3. **Ranking is UNCHANGED.** The comparator never sees grams: options order
   exactly as ADR 0011 and ADR 0014 define (effective money cost first, the
   ordinal tie-break score second, then the hold rules of ADR 0016). Carbon is
   recorded, not optimised, in this slice; a carbon-budget policy lever is the
   named follow-up, mirroring how the regional budget later consumed ADR 0011's
   costs.
4. **Honest omission on the wire.** `FulfillmentReleasedToNode` and
   `FulfillmentSourcingDecided` carry optional `estimatedCo2eGrams` and
   `emissionComplete`, both omitted when the estimate is incomplete — the exact
   `estimatedCostCents` semantics (ADR 0011 §6), so absence always means "not
   known", never "zero". The REST candidate view, the MCP tools and the A2A
   view carry `estimatedCo2eGrams` under the same rule. Additive and optional:
   consumers decode tolerantly and older consumers are unaffected.
5. **Read model.** The emission declarations are columns of the same rows as
   the costs (migration 0007: `rm_lanes.emission_per_unit_g`,
   `rm_waves.emission_per_stop_g`, both `NOT NULL DEFAULT 0`, plus their
   `emission_declared_at` timestamptz). The merge rules are identical to the
   costs': the newer `declaredAt` wins, and a declaration for an unknown lane
   or wave is stored keyed by its id anyway — the later `LaneOpened` /
   `DeliveryWaveScheduled` completes the row without losing the emission
   (ADR 0003).
6. **Consolidation.** A merged release emits one release event and one
   decision per member; the carbon estimate is per member from the merged
   option, the same rule as the money estimate and the units (ADR 0015).

## Consequences

- Honest silence until planners declare emissions: with no declaration
  anywhere every estimate is incomplete and the fields are simply absent —
  no zeros-as-fact, and rollout is safe with declarations arriving gradually.
  A late declaration changes the estimate at the next sweep at the earliest.
- The network's choices are bit-for-bit what they were: same inputs, same
  candidate, same path. The recorded grams let analytics and operators audit
  the carbon of the chosen path without trusting a heuristic.
- No consumer in the estate reads the fields yet; the analytics projection of
  ADR 0013 ignores them tolerantly, and a carbon-per-path report is a
  follow-up on the analytics read side, not this slice.
- The estimate under-counts by the same excluded terms as the money estimate
  (node handling, ADR 0011): it is a path footprint, not a lifecycle one.
