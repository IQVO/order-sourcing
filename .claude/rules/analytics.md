---
paths:
  - "cmd/os-projector/**"
  - "cmd/os-reports/**"
  - "analytics/**"
  - "internal/analytics/**"
  - "internal/adapters/outbound/analyticsstore/**"
  - "internal/adapters/inbound/kafka/analytics_*.go"
  - "internal/adapters/inbound/reportshttp/**"
---

# Analytics read side (projector + reports)

Record: `docs/adr/0013-analytics-read-side-and-the-sourcing-decision-fact.md`. The read side consumes the facts this
context already publishes; the only producer-side addition is the analytics-only `FulfillmentSourcingDecided` (raised by
`Assignment.Release` with every `FulfillmentReleasedToNode`, its own event id, ONLY on `.analytics`). Do not change the
integration payloads, the OLTP tables or the `cmd/api` routes from here.

- Source: ONLY this context's own `retailnetwork.order-sourcing.events` and `retailnetwork.order-sourcing.analytics`
  (never a sibling's topic). Three projected types, dispatched on the FULL CloudEvents `type`
  (`internal/adapters/inbound/kafka/analytics_decode.go`): `FulfillmentQueued`, `FulfillmentSourcingDecided`,
  `FulfillmentUnsourceable`. The other published types are acknowledged silently; anything else (and non-CloudEvents) is
  skipped with a sampled WARN.
- `cmd/os-projector` is the ONLY writer of the SEPARATE analytical database (`ANALYTICS_DATABASE_URL`, `KAFKA_BROKERS`,
  `ANALYTICS_CONSUMER_GROUP` default `order-sourcing-analytics`, `ADMIN_ADDR` `:8091`). It never opens the OLTP database.
  `cmd/os-reports` (`HTTP_ADDR` `:8092`) reads through a read-only pool (`ANALYTICS_READER_DATABASE_URL`, falling back
  to `ANALYTICS_DATABASE_URL`; the chart renders only the reader DSN).
- Dedupe on the CloudEvents `id` and the fact write are ONE analytical-database transaction (`Projection.Apply`); test
  atomicity with a trigger that RAISEs (`postgres_integration_test.go`). A republished decision is keyed
  `(assignment_id, release_no)`; cost-to-serve uses the LAST decision per assignment (highest `releaseNo`).
- Delivery policy: dead-letter ONLY deterministic poison (bad payload, subject mismatch, missing field, Postgres class
  22/23) to `retailnetwork.order-sourcing.analytics.dlq`; retry transient failures forever with capped backoff.
- `internal/analytics/report` is pure and imports no other internal package (arch test); its payload vocabulary is
  hand-mirrored from `apis/asyncapi.yaml`. SQL only counts, sums and `percentile_cont`; shares, means, rates, the
  reassignment/split rules, range rules and the freshness lag live there, as functions (not package variables), in
  `make mutation` and in the coverage scope. One contract suite (`analyticsstore/contract_test.go`) runs against
  `MemoryStore` and Postgres: change both twins together.
- `analytics/migrations` is embedded (`analytics/migrations.go`), additive, with down files; never edit an applied
  migration, add the next number.
- Reports are unauthenticated, read-only GETs under `/reports/...` (plus `/healthz`, `/readyz`), camelCase, UTC days,
  `from`/`to` inclusive dates, a share/mean/rate is `null` when its denominator is 0. Any route change edits
  `apis/openapi.yaml` (tag `reports`, per-path server `:8092`) and regenerates the REST reference in the same commit;
  `reportshttp/spec_test.go` fails on drift.
- Chart: the `analytics:` block is opt-in (default render byte-identical); keep `tests/render_test.sh` and
  `tests/test_service_selectors.py` green. Kong: `/api/order-sourcing/reports` rewritten to `/reports`.
