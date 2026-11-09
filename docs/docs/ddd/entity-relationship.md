---
id: entity-relationship
title: Entity relationship
sidebar_label: Entity relationship
description: "The Postgres schemas of Order Sourcing as migrated: the OLTP assignments table, the rm_ read-model tables fed by sibling events, the outbox, inbox and idempotency tables, and the separate analytical database."
---

# Entity relationship

The schema of this context's own Postgres database ("every context owns its database; no shared tables"), as created by
the embedded migrations in `internal/adapters/outbound/postgres/migrations/`, applied by golang-migrate at every boot:
`0001_order_sourcing_schema` creates all **14 tables**; `0002_idempotency_global_key`, `0003_rm_lane_wave_cost` and
`0004_rm_listings_owner_ref` only alter existing tables (none creates or drops a table). There are no foreign keys: the aggregate
is one row with `jsonb` columns, the read models are projections that can be dropped and replayed, and every id of another
context is a plain value.

A second, separate **analytical database** (ADR 0013) has its own migration, `analytics/migrations/0001_sourcing_facts.up.sql`,
with **4 tables**; it is described in [Analytical database](#analytical-database) at the end of this page.

## FulfillmentAssignment (one table)

```mermaid
erDiagram
    assignments {
        text assignment_id PK
        text order_id UK
        integer group_no UK
        text destination_postal_prefix
        text promise_id
        text tier
        timestamptz deliver_by_latest
        text witness_node_id
        integer revision_no
        jsonb lines
        text state
        text node_id
        jsonb candidate
        timestamptz release_at
        jsonb releases
        jsonb excluded_nodes
        bigint version
        timestamptz queued_at
        timestamptz updated_at
        timestamptz locked_at
        text lock_reason
        text pickup_departure_id
        jsonb tracking_numbers
        timestamptz shipped_at
        jsonb delivered
        timestamptz delivered_at
        text cancel_reason
    }
```

Source: the `assignments` table of the migration and `internal/domain/assignment/snapshot.go` (`Snapshot`).

Column notes:

- `(order_id, group_no)` is one unique key (`assignments_order_group_key`): one assignment per shipment group, and the reason a
  repeated `CustomerOrderPlaced` is a no-op. A violation is `ports.ErrDuplicateGroup`.
- `version` (CHECK at least 1) is the optimistic lock: every save expects the stored version to be the aggregate's version minus
  one (`ports.ErrConcurrentUpdate` otherwise). It is the `assignmentVersion` of the API.
- `lines`, `candidate`, `releases`, `excluded_nodes`, `tracking_numbers` and `delivered` are JSON documents that belong to the
  aggregate: lines, the current candidate (node, pickup departure and cutoff, legs, wave, eta, cost), the release history with each
  outcome, the exclusion list, the shipment's tracking numbers and the delivered packages. There are no child tables.
- `node_id` is the node of the candidate while `QUEUED` and of the latest release afterwards; it is what `GET /assignments?nodeId=`
  filters on. `state` is not constrained by a CHECK; the domain validates it.
- `destination_postal_prefix` is a postal prefix, not an address: no PII is stored here.
- Indexes: `assignments_promise_idx (promise_id)` for `FindByPromise`; `assignments_state_node_idx (state, node_id)` for the list
  filters; `assignments_queue_idx (release_at, assignment_id) WHERE state = 'QUEUED'` for the release timer and the
  re-optimisation sweep.

## Read-model tables

Fed only by sibling events, rebuilt by replaying their topics (an older instant or a lower revision never overwrites a newer
one). The five views map to ten tables.

```mermaid
erDiagram
    rm_nodes {
        text node_id PK
        text node_type
        text subtype
        text operator
        text region_id
        text status
        timestamptz status_at
        integer profile_revision
        integer handling_minutes
        integer sort_dwell_minutes
    }
    rm_scorecards {
        text node_id PK
        text score_window PK
        text grade
        timestamptz computed_at
    }
    rm_book_pressure {
        text book_id PK
        text node_id
        text pickup_departure_id
    }
```

```mermaid
erDiagram
    rm_lanes {
        text lane_id PK
        text origin
        text destination
        text mode
        boolean closed
        bigint cost_per_unit_cents "default 0, added by 0003"
        timestamptz cost_declared_at "NULL = no declaration"
    }
    rm_departures {
        text departure_id PK
        text lane_id
        text kind
        text origin
        text destination
        timestamptz cutoff_at
        timestamptz departs_at
        timestamptz arrives_at
        integer capacity
        integer remaining
        boolean remaining_known
        boolean scheduled
        boolean cancelled
    }
    rm_waves {
        text wave_id PK
        text station_id
        timestamptz induct_cutoff_at
        timestamptz window_start
        timestamptz window_end
        integer capacity
        integer remaining
        boolean remaining_known
        boolean scheduled
        boolean cancelled
        bigint cost_per_stop_cents "default 0, added by 0003"
        timestamptz cost_declared_at "NULL = no declaration"
    }
    rm_service_areas {
        text postal_prefix PK
        text station_id
        text region_id
        integer version
    }
    rm_regions {
        text region_id PK
        text code
        text name
    }
```

```mermaid
erDiagram
    rm_listings {
        text listing_id PK
        text catalog_item_id
        text node_id
        integer available_qty
        timestamptz as_of
        boolean suppressed
        text owner_ref "default empty = operator-owned, added by 0004"
    }
    rm_discontinued_items {
        text catalog_item_id PK
    }
```

Source: the `rm_*` tables of `0001_order_sourcing_schema.up.sql`, plus the columns added later: `cost_per_unit_cents` and `cost_declared_at` on `rm_lanes` and `cost_per_stop_cents` and `cost_declared_at` on `rm_waves` (`0003_rm_lane_wave_cost.up.sql`; `cost_declared_at` NULL means no declaration was seen, so the component contributes 0 to a cost estimate and marks it incomplete) and `owner_ref` on `rm_listings` (`0004_rm_listings_owner_ref.up.sql`; `''` is operator-owned, and the unique index `rm_listings_item_node_owner_key` on `(catalog_item_id, node_id, owner_ref) WHERE catalog_item_id <> ''` keys the availability view by item, node and owner while `listing_id` stays the primary key). The projection rules are in `internal/domain/network/network.go` and
`internal/application/usecases/projector.go`.

| View | Tables | Event source |
| --- | --- | --- |
| `NodeProfileView` | `rm_nodes` | `node-topology` node events |
| `NetworkGraphView` | `rm_lanes`, `rm_departures`, `rm_waves`, `rm_service_areas`, `rm_regions` | `node-topology` (lanes, service areas, regions), `linehaul-transportation` (departures), `last-mile-delivery` (waves) |
| `AvailabilityView` | `rm_listings`, `rm_discontinued_items` | `offer-availability` |
| `ScorecardView` | `rm_scorecards` (`score_window` stands for the scorecard's `window`, a reserved word) | `node-performance`; only the `30d` window is used by the cost |
| `PromiseBookPressureView` | `rm_book_pressure` | `delivery-promise` `PromiseBookExhausted` |

`remaining_known` marks that `remaining` holds a real value: the schedule's capacity or a capacity event, and a later schedule
keeps the remaining value a capacity event already set. A row is usable only if `scheduled`, not `cancelled` and `remaining` is
above 0; a cancellation that arrives before the schedule is kept.

## Infrastructure tables

```mermaid
erDiagram
    outbox_events {
        bigserial id PK
        text event_id
        text topic
        text event_type
        text subject
        bytea key
        text dataschema
        bytea value
        jsonb headers
        integer attempts
        text last_error
        timestamptz created_at
        timestamptz published_at
    }
    processed_events {
        text event_id PK
        text event_type
        timestamptz processed_at
    }
    idempotency_keys {
        text key PK
        text fingerprint
        integer status
        bytea body
        jsonb headers
        timestamptz created_at
    }
```

Source: `0001_order_sourcing_schema.up.sql`, with `idempotency_keys` as reshaped by `0002_idempotency_global_key.up.sql`. All three are infrastructure, not domain tables:

- `outbox_events` is written in the same transaction as the aggregate and drained by the relay. `value` is the already-encoded
  CloudEvents message, `event_id` its stable CloudEvents `id`; a partial index serves the unpublished rows by `id`. `attempts` and
  `last_error` support the attempt cap ([Runbook](/docs/operations/runbook#outbox-relay)).
- `processed_events` is the inbox: one row per consumed CloudEvents `id`, written in the same transaction as its effect.
- `idempotency_keys` stores the first answer of `POST /assignments/{id}/reoptimisations` per `key` (global), with the request fingerprint (method + path + SHA-256 of the body), ignored after 24 hours. Migration `0002` moved the key from `(route, key)` to `key` alone.

golang-migrate also keeps its own bookkeeping table (`schema_migrations`), created by the tool and not by these migrations.

## No foreign key across aggregates or contexts

- `node_id`, `witness_node_id`, `promise_id`, `order_id`, `catalog_item_id`, `listing_id` and `pickup_departure_id` name entities
  of other contexts, which live in other databases, so they are values. A node retired in `node-topology` must not block a write
  here.
- The `rm_` tables are not joined to the aggregate by constraints either: a constraint would make a replay order-dependent.
- There is only one aggregate, so no cross-aggregate key exists.

## Table is not aggregate

| Table | Role |
| --- | --- |
| `assignments` | **FulfillmentAssignment** root, with its lines, release history and exclusions embedded as JSON |
| `rm_nodes`, `rm_lanes`, `rm_departures`, `rm_waves`, `rm_service_areas`, `rm_regions`, `rm_listings`, `rm_discontinued_items`, `rm_scorecards`, `rm_book_pressure` | read models; no invariant of this context |
| `outbox_events`, `processed_events`, `idempotency_keys` | infrastructure |

## Analytical database

The analytical database is separate from the OLTP one, written only by `cmd/os-projector` and read (read-only) by `cmd/os-reports` (ADR 0013). It is a projection of this context's own two topics (`retailnetwork.order-sourcing.events` and `.analytics`), not a source of truth, and can be dropped and rebuilt from them. It has no foreign keys; the dotted lines are shared `assignment_id` values, not constraints.

```mermaid
erDiagram
    queued_groups ||..o{ sourcing_decisions : "assignment_id"
    queued_groups ||..o{ unsourceable_declarations : "assignment_id"

    analytics_processed_events {
        text event_id PK
        text event_type
        timestamptz occurred_at "CloudEvents time"
        timestamptz applied_at "default now()"
    }
    queued_groups {
        text assignment_id PK
        text order_id
        integer group_no "1 or more"
        timestamptz queued_at "CloudEvents time"
        text event_id
    }
    sourcing_decisions {
        text assignment_id PK
        integer release_no PK
        text order_id
        integer group_no "NULL when omitted"
        text node_id
        text node_region_id "NULL when unknown"
        text destination_region_id "NULL when unknown"
        boolean in_region "NULL when unknown"
        integer legs "0 or more"
        bigint estimated_cost_cents "present exactly when cost_complete"
        boolean cost_complete
        integer units "1 or more"
        integer candidate_count "1 or more"
        timestamptz decided_at
        text event_id
    }
    unsourceable_declarations {
        text event_id PK
        text assignment_id
        text order_id
        timestamptz declared_at "CloudEvents time"
    }
```

Source: `analytics/migrations/0001_sourcing_facts.up.sql`; the projection is `internal/adapters/outbound/analyticsstore/projection.go` and the reports are `internal/adapters/outbound/analyticsstore/reader.go`.

- `queued_groups`: one row per assignment, from `FulfillmentQueued` (analytics topic); `idx_queued_groups_order` on `order_id` serves the order-split report.
- `sourcing_decisions`: one row per release, from `FulfillmentSourcingDecided` (analytics topic), keyed `(assignment_id, release_no)`; the check `sourcing_decisions_cost_complete` makes `cost_complete` equal `estimated_cost_cents IS NOT NULL` (ADR 0011); index `idx_sourcing_decisions_decided_at`.
- `unsourceable_declarations`: one row per `FulfillmentUnsourceable` (events topic), on its CloudEvents time; index `idx_unsourceable_declarations_declared_at`.
- `analytics_processed_events`: the projector's inbox. Every applied CloudEvents id is recorded once, in the same transaction as its effect on a fact table (`projection.go` claims the id, then projects); `occurred_at` is the basis of `/reports/freshness`; index `idx_analytics_processed_events_occurred_at`.

Down migration: `0001_sourcing_facts.down.sql` in the same directory.
