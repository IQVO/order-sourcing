---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

Route -> use case, kept in sync with `apis/openapi.yaml`. Routes are registered in
`internal/adapters/inbound/http/server.go` (`Server.Handler`) and wired in `cmd/api/main.go` `buildServices`:

- `GET /assignments?state=&nodeId=&orderId=&limit=&cursor=` -> `ListAssignments` (paged, body `{items,nextCursor}`)
- `GET /assignments/{assignmentId}` -> `GetAssignment`
- `POST /assignments/{assignmentId}/reoptimisations` -> `ReoptimiseAssignment` (optional body `{"assignmentVersion"}`)
- `GET /sourcing-policy` -> `SourcingPolicies.Current` (the current version, or the defaults with `version: 0`);
  `PUT /sourcing-policy` -> `SourcingPolicies.Publish` (whole document `{crossRegionSurchargeCents, maxCostDeviationPercent}`,
  422 outside the ranges, publishes version n+1, takes an `Idempotency-Key` because every call publishes a new version; ADR 0014)
- `GET /healthz` (liveness), `GET /readyz` (readiness): not business operations.
- Not served by `cmd/api`: the read-only `GET /reports/...` routes (tag `reports`, `cmd/os-reports`, `internal/adapters/inbound/reportshttp`, ADR 0013; rules in `.claude/rules/analytics.md`).

Every other transition (queue, release, refusal, lock, ship, deliver, cancel, revise) is driven by Kafka events,
not by REST (ADR 0002).

## Conventions

- **Errors** are RFC 9457 `application/problem+json`: `type` is a kebab-case slug from `problemCatalogue` in
  `errors.go`, plus `title`, `status`, `detail`, `instance`. Shared: `not-found` 404, `illegal-transition` 409,
  `concurrent-update` 409, `validation-failed` 422; one slug per model `Err...` (`assignment-locked`,
  `release-in-flight`, `stale-assignment-version`, `candidate-breaks-promise`, `node-excluded`). An unmapped error
  is an opaque 500 `internal-error`. A new `Err` needs a catalogue row and a row in
  `TestEveryTypedErrorHasItsProblem` (`handlers_test.go`).
- **Idempotency-Key** is required on every POST/PATCH (`idempotency.go`): the key is GLOBAL (looked up alone,
  fingerprint = method + path + body hash); same key + same request replays the stored answer
  (`Idempotency-Replayed: true`), same key + other request is 409 `idempotency-key-reused`,
  missing key 400 `idempotency-key-required`, a key outside 1-128 printable ASCII 400
  `idempotency-key-invalid`; keys live 24 h in `idempotency_keys`. A new keyed route
  goes inside the `r.Group` that `Server.Handler` wraps with `s.Idempotency`.
- **NUL** (U+0000) is rejected before anything else (`nul.go`).
- **Handlers hold no business logic**: decode, call a use case, encode the view.
- **Auth**: none. REST is unauthenticated, as in the rest of the network
  (`internal/architecture/fitness_test.go` `TestNoAuthMiddlewareReintroduced` keeps it so).
- Unit tests (`handlers_test.go`) run against use-case fakes; the real middleware is proven on Postgres in
  `idempotency_integration_test.go` and the whole process in `cmd/api/e2e_integration_test.go`.
