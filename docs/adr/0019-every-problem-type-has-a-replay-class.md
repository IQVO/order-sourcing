# ADR 0019: every problem type has a replay class; a retryable refusal is not stored under its Idempotency-Key

## Status

Accepted (2026-11-20). Supersedes the sentence "the stored outcome (4xx included) is replayed" of ADR 0010 point 2 and
the matching sentence of ADR 0014 point 5 and of the `Idempotency-Key` text of `apis/openapi.yaml` (the Decision text of
those records is not rewritten: each carries a dated pointer here). Adopts the fleet rule decided by the owner on
2026-11-18 (`.claude/rules/fleet/idempotency-and-outbox.md`, `docs/idempotency-replay-classes.md` of
warehouse-harness-template). No route, status code, problem slug, title, header, event or table changes: a refusal that
was replayed for 24 hours is, for most problem types, now answered again by running the request.

## Context

`RequireIdempotencyKey` (`internal/adapters/inbound/http/idempotency.go`) wraps the two keyed routes, `POST
/assignments/{id}/reoptimisations` and `PUT /sourcing-policy` (`server.go`). The store is the request transaction:

- `serve` begins a transaction, takes `pg_advisory_xact_lock(hashtextextended(key, 0))` (so a second request with the
  same key waits for the first to end), looks up `idempotency_keys` for the key inside the transaction
  (`created_at > now() - 24h`, so a stored answer never outlives its window), and opens `SAVEPOINT handler_run`.
- The handler runs with the transaction bound into its context (`TxBinder`, `pgtx`): the aggregate, the outbox rows
  and the key row commit or roll back together. The key row is written LAST, by `store`
  (`INSERT ... ON CONFLICT (key) DO UPDATE ... created_at = now()`), then `Commit`.
- Until now every answer below 500 was stored: a 2xx with its writes, a 4xx with the handler's writes KEPT
  (`releaseHandlerSavepoint` only healed an aborted transaction), a 5xx or a panic rolled everything back unstored.

That is wrong for a refusal the same request could pass a moment later: `409 concurrent-update` (another writer moved
the aggregate) was replayed to a client that did the right thing and retried, and `404 not-found` for an assignment
that the `CustomerOrderPlaced` event had not created yet stayed the answer for 24 hours. Both routes are driven by people
(the console remote) and by the planner; no service calls them with a deterministic key.

The fleet decision: a retried request is answered from the store only when its first answer is final for that request.
Each problem type carries exactly one class, declared on its catalogue entry; the middleware reads the class and holds
no list of slugs and no status range beyond 2xx and 5xx.

## Decision

1. **The class is a field of the catalogue entry.** `problemCatalogue` (`errors.go`) was a slice of `{err, problem}`;
   it is now a slice of `catalogueEntry{err, p, replay}` (a named struct and a third field: the rows keep their order
   and their problems). `replay` is `replayFinal`, `replayOutcome` or `replayRetryable` (`replay_class.go`). The zero
   value is `replayUnset`; there is no declared default. An entry that declares none, a 5xx entry that is not
   RETRYABLE, or one slug with two classes fails `TestEveryCatalogueEntryDeclaresAReplayClass`; this ADR's table is
   held equal to the catalogue by `TestTheADRDeclaresTheClassOfEveryProblem` and the RETRYABLE list of the OpenAPI text
   by `TestTheSpecListsTheRetryableProblemTypes`.
2. **The error writer marks the capture.** `writeError` tells the middleware's capturing writer the class of the entry
   it answers with (`replayMarker`, `capture.MarkReplay`). A handler that answers without a catalogue problem leaves
   the class unset.
3. **The middleware acts on the verdict, nothing else** (`capture.verdict`, `runFreshRequest`):

   | answer | verdict |
   | --- | --- |
   | 2xx | stored; the handler's writes commit with the key row (`RELEASE SAVEPOINT`, unchanged) |
   | 5xx and a panic | the whole transaction rolls back, key row included, nothing stored (unchanged) |
   | FINAL | `ROLLBACK TO SAVEPOINT` (the handler's writes are discarded, an aborted transaction heals), the answer is stored and replayed |
   | OUTCOME | `RELEASE SAVEPOINT`: the handler's writes commit with the stored answer (no entry uses it today) |
   | RETRYABLE | released exactly as the 5xx branch: `tx.Rollback`, key row included, nothing stored (new) |
   | no class (anything else) | released like RETRYABLE: nothing may be stored without a class |

   A released answer is flushed to the client unchanged and never carries `Idempotency-Replayed`. A 2xx, a 5xx and a
   FINAL answer are byte-identical to before. A 2xx or OUTCOME answer whose `RELEASE SAVEPOINT` fails (an aborted
   transaction: its writes are gone) rolls back and answers 500, as a 2xx did before.
4. **Releasing a RETRYABLE key means the request is forgotten.** The same body under the same key runs again; a
   different body under the same key is a NEW request, not `idempotency-key-reused`: nothing is left to compare
   against.
5. **A concurrent duplicate while the first ends RETRYABLE** waits on the advisory lock until the first transaction
   ends. The first rolls back, the key row never existed, so the duplicate finds no row and is a FRESH request: it
   runs the handler itself, never replays a released refusal and never runs alongside the first.
   `TestAConcurrentDuplicateOfARetryableRefusalRunsTheWorkItself` (real router, Postgres) holds the first request in
   its load, waits until `pg_locks` shows the duplicate waiting, lets the first lose, and checks the duplicate answered
   a fresh 200 with one stored key row.
6. **No OUTCOME problem type exists in this service**, and none could be an OUTCOME today. By reading the use cases
   behind the two keyed routes: `ReoptimiseAssignment.Execute` (`usecases/commands.go` 51-63) runs through `mutate`
   (19-37): load, `apply` (every domain refusal is raised here: `Reoptimise` `commands.go` 77-99, `guardMovable`
   31-42, `checkCandidate` `assignment.go` 173-198), and only then `persist` (`writer.go` 125-135); a refusal returns
   BEFORE the first write. The one error raised after a write started is `ports.ErrConcurrentUpdate` from `Save`, which
   by definition wrote nothing (`UPDATE ... WHERE version = $n-1` affected 0 rows, `postgres/assignments.go` 184-186).
   `SourcingPolicies.Publish` (`usecases/policy.go` 67-92): `policy.Publish` refuses (`policy.go` 63-78) before
   `Policies.Save`; a lost race is `ON CONFLICT DO NOTHING` with 0 rows affected, `ErrConcurrentUpdate`, nothing
   written (`postgres/policies.go` 66-75). The OUTCOME verdict is nevertheless implemented and proven
   (`TestTheMiddlewareActsOnTheClassOfTheRefusal` over a spy transaction, `TestAnAnswerWhoseWritesAreGoneIsNeverStored`),
   so the first refusal that does commit state gets its writes kept instead of silently rolled back; its entry must
   cite the lines of its use case. The existing store KEPT a 4xx's handler writes; a FINAL refusal now discards them
   (J4): nothing is lost, because no refusal writes (see the integration test
   `TestAStateRefusalLeavesNoTraceAndIsNotStored`: no version moved, no outbox row).
7. **MCP and A2A write nothing** (ADR 0006 and ADR 0009: read-only, `find_assignments`, `assignment_detail`): no store
   of their own, nothing to classify. No outbound caller of this REST API exists (ADR 0010: only the repository's
   `web/` remote), so there is no deterministic-key caller to check.
8. **Retention.** The 24-hour window is enforced at read (`replay` filters `created_at > now() - 24h`, and `store`
   refreshes `created_at`), so no stored answer outlives its window. Nothing deletes expired rows (runbook, runtime
   page); unchanged and out of scope here (see Consequences).

### The classes

The question per entry: did the command commit state before refusing (OUTCOME)? Is the answer a function of the request
alone, or of a state that never moves back (FINAL)? Otherwise RETRYABLE. When unsure, RETRYABLE: the refusal committed
nothing, so running it again costs one transaction and cannot pin a false answer for 24 hours. The status and the slug
do not decide the class.

| Problem type | Class | Status | Reason (code of `origin/develop` at b43470b) |
| --- | --- | --- | --- |
| `malformed-request` | FINAL | 400 | the body is not valid JSON for the operation (`decodeOptionalJSON` `response.go` 38-50, `decodeRequiredJSON` `policy.go` 82-): unknown field, trailing data, wrong type, empty policy body. A function of the request alone |
| `assignment-locked` | FINAL | 409 | on a keyed route only `guardMovable` (`commands.go` 35-38) raises it, for LOCKED, SHIPPED and DELIVERED; the only transitions into QUEUED (`commands.go` 293, `consolidation.go` 269, `commands_revision.go` 59) start from RELEASED or UNSOURCEABLE, never from those three, and no code deletes an assignment (J5) |
| `release-in-flight` | RETRYABLE | 409 | RELEASED (`guardMovable` `commands.go` 35-36): the node's refusal or expiry re-queues the assignment (`RefuseRelease`, `commands.go` 293), an acceptance locks it; a retry after that answers differently |
| `node-excluded` | RETRYABLE | 409 | `checkCandidate` (`assignment.go` 192-194): the exclusion list is reset when the promise revision changes (`commands_revision.go` 40), and the candidate comes from event-fed read models |
| `stale-assignment-version` | RETRYABLE | 409 | `Reoptimise` (`commands.go` 81-83): the caller's `assignmentVersion` can be behind (never passes) OR ahead (passes once the aggregate reaches it) of the stored version, and the stored version moves with every event (J2) |
| `candidate-breaks-promise` | RETRYABLE | 422 | `checkCandidate` (`assignment.go` 195-198): the ETA comes from event-fed read models (departures, waves, lanes) and `deliverByLatest` is revised by `ApplyRevisedPromise`; the status family is 422 but the state decides |
| `not-found` | RETRYABLE | 404 | the assignment of the path (`Assignments.Get` `postgres/assignments.go` 229). This service never mints assignment ids over REST: `QueueAssignment` (`usecases/queue.go` 24-35) creates them from the `CustomerOrderPlaced` event, so a request can name one that has not arrived yet (J3) |
| `concurrent-update` | RETRYABLE | 409 | the optimistic lock lost the race (`ErrConcurrentUpdate`, `postgres/assignments.go` 184-186, `postgres/policies.go` 73-75) after `transact` retried once on fresh state (`writer.go` 138-): the next try reads the winner and passes |
| `illegal-transition` | RETRYABLE | 409 | `guardMovable`'s default branch (`commands.go` 39-40: CANCELLED and UNSOURCEABLE): UNSOURCEABLE is re-queued by a promise revision or a withdrawn consolidation (`commands_revision.go` 59-62), so the state is not terminal; one slug serves many commands |
| `validation-failed` | RETRYABLE | 422 | one slug, two kinds of cause: the request's own fields (`assignmentVersion < 0`, `server.go` 119-122; the policy ranges, `policy.go` 63-69) and a value built from read models during the search (`checkCandidate` `assignment.go` 174-191: a candidate's ids, ETA, legs, cost). A slug that mixes request-only and state-dependent causes is RETRYABLE (J1) |

Problems the capture never holds (not catalogue entries, never stored): `idempotency-key-required` and
`idempotency-key-invalid` (400) and `idempotency-key-reused` (409) are written by the middleware itself before the
handler (`idempotency.go`, `writeProblem`); `internal-error` (500) is a 5xx; `resource-not-found` (404) and
`method-not-allowed` (405) are answered by the router outside the keyed group (`server.go`), and `invalid-limit`,
`invalid-cursor`, `unknown-query-parameter` (400) by `GET /assignments` only; `invalid-report-range` (400) belongs to
`cmd/os-reports`. `policy.ErrVersionNotNext` is not a catalogue entry (a publish computes its version from the row it
just read, so it cannot occur on a keyed route today; it would answer 500). 10 catalogue entries, 10 slugs: 2 FINAL,
8 RETRYABLE, 0 OUTCOME.

### Judgement calls

- **J1 `validation-failed` is RETRYABLE although the PUT /sourcing-policy ranges are a function of the request.**
  This is the most consequential call: a client that sends an out-of-range policy twice under one key now executes
  twice instead of replaying the 422. The cost is one rolled-back transaction; the alternative (FINAL) would pin a
  422 that the search derived from read models. Splitting the slug into a request-only type and a state-derived type
  (two problem types, same wire status) is a recorded follow-up, not part of this change.
- **J2 `stale-assignment-version` is RETRYABLE although a caller behind the version can never pass.** The caller ahead
  of the stored version can; "version only moves forward" does not make the answer a function of the request.
- **J3 `not-found` is RETRYABLE, not FINAL.** The assignment of the path is created by an event this service
  consumes, so a 404 can become a 200 without the request changing (the race between the planner and the event). It
  is also the slug of every other port miss.
- **J4 A FINAL refusal now discards the handler's writes** (`ROLLBACK TO SAVEPOINT`), where the store kept a 4xx's
  writes. No refusal writes anything (Decision 6), so no observable behaviour changes; the change makes the
  FINAL/OUTCOME split real, so a refusal that commits state has to be declared OUTCOME to keep it.
- **J5 `assignment-locked` is FINAL.** The only one of its kind: a state that never moves back for an id that is never
  deleted, so no retry of the same request can pass (the same argument as delivery-promise's `promise-closed`). The
  cost of being wrong is a stored 409 for 24 hours on an assignment that, by the model, cannot be unlocked.
- **J6 A refusal with no class is released** (the rule's runtime fallback): the middleware holds no list, so a handler
  that writes a problem without the catalogue (`writeProblem` inside a keyed handler) is treated like RETRYABLE.

## Consequences

- A retry under the same key after `release-in-flight`, `node-excluded`, `stale-assignment-version`,
  `candidate-breaks-promise`, `not-found`, `concurrent-update`, `illegal-transition` or `validation-failed` runs the
  request again instead of replaying a refusal; a success is then stored and replayed. A different body under that key
  is a new request. A client that reused a key on purpose after one of these refusals gets its retry executed.
- Only `malformed-request` and `assignment-locked` (and every 2xx) are replayed. Callers driven by people still surface
  a refusal; this change adds no consumer-side or client-side retry (the `web/` remote is untouched).
- A released refusal writes no key row, so the key table no longer grows with refusals.
- A new problem type is classified by the PR that adds it: the unit test is red otherwise, and the table above, the
  OpenAPI RETRYABLE sentence and the catalogue move together.
- Out of scope, found and not changed: nothing deletes expired `idempotency_keys` rows (they are overwritten on the
  next use of the same key and ignored on lookup after 24 hours); ADR 0010's migration 0002 comment and the runbook
  still say so.
