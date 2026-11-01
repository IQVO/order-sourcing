---
id: testing
title: Testing
sidebar_label: Testing
description: "The test pyramid of Order Sourcing as it is: unit, handler, godog features, integration, contract, mutation and architecture fitness tests, the make targets and the CI jobs."
---

# Testing

The pyramid below is what exists on `develop`. Every `make` target mirrors a job in `.github/workflows/ci.yml`, so the feedback
CI gives after a push is available locally before it.

## The pyramid

| Layer | Where | What it proves | Run with |
| --- | --- | --- | --- |
| Unit (domain) | `internal/domain/assignment` (7 test files), `sourcing`, `network`, `shared` | The `FulfillmentAssignment` state machine and its invariants (no node change after the lock, at most one pending release, strictly increasing version, cancel only before the lock), the pure candidate search and cost policy, the read-model merge rules | `make test` |
| Unit (application) | `internal/application/usecases` (queue, commands, cancel/revise, projector, sweep and queries, metrics), `internal/application/outbox` | Each use case against the in-memory doubles of `internal/adapters/outbound/memory`: inbox claim, idempotent redelivery, one retry on a concurrent update, metric outcomes | `make test` |
| Unit (adapters) | `internal/adapters/inbound/{http,kafka,mcp,a2a,requestlog}`, `internal/adapters/outbound/{kafka,outbox,telemetry,clock,ids}`, `internal/adapters/kafka/cloudevents`, `internal/bootretry`, `cmd/*/main_test.go`, `cmd/*/telemetry_test.go` | REST handlers over `httptest` (problem mapping, NUL rejection, readiness cache and drain gate, idempotency), the consume loop (retry, backoff, dead letter, commit-after-handling), the relay (attempt cap), the Kafka sink, the CloudEvents codec, MCP and A2A tool behaviour, boot configuration errors | `make test` |
| BDD acceptance | `features/*.feature`: `fulfilment`, `planner`, `queueing`, `release`, `revision`, `virtual_queue` (6 files, 45 scenarios) with steps in `features/steps_*_test.go` and the runner `features/features_test.go` (`TestFeatures`, godog) | The business rules end to end through the real use cases and REST handlers on the in-memory doubles | `make bdd` |
| Contract | `internal/adapters/outbound/kafka/golden_test.go` with 10 golden CloudEvents in `testdata/golden/`; `internal/architecture/catalogue_fitness_test.go` (`TestEventCatalogueMatchesContract`); `internal/adapters/inbound/mcp/testdata/tool_registry.golden.json`; `internal/adapters/outbound/repocontract` (one contract run against memory and Postgres) | The published event shapes and the AsyncAPI catalogue, the MCP tool registry, and that the in-memory repositories behave like the Postgres ones | `make test`, `make integration` |
| Integration | 7 files tagged `integration`: `cmd/api/e2e_integration_test.go`, `cmd/mcp/main_integration_test.go`, `cmd/a2a/main_integration_test.go`, `internal/adapters/outbound/postgres/{postgres,outbox_drain,outbox_trace}_integration_test.go`, `internal/adapters/inbound/http/idempotency_integration_test.go` | Repositories and migrations on real Postgres, the outbox drain, trace propagation into the outbox row, the Idempotency-Key middleware, and the real-wiring end-to-end test (Postgres and Kafka; the pipeline from consumed event to published event) | `make integration` (needs Docker) |
| Mutation | `.gremlins.yaml` (efficacy 95, mutant coverage 90, one worker, timeout coefficient 30) over each `internal/domain/<pkg>` | That the domain tests assert rather than merely execute. Measured with gremlins v0.6.0: assignment 100 / 100, sourcing 97.7 / 97.8, network 100 / 100, shared 100 / 100 (efficacy / mutant coverage, as recorded in `.gremlins.yaml`) | `make mutation`, `make mutation-full` |
| Architecture fitness | `internal/architecture` (13 files, 33 top-level tests) | Executable rules (see below) | `make arch-test` |

No test needs a database or a broker except the integration layer, which boots its own Postgres and Kafka with testcontainers
(`TestPostgresIntegrationTestsUseTestcontainers` and `TestKafkaIntegrationTestsUseTestcontainers` forbid `t.Skip` gates and
environment-supplied DSNs).

## Architecture fitness tests

| Test | Rule |
| --- | --- |
| `TestHexagonalArchitecture` | The domain imports no outer layer; adapters do not reach sideways into each other |
| `TestMCPAdapterDependencyRule`, `TestA2AAdapterDependencyRule` | `inbound/mcp` and `inbound/a2a` depend inward only and nothing depends on them |
| `TestA2AAgentIsReadOnly`, `TestA2ABinaryIsKafkaFreeAndReadOnly`, `TestA2AHasNoOutboundClient` | No write use case reachable from MCP or A2A; `cmd/a2a` never imports Kafka; no outbound A2A client |
| `TestNoAuthMiddlewareReintroduced` | No auth middleware in any router (fleet decision) |
| `TestRuntimeBinaryHasNoInMemoryAdapters` (+ `...SensorBites`) | `cmd/**` never imports `internal/adapters/outbound/memory` or `repocontract` |
| `TestNoGoFilesAtRepoRoot` (+ sensors) | No Go file at the repository root |
| `TestEventCatalogueMatchesContract` | The code's event catalogue equals `apis/asyncapi.yaml` |
| `TestCloudEventsOnly`, `TestNoEventEnvelopeToggleOrFlatEnvelope` | Only CloudEvents 1.0 on the wire, no flat envelope or toggle |
| `TestKafkaConsumerGroupNeverHardcodedInline`, `TestReplayConsumersSetCommitInterval` | Consumer groups come from the environment; no async commit interval |
| `TestKafkaWritersSetRequiredAcks`, `TestKafkaWriterTopicNotSetTwice` | Writers set RequireAll and never a topic on both writer and message |
| `TestOutboxRelaysPassAttemptCap` | Every relay built under `cmd/*` passes `WithMaxAttempts` |
| `TestDeadLetterCallsGuardCancellation` | Dead-letter publishing guards context cancellation on shutdown |
| `TestMCPOptionalToolInputsCarryOmitempty` | Optional MCP inputs are `omitempty`, so the schema marks them optional |

Each sensor has a companion test that feeds it a bad fixture and expects a failure, so a sensor cannot pass vacuously.

## Make targets

| Target | What it runs |
| --- | --- |
| `make help` | Print the targets (default) |
| `make build`, `make vet`, `make fmt`, `make fmt-check` | `go build ./...`, `go vet ./...`, `gofmt -w .`, fail if `gofmt -l .` is non-empty |
| `make lint` | `golangci-lint run --allow-parallel-runners ./...` (pinned `v2.14.0`) |
| `make test` | `go test ./... -race` (unit, handler and BDD; no database) |
| `make coverage` | `go test ./... -race -coverprofile=coverage.out -coverpkg=./internal/domain/...,./internal/application/...` and a 90 percent gate over that scope |
| `make integration` | `go test -tags=integration ./... -race -count=1` (testcontainers; needs Docker) |
| `make bdd` | `go test ./... -run TestFeatures -v` |
| `make arch-test` | `go test ./internal/architecture/... -v` |
| `make mutation` | gremlins on every `internal/domain/<pkg>` |
| `make mutation-full` | gremlins on `./internal/domain` with `--workers 1 --timeout-coefficient 30` |
| `make vuln` | `govulncheck ./...` |
| `make check` | `fmt-check vet build lint test` (the pre-push gate) |
| `make check-all` | `check` + `coverage` + `arch-test` + `bdd` (run before pushing) |
| `make check-fast` | `fmt-check vet arch-test` plus the tests of the packages changed against HEAD (the agent Stop hook) |
| `make guide-lint`, `make harness-test` | Lint the agent guides; unit-test the agent hooks |

lefthook (`lefthook install` once per clone) runs `fmt-check`, `vet` and `lint` at pre-commit and `make check` at pre-push.
`integration` and `mutation` are deliberately not in either hook.

## CI jobs

From `.github/workflows/ci.yml` (triggers: push and pull request on `main` and `develop`, a weekly Monday 06:00 UTC schedule, manual
dispatch):

| Job | Runs | Blocks |
| --- | --- | --- |
| `lint` | golangci-lint `v2.14.0` | yes |
| `guide-lint` | `scripts/harness/guide_lint.py`, `test_hook.py` | advisory (`continue-on-error`) |
| `complexity` | golangci-lint with only `gocyclo,gocognit,cyclop,funlen,nestif`, plus an informational gocyclo report | yes |
| `test` | `go build`, `go vet`, then `make coverage` (the 90 percent gate) | yes |
| `bdd` | `go test ./... -run TestFeatures -v` | yes |
| `integration` | `go test -tags=integration ./... -race -count=1` | yes |
| `mutation-fast` | gremlins on every `internal/domain/*/` | yes |
| `mutation` | gremlins on `./internal/domain`, exhaustive; opens a `harness:red` issue when a scheduled run fails | schedule and manual only |
| `drift` | dead code (`deadcode -test`), `go mod tidy -diff`, knip on `web/`, coverage-quality; advisory | schedule and manual only |
| `api-lint` | Spectral on `apis/openapi.yaml` (`.spectral.yaml`) and `apis/asyncapi.yaml` (`.spectral.asyncapi.yaml`), `--fail-severity=warn` | yes |
| `vuln` | `govulncheck ./...` | yes |
| `helm-lint` | `ct lint` on `charts/order-sourcing` and the selector test | pull requests only |
| `chart-tests` | every `charts/order-sourcing/tests/*.sh` and `test_*.py` (a `SKIP` line fails the job) | yes |
| `arch-test` | `go test ./internal/architecture/... -v` | yes |
| `web` | the `web/` remote: `npm run lint`, `tsc -b`, `npm test`, `npm run build` (needs a `warehouse-ui-kit` checkout) | yes |
| `docker-publish` | builds, pushes, signs (cosign keyless) and attests an SBOM for `ghcr.io/iqvo/order-sourcing` | push to `main` only; needs `lint`, `complexity`, `test`, `bdd`, `integration`, `mutation-fast`, `vuln`, `api-lint`, `arch-test` |
| `release` | tags the next patch version, retags the image, packages and pushes the Helm chart, creates the GitHub release | push to `main` after `docker-publish` |

The documentation site has its own workflow, `.github/workflows/docs.yml`: it builds on pull requests (the `Docs` check) and deploys
on push to `main`.

## Adding a test

- A new business rule: add a scenario to the closest `features/*.feature` and the step in `features/steps_*_test.go`; add a unit test
  next to the domain code. Both are required to keep `make mutation` above its thresholds.
- A new consumed or published event: update `apis/asyncapi.yaml`, the handler map in `internal/adapters/inbound/kafka/handlers.go` or
  the encoder in `internal/adapters/outbound/kafka/encoder.go`, add a golden file, and let `TestEventCatalogueMatchesContract` confirm
  the catalogue.
- A new MCP tool: update `internal/adapters/inbound/mcp/testdata/tool_registry.golden.json` (the governance test fails on any
  write-verb name and on drift).
