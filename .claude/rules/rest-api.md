---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

Route -> use case, kept in sync with `apis/openapi.yaml`. Routes are registered in
`internal/adapters/inbound/http/server.go` (`Server.Handler`) and wired in `cmd/api/main.go` `buildServices`:

- `GET /assignments?state=&nodeId=&orderId=&limit=` -> `ListAssignments`
- `GET /assignments/{assignmentId}` -> `GetAssignment`
- `POST /assignments/{assignmentId}:reoptimise` -> `ReoptimiseAssignment` (optional body `{"assignmentVersion"}`)
- `GET /healthz` (liveness), `GET /readyz` (readiness): not business operations.

Every other transition (queue, release, refusal, lock, ship, deliver, cancel, revise) is driven by Kafka events,
not by REST (ADR 0002).

## Conventions

- **Errors** are RFC 9457 `application/problem+json`: `type` is a kebab-case slug from `problemCatalogue` in
  `errors.go`, plus `title`, `status`, `detail`, `instance`. Shared: `not-found` 404, `illegal-transition` 409,
  `concurrent-update` 409, `validation-failed` 422; one slug per model `Err...` (`assignment-locked`,
  `release-in-flight`, `stale-assignment-version`, `candidate-breaks-promise`, `node-excluded`). An unmapped error
  is an opaque 500 `internal-error`. A new `Err` needs a catalogue row and a row in
  `TestEveryTypedErrorHasItsProblem` (`handlers_test.go`).
- **Idempotency-Key** is required on the state-changing route (`idempotency.go`): same key + same request replays
  the first response (`Idempotent-Replayed: true`), same key + other request is 422 `idempotency-key-reused`,
  missing key 400 `idempotency-key-required`, keys live 24 h in `idempotency_keys`. A new state-changing route
  goes inside the `r.Group` that `Server.Handler` wraps with `s.Idempotency`.
- **NUL** (U+0000) is rejected before anything else (`nul.go`).
- **Handlers hold no business logic**: decode, call a use case, encode the view.
- **Auth**: none. REST is unauthenticated, as in the rest of the network
  (`internal/architecture/fitness_test.go` `TestNoAuthMiddlewareReintroduced` keeps it so).
- Unit tests (`handlers_test.go`) run against use-case fakes; the real middleware is proven on Postgres in
  `idempotency_integration_test.go` and the whole process in `cmd/api/e2e_integration_test.go`.
