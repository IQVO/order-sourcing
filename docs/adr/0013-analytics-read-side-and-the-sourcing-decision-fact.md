# ADR 0013: Analytics read side and the sourcing-decision fact

## Status

Accepted (2026-11-09). Additive: no integration payload, OLTP table or REST
route of `cmd/api` changes. Adds one analytics-only event
(`FulfillmentSourcingDecided`), two binaries (`cmd/os-projector`,
`cmd/os-reports`), one analytical-database migration set
(`analytics/migrations`), four read-only report routes, two default-off chart
components and a second, separate database. Copies the shape of the
shipping-and-tracking pilot (its ADR 0012) and extends ADR 0004 (catalogue)
and ADR 0011 (the estimate semantics the new fact reuses).

## Context

Roadmap F9 asks three network questions of order sourcing as trends: how much
of the released demand is served from inside the destination's region and over
how many legs (and how often a group has to be released again), how often an
order is split into several shipment groups, and what serving a unit costs.
Nothing answers them today: the facts leave as CloudEvents, the OLTP tables
hold the current state only, and two of the facts are not on any event. The
destination's region and the released node's region are known only inside the
candidate search (`NetworkGraphView`: `rm_nodes.region_id` and the
`RegionAndServiceArea` row of the destination's postal prefix), and so are the
number of legs of the released path and the size of the candidate set. They
are decided by this context, so this context publishes them.

The shipping-and-tracking pilot (ADR 0012 there) built the read side as a
projector over the context's own topic, a separate analytical database and a
read-only reports binary. Unlike the pilot, order-sourcing already publishes
on `retailnetwork.order-sourcing.analytics` (`FulfillmentQueued`,
`FulfillmentDelivered`, ADR 0004), and the new fact joins them there.

## Decision

### 1. The new analytics-only fact

`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentSourcingDecided`
is published ONLY on `retailnetwork.order-sourcing.analytics`
(`dataschema urn:retailnetwork:order-sourcing:analytics:FulfillmentSourcingDecided:v1`),
in the SAME transaction as each `FulfillmentReleasedToNode`: the aggregate's
`Release` raises both (same aggregate, same version, subject and Kafka key =
`assignmentId`). It is a separate domain event with its own CloudEvents id,
minted at outbox append like every other, so no fan-out encoder is needed. The
outbox has no `UNIQUE` constraint on `event_id` (or on `(event_id, topic)`;
migration 0001 only indexes the unpublished rows), so the extra row needs no
OLTP migration.

`data` (camelCase, `?` = optional and omitted when unknown):

- `assignmentId`, `orderId`, `groupNo?`, `releaseNo`, `nodeId`;
- `nodeRegionId?` and `destinationRegionId?` (from the NetworkGraphView /
  RegionAndServiceArea read model: the released node's `regionId` and the
  region of the destination's service area; each omitted when unknown);
- `inRegion?` (bool, omitted when either region is unknown);
- `legs` (int: the departures of the released path after the pickup, ADR 0002
  A12 — 0 when the pickup goes straight to the destination's station);
- `estimatedCostCents?` and `costComplete` (bool) (ADR 0011 semantics: the
  cents are present exactly when the estimate is complete, i.e. when
  `costComplete` is true);
- `units` (sum of line quantities);
- `candidateCount` (candidates evaluated: the size of the ranked candidate set
  the release chose from, one per (node, pickup departure), at least 1);
- `decidedAt` (the release instant, RFC 3339 UTC).

It is listed in ADR 0004 (stream `analytics`), in `apis/asyncapi.yaml` and
pinned by an encoder golden file. It has no integration consumer; it is not on
`.events`.

### 2. What is projected

The projector `cmd/os-projector` consumes ONLY this context's own topics,
`retailnetwork.order-sourcing.events` and `retailnetwork.order-sourcing.analytics`,
under one consumer group (`order-sourcing-analytics`). It never reads a
sibling's topic. Three types are projected, dispatched on the FULL CloudEvents
`type`:

| type | topic | payload fields read | fact |
| --- | --- | --- | --- |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentQueued` | `.analytics` | `assignmentId` (= subject), `orderId`, `groupNo` | `queued_groups` (one row per assignment, day = CloudEvents `time`) |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentSourcingDecided` | `.analytics` | every field above | `sourcing_decisions` (one row per (assignment, releaseNo)) |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentUnsourceable` | `.events` | `assignmentId` (= subject), `orderId` | `unsourceable_declarations` (one row per event, day = CloudEvents `time`) |

The other as-built types are acknowledged and not projected (no report needs
them; the release itself is carried by its decision fact):
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode`,
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReassigned`,
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentLocked`,
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped`,
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentDelivered`,
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled`,
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelRefused`,
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentConstraintRevised`.

Tables of the analytical database (`analytics/migrations/0001_sourcing_facts`):
`analytics_processed_events (event_id PK, event_type, occurred_at, applied_at)`
(the dedupe set; `occurred_at` is the CloudEvents `time`, the basis of the
freshness lag), `queued_groups`, `sourcing_decisions` and
`unsourceable_declarations`. The two topics are independent partitions, so the
model is order-independent: it stores facts and the reports join them at query
time.

### 3. KPI contract

Days are UTC calendar days. Range rules as in the pilot: `from` and `to` are
UTC dates (`YYYY-MM-DD`), both inclusive; both omitted = the 30 days ending
today; only `to` = the 30 days ending on `to`; only `from` = `from` .. today;
`from` after `to` or a range over 366 days is a 400 problem
(`invalid-report-range`). Empty results are empty arrays. A share, rate or mean
whose denominator is 0 is `null`.

**`GET /reports/sourcing-mix?from=&to=`** (UTC day of release, i.e. of
`decidedAt`), per day:

- `groupsReleased` = the releases (sourcing decisions) of that day;
- `inRegionShare` = decisions with `inRegion` true / (true + false), with an
  unknown-region count `unknownRegionCount` (decisions without `inRegion`);
- `meanLegs` = sum of `legs` / `groupsReleased`;
- `reassignmentRate` = releases with `releaseNo` > 1 / `groupsReleased`;
- `unsourceableCount` = the `FulfillmentUnsourceable` events of that day (day
  of their CloudEvents `time`).

A day appears when it has a release or an unsourceable declaration.

**`GET /reports/order-splits?from=&to=`** (UTC day of `FulfillmentQueued`):
`ordersQueued` (distinct `orderId`), `groupsPerOrder` mean (queued groups of
those orders / `ordersQueued`), `splitOrderShare` = orders with > 1 group /
`ordersQueued`. An order belongs to the day its first group was queued (one
`QueueAssignment` queues every group of an order in one transaction).

**`GET /reports/cost-to-serve?from=&to=`** per day: `costCompleteShare`
(complete decisions / decisions); `costPerUnitCents` = sum(`estimatedCostCents`
of complete decisions) / sum(`units` of complete decisions) (`null` when
undefined); `p50CostCentsPerGroup` / `p90CostCentsPerGroup` = the continuous
percentiles (`percentile_cont`) of `estimatedCostCents` per group over the
complete decisions (`null` without one). It uses the LAST decision per
assignment (highest `releaseNo`), bucketed on that decision's `decidedAt`.
`estimatedCostCents` is per package-unit as ADR 0011 point 3 defines it.

**`GET /reports/freshness`**: `asOf` = the newest CloudEvents `time` the
projection applied, `lagSeconds` = now - `asOf` (never negative); both `null`
until the first event.

SQL only counts, sums and takes `percentile_cont`. The shares, rates, means,
the range rules, the percentile ranks and the freshness lag live in
`internal/analytics/report`, a pure package that imports no other internal
package (arch test), is in the coverage scope and in `make mutation`. ONE
contract suite runs against the in-memory twin and against real Postgres. The
routes live in `apis/openapi.yaml` under the tag `reports` with a per-path
server on `:8092`; wire fields are camelCase (ADR 0010), the routes are
collection-shaped reports (`x-pagination-exempt: report`).

### 4. Delivery policy (the projector)

- Consumer group `ANALYTICS_CONSUMER_GROUP` (default `order-sourcing-analytics`),
  fixed and stable; at-least-once, the offset is committed only after the
  message was handled. A brand-new group starts at the earliest offset, so the
  model rebuilds from retained history.
- Dedupe and projection in ONE analytical-database transaction:
  `INSERT ... ON CONFLICT DO NOTHING` into `analytics_processed_events`; the
  fact is written only if that insert claimed the id.
- Not a CloudEvents 1.0 message, or a type that is neither projected nor
  acknowledged: skipped and committed past with a sampled WARN (the first, then
  at most one per minute with the count suppressed).
- Dead-letter ONLY deterministic poison (an undecodable payload, a subject that
  is not the `assignmentId`, a missing or inconsistent required field, a store
  rejection in Postgres class 22 or 23) to
  `retailnetwork.order-sourcing.analytics.dlq` with `x-dlq-*` headers, then
  commit. A failed DLQ write is retried, so poison is never lost.
- Transient failures are retried forever with capped exponential backoff
  (200ms doubling to 5s) and never dead-lettered.

### 5. The binaries

- `cmd/os-projector`: the ONLY writer of the analytical database. Env:
  `ANALYTICS_DATABASE_URL` (required, a direct DSN: it runs the embedded
  migrations), `KAFKA_BROKERS` (required), `ANALYTICS_CONSUMER_GROUP`,
  `ADMIN_ADDR` (default `:8091`, `/healthz` and `/readyz`), `LOG_LEVEL`,
  `OTEL_SERVICE_NAME` (default `order-sourcing-projector`),
  `OTEL_EXPORTER_OTLP_ENDPOINT`, `SERVICE_VERSION`, `ENVIRONMENT`. It never
  opens the OLTP database.
- `cmd/os-reports`: read-only HTTP on `HTTP_ADDR` (default `:8092`):
  `GET /reports/sourcing-mix`, `GET /reports/order-splits`,
  `GET /reports/cost-to-serve`, `GET /reports/freshness`, `/healthz`, `/readyz`.
  It reads `ANALYTICS_READER_DATABASE_URL`, falling back to
  `ANALYTICS_DATABASE_URL` for single-role local runs (the chart renders only
  the reader DSN), through a pool whose sessions default to read-only
  transactions with a 15s statement timeout. No Kafka, no OLTP database, no
  call to any sibling, no auth (fleet rule). Errors are problem+json.
- Both ship in the one image (`/app/os-projector`, `/app/os-reports`).

### 6. Packaging

The chart gains an opt-in `analytics:` block (default `enabled: false`; the
default render is byte-identical): components `analytics-projector`
(Deployment, no Service) and `analytics-reports` (Deployment + ClusterIP
Service, an opt-in Kong HTTPRoute PathPrefix `/api/order-sourcing/reports`
rewritten to `/reports`), an `analytics` Secret or
`analytics.database.existingSecret`. Render-time guards refuse
`analytics.enabled` without a DSN source, without `kafka.brokers` or without a
consumer group.

## Consequences

- Every release writes one more outbox row (the decision fact) to
  `.analytics`; the integration contract on `.events` is unchanged.
- The two topics gain one service-owned consumer group
  (`order-sourcing-analytics`). The reports are only as fresh as the projector
  (`/reports/freshness`).
- Releases published before this fact existed have no decision, so they are
  absent from the sourcing mix and the cost-to-serve; the model starts at the
  earliest retained offset.
- `groupsReleased` counts releases: a group released again after a refusal
  counts once per release, which is what `reassignmentRate` measures.
- A deploy needs a second database, two roles (projector read-write, reports
  read-only), the DLQ topic, both source topics pre-created and the consumer
  group above (`warehouse-infra`).

## Alternatives considered and rejected

- **Derive the regions in the projector from node-topology's topic**: a
  sibling's topic and a second copy of the network graph; the region facts are
  decided here, at release, and the projection would see them only eventually.
- **Add the fields to `FulfillmentReleasedToNode`**: widens an integration
  contract for analytics-only facts (`conventions.analytics_rule`).
- **Fan `FulfillmentReleasedToNode` out to `.analytics`**: needs a fan-out
  encoder for facts that are not on it anyway.
- **Dead-letter after N attempts for every failure**: drops analytics for
  events that failed only because the database was briefly down.
