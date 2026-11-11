# ADR 0016: the CONSOLIDATED delivery day, search window and latest-pickup hold

## Status

Accepted (2026-11-10). Extends ADR 0011 (the search and its comparator), ADR 0014
(the policy tiers of the comparator) and ADR 0015 (carrier and follower
consolidation, on which this slice is stacked). Additive: a group that is not
CONSOLIDATED is searched, released and ranked exactly as before.

## Context

delivery-promise offers the customer a CONSOLIDATED option: a delivery on a day
the customer chooses, a day or more after the fastest window, so that the groups
of one shopper can ride together. The promise it returns carries a window
(`deliverByEarliest` and `deliverByLatest`) and the tier `CONSOLIDATED`.
customer-ordering stores the choice and sends it in `CustomerOrderPlaced`.

Until now order-sourcing searched for the cheapest path that reaches ANY delivery
wave ending by `deliverByLatest`, then released at the pickup point of that
path. For a CONSOLIDATED group that is the wrong day (the earliest wave) and the
wrong moment (the earliest pickup, so the group is out of the shopper's reach
long before the day it was promised). It would also leave nothing queued for a
second order of the same shopper to merge with (ADR 0015).

Alternatives weighed:

- Making delivery-promise return the wave id and searching for exactly that
  wave. Rejected: the wave is a planning fact owned by network planning and it
  may be replaced between the promise and the release; the window is the
  contract, the wave is re-found at each search like every other fact.
- A separate scheduler that holds the group until a chosen time. Rejected: the
  VirtualQueue already releases at `releaseAt` = pickup cutoff - handling - ack
  budget. Choosing the latest pickup that still reaches the day IS the hold, with
  no new timer, state or table.
- Mapping CONSOLIDATED to STANDARD on arrival. Rejected: the group would then be
  indistinguishable from a STANDARD one and could not carry the window or merge
  (ADR 0015, rule c1).

## Decision

1. **Tier acceptance sits in ADR 0015, not here.** ADR 0015 (rule c1: only
   CONSOLIDATED groups merge) needs a CONSOLIDATED assignment to exist, so
   `assignment.Tier` already accepts the fifth value, with
   `FulfilmentClass()` mapping it to STANDARD in `FulfillmentReleasedToNode.tier`
   (the vendor and the linehaul never see it). This ADR adds only what the search
   does with it.
2. **The window start bounds the search.** For a CONSOLIDATED promise every
   search (`QueueAssignment`, `ReoptimiseAssignment`, `ReleaseToNode`, the
   refusal re-search, the revision, the dissolution) builds its request in one
   place, `usecases.searchRequest`, and sets `Request.NotBefore =
   promise.DeliverByEarliest`. `searcher.finish` then skips every delivery wave
   whose window ends before `NotBefore` and still takes the earliest wave of
   those left, so the group lands on the wave of the chosen day (or the first
   after it). A wave ending exactly at `NotBefore` is allowed. An unknown window
   start (zero, `deliverByEarliest` absent) is no bound, which keeps a group
   queued before ADR 0015 exactly as it was. When no wave ends inside the window
   the group is UNSOURCEABLE like any group with no candidate.
3. **The latest-pickup hold.** The same request sets `Request.HoldLatestPickup =
   true`. The comparator (`orderBy`) then puts, after the budget tier and
   `EffectiveCostCents` and BEFORE the legacy tie-break score, the option with the
   LATER `PickupCutoffAt`. Money still decides first; among options of equal
   money the group is released at the latest pickup that still reaches the chosen
   wave, and `releaseAt` follows the pickup cutoff, so the VirtualQueue holds the
   group until then. With the flag false every pair compares equal at this step,
   so the order of every other tier is the one of ADR 0011 and ADR 0014 (a test
   runs every fixture of the sourcing tests with the flag on and off and compares
   the orders where all cutoffs are equal).
4. **The merged search holds the same way.** `sourcing.Merge` builds the merged
   request with `HoldLatestPickup` set (a carrier is CONSOLIDATED by rule c1) and
   with `NotBefore` the latest window start of the members, so every member of a
   merged release rides a wave inside every member's window (ADR 0015, rule c3).
5. **Intake and revision.** `CustomerOrderPlaced.groups[].deliverByEarliest` and
   `PromiseRevised.deliverByEarliest` are decoded since ADR 0015; this slice
   changes no contract and no table (migration 0006 already holds
   `deliver_by_earliest`). The sweep is unchanged: a held group simply has a later
   `releaseAt`.
6. **No new REST field, event or metric.** The assignment view already shows the
   real tier and `promise.deliverByEarliest` (ADR 0015); a CONSOLIDATED candidate
   shows its wave and `eta` as every candidate does.

## Consequences

- A CONSOLIDATED group is released as late as its day allows. That shortens the
  time a shopper's second order has to find it still QUEUED (the merge window) and
  is the point of the tier.
- The hold sits BEFORE the legacy tie-break score. For a CONSOLIDATED group that
  means that when two options cost the same cents, the one with the later pickup
  wins even when the legacy score would have preferred the other (for example it
  is in-region, or the other node's book is under pressure). Money and the policy
  budget tier (ADR 0014) still come first, so a cheaper or an in-budget in-region
  option is never given up. Accepted: the hold is the feature; the legacy score is
  a tie-break of the tie-break.
- A group released at the latest pickup has no slack: if the vendor refuses or
  lets the purchase order expire, the re-search may find no later wave inside the
  window and the group goes UNSOURCEABLE where an eager release would still have
  had time. delivery-promise's `consolidationEligible` requires at least one local
  day of hold, which bounds the exposure (design constant, no policy field yet).
- Search cost is unchanged: the filter and the comparator step are constant-time
  per option.
- Not decided here (activation gates, outside order-sourcing): the storefront
  incentive and picker copy, and the four storefront tier validators, must accept
  `CONSOLIDATED` before any storefront asks for it.
