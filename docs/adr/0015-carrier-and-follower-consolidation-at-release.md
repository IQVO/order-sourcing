# ADR 0015: carrier and follower consolidation at release

## Status

Accepted (2026-11-10). Extends ADR 0002 (the aggregate), ADR 0004 (catalogue)
and ADR 0014 (the sourcing policy). Additive and default-off: until an
operator publishes `consolidationMaxAssignments >= 2` and customer-ordering
sends a `consolidationKey`, nothing merges and every event, view and ordering is
unchanged. ADR 0016 (the CONSOLIDATED delivery day) is stacked on this one.

## Context

The identifier model downstream of a release is one assignment = one group = one
promise = one purchase order = one tracking number. A shopper who places two
orders to the same address within a few days still costs two pickups, two
purchase orders and two packages. The product wants those packages merged, but
merging may not break the 1:1 model of the consumers (vendor-connectivity,
linehaul-transportation, delivery-promise, customer-ordering all key their work
by the assignment id), and it may not be a blocker: a merge is an optimisation.

Alternatives weighed:

- A separate `Consolidation` aggregate (own table, repository, memory twin,
  contract cases). Rejected: heavier, and the invariants fit inside the
  assignment rows that `QueueAssignment` already saves in one unit of work.
- One `FulfillmentLocked` listing every merged assignment id. Rejected: delivery
  promise and customer ordering would have to expand members, and linehaul would
  book N packages for it. Each member keeps its own events instead.
- Moving the carrier to another node to fit more followers. Rejected: the merge
  never moves the carrier; a follower that does not fit at the carrier's node
  releases alone.

## Decision

1. **Vocabulary.** The *carrier* is the QUEUED assignment whose release is due
   (the one the sweep is releasing). *Followers* are other QUEUED assignments
   absorbed into that release. A *member* is a carrier or a follower. A merge
   exists only from release until the end of the members' life or until it is
   dissolved; before release nothing is merged, so cancel and revise of a QUEUED
   group are unchanged.
2. **Aggregate (four places, migration 0006).** `Snapshot`, `Queue`/`New`,
   `Restore` and the `Snapshot()` builder gain
   - `PromiseConstraint.DeliverByEarliest` (zero = unknown; when set it is not
     after `DeliverByLatest`),
   - `consolidationKey` (`""` or 64 lower-case hex characters),
   - `consolidatedInto` (a follower only; the follower is RELEASED, LOCKED,
     SHIPPED or DELIVERED and its candidate is a copy of the carrier's),
   - `members` (a carrier only: follower ids, ascending, non-empty exactly while
     the carrier is the carrier of a merge; a follower has none).
   `Outcome` gains `WITHDRAWN`. Migration `0006_consolidation` adds
   `deliver_by_earliest`, `consolidation_key`, `consolidated_into`, `members` and
   a partial index on the QUEUED rows that have a key.
3. **Compatibility (pure `sourcing.Merge`).** A candidate joins the carrier,
   greedily in `deliverByLatest` then id order, only if all of these hold:
   - **c1 identity:** not the carrier, QUEUED, the same non-empty
     `consolidationKey`, the same destination postal prefix, and the tiers of the
     carrier and of the candidate are both `CONSOLIDATED`. SAME_DAY, NEXT_DAY,
     TWO_DAY and STANDARD never merge (product decision Q2: only orders whose
     shopper chose the consolidated delivery day merge).
   - **c2 node:** the carrier's solo best node N is not excluded by the
     candidate and the merged search returns an option at N. The merge never
     moves the carrier.
   - **c3 overlap:** the merged ETA lies inside `[max(deliverByEarliest),
     min(deliverByLatest)]` of the trial member set. `sourcing.Request` gains
     `NotBefore`; `finish` skips waves ending before it (zero = no bound, so every
     existing search is byte-identical).
   - **c4 stock:** lines are grouped by catalogue item, quantities are summed and
     the OwnerChoice rule (ADR 0012) must pick one covering owner for each sum;
     every member line of that item takes that listing and owner. Owners may
     differ between items.
   - **c5 caps:** the member count is at most `consolidationMaxAssignments` and
     the union at most `assignment.MaxLines`.
   When no candidate fits the carrier releases alone, exactly as before.
4. **Policy lever.** `SourcingPolicy` gains `consolidationMaxAssignments`
   (integer 0..10, default 0; 0 and 1 both mean off) in the aggregate, the
   `GET/PUT /sourcing-policy` body, `SourcingPolicyPublished` and the
   repocontract. The policy is jsonb, so no DDL; an old row restores 0. The PUT
   stays whole-document (ADR 0014): a PUT that omits the field switches
   consolidation off.
5. **Release.** `ReleaseToNode` runs in one unit of work. When the policy is on
   and the carrier has a key and is CONSOLIDATED it reads `QueuedByKey` (new port
   method, ordered by `deliver_by_latest`, id; at most twice the cap), loads the
   union world once and calls `sourcing.Merge`. With at least one follower the
   carrier calls `ReleaseConsolidated` and each follower `ReleaseAsFollower`;
   each is saved with the optimistic version check, so two concurrent releases are
   safe (the loser re-runs once and finds the follower already RELEASED).
   Otherwise the carrier calls `Release` as before.
6. **Fan-out on the carrier id** (the id the vendor side knows). `LockAssignment`,
   `MarkShipped`, `MarkDelivered` and `RecordPickupMoved` apply the same command
   to each follower in the same transaction. Each member raises its own
   `FulfillmentLocked`, `FulfillmentShipped` and `FulfillmentDelivered`, so
   delivery promise, customer ordering and customer payments keep working per
   group. A repeat on a locked member is a no-op.
7. **Tier acceptance sits HERE, not in ADR 0016.** Rule c1 needs a CONSOLIDATED
   assignment to exist, so `assignment.Tier` accepts the fifth value in this
   slice, with `FulfilmentClass()` (CONSOLIDATED maps to STANDARD) so that
   `FulfillmentReleasedToNode.tier` never carries the fifth value to vendor
   connectivity and shipping. ADR 0016 adds the day-honouring search levers
   (`NotBefore` for every CONSOLIDATED search and the latest-pickup hold).
8. **Cancel of a merged member is REFUSED (product decision Q1).** While a merge
   is live and not yet LOCKED (carrier or follower, state RELEASED),
   `CancelAssignment` applies the refusal path it already uses after the lock:
   `RefuseCancel` and `FulfillmentCancelRefused`, the state does not change, the
   order side keeps the group, nothing is withdrawn and the merge stays intact.
   The only new thing on the wire is the refusal reason value
   `CANCEL_WHILE_MERGED` (additive; the reason is free text for the consumer).
   The cancel can be retried after the carrier's lock (then it is refused as
   `CANCEL_AFTER_LOCK`, as today) or after a vendor refusal or a revision
   dissolved the merge (then the group is QUEUED again and cancels). A cancel of
   a QUEUED group or of an unmerged RELEASED group is unchanged. Counter
   `ordersourcing_merged_cancel_refused_total`.
9. **Dissolution.** There is no `MEMBER_CANCELLED` reason (Q1).
   - *Vendor refusal or expiry of the carrier's release:* the carrier takes the
     existing `RefuseRelease`; each follower takes `RefuseRelease` with its own
     pending release number. All exclude the node and are re-searched
     independently (they may re-merge at their next release). Raises
     `AssignmentsDeconsolidated` with reason `PURCHASE_ORDER_REJECTED` or
     `PURCHASE_ORDER_EXPIRED`. No withdraw event: the vendor side already holds the
     terminal state.
   - *Promise revision of a RELEASED member:* applied as today. If the new window
     excludes the merged ETA (after the new latest, or before the new earliest
     when known) the merge dissolves with reason `PROMISE_REVISED`: raises
     `FulfillmentReleaseWithdrawn` (vendor connectivity cancels the issued
     purchase order), every member's pending release gets outcome `WITHDRAWN`, every
     member returns to QUEUED with `consolidatedInto` and `members` cleared and is
     re-searched with NO node exclusion (`FulfillmentReassigned` with reason
     `CONSOLIDATION_WITHDRAWN` only if its node changed), then
     `AssignmentsDeconsolidated`. A due member is released again by the next sweep
     tick, alone or re-merged with whatever is QUEUED then (a merge only happens at
     a release, so there is no loop). A stale revision stays ignored.
   - *After the lock:* nothing dissolves; cancel is refused per member as today.
10. **Events** (catalogue in ADR 0004, asyncapi; all additions optional):
    new `AssignmentsConsolidated` and `AssignmentsDeconsolidated` (analytics) and
    `FulfillmentReleaseWithdrawn` (integration, subject = carrier id); changed
    `FulfillmentReleasedToNode` (`members`, per-line `assignmentId`, union lines in
    member order, tier = fulfilment class, `promisedDeliverByLatest` = minimum),
    `FulfillmentLocked` (`consolidatedInto`, `mergedAssignmentIds`),
    `FulfillmentSourcingDecided` (`carrierAssignmentId`, `consolidatedCount`),
    `FulfillmentReassigned.reason` (`CONSOLIDATION_WITHDRAWN`) and
    `FulfillmentCancelRefused.reason` (`CANCEL_WHILE_MERGED`). Withdraw reason enum:
    `PROMISE_REVISED`. Deconsolidated reason enum: `PROMISE_REVISED |
    PURCHASE_ORDER_REJECTED | PURCHASE_ORDER_EXPIRED`. Absent `members` means a
    single-group release; absent `consolidationKey` means the order never merges.
    The analytics projection counts the cost of a merged package once, on the
    carrier.
11. **Consumed.** `CustomerOrderPlaced` is decoded for the optional top-level
    `consolidationKey` and `groups[].deliverByEarliest`; `PromiseRevised` for the
    optional `deliverByEarliest`. Absence means "never merges" and "unknown".
12. **REST, MCP, A2A (additive).** The assignment view gains `consolidatedInto`,
    `consolidatedWith` (on a carrier: its followers), `promise.deliverByEarliest`;
    the release `outcome` enum gains `WITHDRAWN`; the `tier` enum gains
    `CONSOLIDATED`. `consolidationKey` is NOT exposed. No new route.
13. **Metrics and logs.** `ordersourcing_consolidated_releases_total`,
    `ordersourcing_consolidated_assignments_total` (followers absorbed),
    `ordersourcing_consolidation_withdrawn_total{reason}`,
    `ordersourcing_merged_cancel_refused_total`,
    `ordersourcing_stale_accept_total`; one log line per merge and per
    dissolution with carrier id, member ids, node and reason.

## Consequences

- Behaviour is unchanged until `consolidationMaxAssignments >= 2` is published
  and keyed CONSOLIDATED groups exist. Switching the policy off later has no
  effect on live merges; only future releases stop merging.
- **Residual race (promise revision only).** After Q1 the only way to withdraw a
  published release is a promise revision. In the instant between order-sourcing
  dissolving and vendor connectivity cancelling, the vendor may acknowledge the
  purchase order. `Lock` then returns `ErrReleaseNotPending`, which the consumer
  acknowledges and `ordersourcing_stale_accept_total` counts; the vendor holds an
  accepted purchase order for the old union while the survivors also get a new
  one, a duplicate pick and ship of the survivors' goods. The window is one Kafka
  hop and exists today for a plain cancel after release. Cancel no longer opens
  it.
- A shopper cannot cancel a merged group between the merge and the vendor's
  acknowledgement (minutes to hours); the order side keeps the group and may
  retry.
- A merged package charges one capacity unit per promise at delivery promise
  while linehaul books one package: a deliberate conservative over-charge, stated
  in ADR 0025 of that repo.
- The vendor API is unchanged: a purchase order already has lines, and the lines
  of a merged release are positional like any other.
- **Activation gates (deploy).** Keep the policy at 0 until vendor connectivity
  consumes the withdraw, linehaul skips follower locks, and delivery promise and
  customer ordering accept a tracking number shared by several groups. No
  environment variable, topic or chart value changes: the analytics events use the
  existing analytics topic and the withdraw uses the events topic. Customer
  ordering needs its own secret to derive the key (its ADR).
