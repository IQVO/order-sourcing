# ADR 0012: Owner-aware availability and sourcing

## Status

Accepted (2026-11-08). Refines the `AvailabilityView` of ADR 0003 and the
`FulfillmentReleasedToNode` payload of ADR 0004.

## Context

offer-availability now allows more than one listing of one catalogue item at
one node: one per (catalogue item, node, owner), where the owner is a seller
(`ownerRef` = sellerId) or the operator (`ownerRef` omitted). `ListingCreated`
and `ListingAvailabilityChanged` carry the optional `ownerRef`;
`ListingSuppressed` and `ListingRestored` address the listing by `listingId`
only (offer-availability `apis/asyncapi.yaml`, its ADR 0006 and migration 0002).

Until now this context read availability by (`catalogItemId`, `nodeId`) only:
`sourcing.ResolveLines` took the FIRST listing of the item at the node, so with
two owners' listings of one item at one node the rows collided (whichever
listing sorted first won) and nothing said whose stock a group takes. Three
alternatives were weighed:

- **Sum the owners' quantities at the node.** Rejected: one line would then be
  fulfilled from two owners' stock, which no purchase order can express (one
  purchase-order line has one owner) and which hides whose inventory is spent.
- **Ignore seller listings.** Rejected: it throws feasible nodes away and
  makes seller stock invisible to the network.
- **Pick an owner per line by one deterministic rule shared with
  delivery-promise.** Chosen: the promise and the sourcing decision then agree
  on which node can ship and from whose stock, and the released event says it.

The contract below was fixed by the orchestrator for order-sourcing,
delivery-promise and vendor-connectivity together (network gap G47).

## Decision

The contract, verbatim:

1. The local availability read model is keyed by (catalogItemId, nodeId, ownerRef), with operator-owned stored as ownerRef '' (empty string, never NULL in a key). It is fed from ownerRef on the consumed listing events: absent means ''. Suppression/restore state, if kept, is per key too. Write a migration with the next free number: the backfill puts existing rows at ownerRef ''. Update the migration tests' version ceilings and step counts.
2. Line feasibility at a node: the line qty must be covered by ONE owner (availableQty minus any netting this context applies, per key). A line never spans owners; summing across owners is forbidden.
3. OwnerChoice rule (deterministic, shared with delivery-promise):
   - operator-owned ('') first, if it covers the line;
   - otherwise the covering seller owner with the highest availableQty;
   - ties go to the lowest ownerRef in byte order;
   - no covering owner means the node is not a candidate for that line (the existing exclusion reason path).
   It is a pure function in the domain with table tests (as a slice).
4. The chosen owner is decided per line at the node actually released. AssignmentLine (or the equivalent line value object) persists `ownerRef` (four-places rule + repocontract). It is recomputed on every re-optimisation, and the value at release time is the one published.
5. FulfillmentReleasedToNode.data.lines[] gains optional `ownerRef` (string, omitted when operator-owned). No other event changes. Update apis/asyncapi.yaml with a 'Consumers: vendor-connectivity (lines[].ownerRef)' note, the CloudEvents ADR if it lists payload fields, and the REST GET /assignments line schema (optional ownerRef) plus regenerated REST docs and MCP golden if outputs change.

How this context applies it:

1. **The key.** `rm_listings` gains `owner_ref text COLLATE "C" NOT NULL DEFAULT ''`
   (migration `0004_rm_listings_owner_ref`; the default backfills every existing
   row at ''), and a UNIQUE index on (`catalog_item_id`, `node_id`, `owner_ref`)
   for every row whose item is known. The `listing_id` stays the row's primary
   key because the suppression events address a listing by id only and may
   arrive before `ListingCreated` (a stub with an unknown item, exactly as
   before); offer-availability keeps one listing per key, so a listing id and
   a key are one-to-one and the suppression state of a row is the suppression
   state of its key. `ListingCreated` and `ListingAvailabilityChanged` set the
   row's owner from `ownerRef`, absent meaning ''.
2. **Feasibility.** This context applies no netting: the covering quantity of
   a key is its `availableQty`, and a suppressed key covers nothing.
3. **The rule.** `sourcing.OwnerChoice` (`internal/domain/sourcing/owner.go`)
   is the pure OwnerChoice rule above over the listings of one item at one
   node. `sourcing.ResolveLines` applies it to every line: a node where one
   line has no covering owner is not a candidate (the search drops it, and an
   empty candidate set is `UNSOURCEABLE` with `NO_CANDIDATE` as before).
   delivery-promise applies the same rule, so the promise and the sourcing
   decision agree on which node can ship a line and from whose stock.
4. **The line.** `assignment.Line` carries `OwnerRef` next to `ListingID`.
   Every candidate search (queue, re-optimisation, release re-check) runs the
   rule again; the lines the aggregate keeps and publishes are the ones
   resolved at the node actually released (`Assignment.Release`), so a
   re-release after a refusal recomputes them.
5. **The wire.** `FulfillmentReleasedToNode.data.lines[]` gains optional
   `ownerRef`, omitted when operator-owned; `FulfillmentQueued` and every
   other event are unchanged. The REST and MCP line views carry the same
   optional field under the same rule.

## Consequences

- A line is never sourced across owners: two owners with 3 units each at one
  node do not make the node a candidate for a line of 5.
- vendor-connectivity learns from `lines[].ownerRef` whose stock the purchase
  order draws on; an older consumer ignores the field.
- The rule prefers operator stock even when a seller has more, so seller stock
  is used only where the operator's does not cover the line.
- A producer that ever broke the one-listing-per-key rule would make the
  consumer's write fail on the unique index and the message dead-letter,
  instead of silently overwriting another owner's stock.
- The rule is duplicated in delivery-promise by design (no shared library
  across contexts); this ADR and delivery-promise's twin name it verbatim, so
  a change of the rule is a change of both.
