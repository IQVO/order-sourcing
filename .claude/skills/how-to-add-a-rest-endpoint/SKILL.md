---
name: how-to-add-a-rest-endpoint
description: Add or change a REST endpoint in this service in the hexagonal order (domain invariant, use case, handler, apis/openapi.yaml, godog scenario). Use when touching internal/adapters/inbound/http, apis/openapi.yaml, or exposing a use case over HTTP.
---

# How to add a REST endpoint

Follow this order: domain first, adapter last. A handler written before the invariant it relies on validates
nothing and gets bypassed.

The worked example is `POST /assignments/{assignmentId}:reoptimise`:
`internal/domain/assignment/commands.go` `Reoptimise` -> `internal/application/usecases/commands.go`
`ReoptimiseAssignment` -> `internal/adapters/inbound/http/server.go` `handleReoptimise`. The read side example is
`GET /assignments/{assignmentId}` (`usecases/queries.go` `GetAssignment` -> `handleGet`).

Most transitions of this service are driven by Kafka events, not REST (ADR 0002). Expose a new one over REST only
when the OpenAPI contract says so.

## 1. Domain first

Does the rule already exist in `internal/domain/assignment/`? A handler holds no business logic. A new rule goes into
the aggregate with a table-driven test in the same package (`lifecycle_test.go`, `release_test.go`,
`reoptimise_test.go`, ...) and a named `Err...` variable in `errors.go`. Pure Go: no I/O, `now` and ids passed in.
Then re-run the mutation gate for that package (`gremlins unleash ./internal/domain/assignment`).

## 2. Application: the use case

Add it to `internal/application/usecases/` (`commands.go`, `queue.go`, `cancel_revise.go` for commands;
`queries.go` for reads). A command embeds `writer` (`writer.go`): `mutate` and `transact` run load, domain call,
`Save` and `publish` (outbox append) in ONE `ports.UnitOfWork` and retry once on `ports.ErrConcurrentUpdate`. A
query holds only its read port. Return a view from `internal/application/views`. If a new repository method is
needed, add it to `internal/application/ports/ports.go` (interfaces only), implement it in
`internal/adapters/outbound/postgres/assignments.go` AND `internal/adapters/outbound/memory/assignments.go`, and
extend the shared suite in `internal/adapters/outbound/repocontract/` so both run it. Unit-test the use case in
`internal/application/usecases/*_test.go`.

## 3. Adapter: the handler

In `internal/adapters/inbound/http/`:

- add the request body to `dto.go` (strict decoding);
- add the use-case interface and field to `Server` (`server.go`) and the route in `Server.Handler`; a state-changing
  route goes inside the `r.Group` that applies the Idempotency-Key middleware, a read uses `r.Get`;
- map a new domain `Err` in `errors.go` `problemCatalogue` (kebab-case slug, the status the OpenAPI documents) and
  add it to `TestEveryTypedErrorHasItsProblem` in `handlers_test.go`;
- wire the use case in `cmd/api/main.go` `buildServices`.

## 4. Contract: apis/openapi.yaml

Describe the operation, its `Idempotency-Key` parameter (writes), the success body and every problem it can
return, then lint: `npx --yes @stoplight/spectral-cli lint apis/openapi.yaml --ruleset .spectral.yaml` (the
`api-lint` CI job runs the same). Keep `.claude/rules/rest-api.md` and the README table in step.

## 5. Behaviour

Add the happy path and each refusal to `features/*.feature` (`features/steps_*_test.go` drive the use cases and
name refusals by their model `Err` name) and a case to `internal/adapters/inbound/http/handlers_test.go`. The real
middleware and Postgres are proven by `idempotency_integration_test.go` and `cmd/api/e2e_integration_test.go`.

## 6. Verify before opening the PR

`make check`, `make coverage`, `go test -tags=integration ./internal/adapters/inbound/http/ ./cmd/... -race -count=1`,
and the spectral lint above.
