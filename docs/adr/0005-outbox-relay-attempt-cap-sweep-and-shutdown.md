# ADR 0005: outbox relay, attempt cap, queue sweep and shutdown order

## Status

Accepted (2026-10-23).

## Context

order-sourcing writes ten event types as a side effect of every transition (ADR 0004) and must never lose one or publish
one for a change that rolled back. It also needs a timer: a `QUEUED` assignment is released at `releaseAt` and the others
are re-optimised against current capacity (ADR 0002 A18). Both run in the one binary `cmd/api`, next to nine consumers.
The alternatives were publishing from the use case after the commit (loses an event on a crash), a separate relay
deployment (a second image and chart for a single table) and a database trigger or change-data-capture (an extra
platform component for a study service).

## Decision

1. **Transactional outbox.** A use case saves the assignment and appends the encoded events in ONE Postgres
   transaction (`ports.UnitOfWork`, table `outbox_events`). The CloudEvents `id` is minted at append and stored with the
   row, so a retry or a redelivery carries the same id.
2. **In-process relay, always on.** `internal/adapters/outbound/outbox/relay.go` drains unpublished rows in id order every
   `OUTBOX_RELAY_INTERVAL` (default 1 s) and writes them to `retailnetwork.order-sourcing.events` or `.analytics`
   with acks from all replicas. Delivery is at-least-once; consumers dedupe on the event id.
3. **Attempt cap.** A row that fails `OUTBOX_MAX_ATTEMPTS` times (default 20, `0` = unlimited) is excluded from
   later passes, logged once at ERROR and kept with `attempts` and `last_error`, so one poison row cannot stall the
   table. An operator re-arms it with `UPDATE outbox_events SET attempts = 0 WHERE id = ...`. While a row is skipped,
   later events of the same assignment can overtake it. The composition root passes `WithMaxAttempts` inline
   (`TestOutboxRelaysPassAttemptCap`).
4. **Queue sweep.** `usecases.SweepQueue` runs every `SWEEP_INTERVAL` (default 15 s) in the same process: it releases the
   assignments whose `releaseAt` has passed (`ReleaseToNode`) and re-optimises the others (`ReoptimiseAssignment`), page
   by page. A sweep touches an assignment through the same use cases as REST and Kafka, so the version check and the
   outbox rules are the same; a transition another actor won first is counted as skipped, not as a failure.
5. **Shutdown order.** `/readyz` flips to 503, the drain delay passes, HTTP drains, then the consumers and the sweep
   stop, and the relay stops last so that every event committed during the drain is still published. A worker that
   returns on its own makes the process exit non-zero.

## Consequences

A write is accepted while Kafka is down and published when it is back; `/readyz` reports the broker as unavailable so
the pod leaves the load balancer, but `/healthz` stays 200 and the pod is not killed. The sweep and the relay run once per
replica: concurrent sweeps are safe because every transition is an optimistic-version save, and concurrent relays are
safe because rows are claimed with `FOR UPDATE SKIP LOCKED` in `internal/adapters/outbound/postgres/outbox.go`.
The cost is one table that needs monitoring (`published_at IS NULL`, `attempts > 0`) and a runbook, in `docs/runtime.md`.
