# ADR 0010: Adopt the fleet REST API guideline

## Status

Accepted (2026-11-01). Applies the fleet REST API guideline (warehouse-docs, REST guideline, rules R1-R8) to this
context. Supersedes the `:reoptimise` custom method and the `(route, key)` idempotency store of
[ADR 0002](./0002-aggregates-invariants-and-assumptions.md) and the REST description in
[ADR 0006](./0006-mcp-server-adoption.md) and [ADR 0009](./0009-a2a-inbound-adapter.md).

## Context

The fleet had three command-URL styles, two spellings of the replay header, key reuse answered 422, and unpaginated
lists. The guideline removes each divergence with a hard cutover: no aliases, no dual routes, no toggles.

## Decision

1. **Nouns, not verbs (R1, R2).** `POST /assignments/{assignmentId}:reoptimise` becomes
   `POST /assignments/{assignmentId}/reoptimisations` (a transition resource). The operationId
   `reoptimiseAssignment`, the status codes and the body do not change. `TestRetiredRoutesAreGone` pins the old paths.
2. **Idempotency (R3-R6, R8).** Every POST (and PATCH, none exist) sits behind `RequireIdempotencyKey`. The key is
   global: the stored row is found by key alone (table `idempotency_keys`, primary key `key`, migration 0002) and
   compared by fingerprint = method + path + SHA-256 of the body. Same fingerprint replays the stored answer with
   `Idempotency-Replayed: true`; another fingerprint is `409 idempotency-key-reused`. A missing key is
   `400 idempotency-key-required`; a key that is not 1 to 128 printable ASCII characters is
   `400 idempotency-key-invalid` (it was `422 validation-failed`). The key row commits in the same transaction as the
   aggregate change and its outbox rows (the store already joined the unit of work; only the key changed). A 5xx or a
   panic rolls back and is never stored. `Idempotent-Replayed` is retired.
3. **Pagination (R7).** `GET /assignments` answers `{items, nextCursor}` with `limit` (1-500, default 100) and an opaque
   `cursor` (the shared `page` package, copied under `internal/adapters/inbound/http/page`). Keyset order is
   `assignmentId` (unique, so ties cannot occur) in both the Postgres and the in-memory repository; the adapter asks the
   use case for `limit + 1` rows (`repository.FetchLimit`). Bad `limit` is `400 invalid-limit`, bad `cursor`
   `400 invalid-cursor`, any undeclared query parameter `400 unknown-query-parameter`.
4. **Exemptions:** none. Both collection reads paginate (REST) or keep their own bound (below).
5. **MCP and A2A** keep their own tool and skill schemas (`assignments` list, `limit` 1..500); `limit` is the documented
   bound and they return the first `limit` assignments by id. They validate `limit` themselves because the use case now
   accepts `limit + 1` for the REST page probe.

## Consequences

- Breaking: new URL, new list envelope, key now global. Deploy with the REST cutover set.
- Migration 0002 deletes stored idempotency rows (at most 24 hours old, all for the retired route).
- Consumers: the repo's `web/` remote (route, key, `items`); no sibling service calls this REST API.
