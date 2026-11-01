---
name: how-to-add-an-integration-event
description: Publish or consume a cross-service Kafka event in this service: CloudEvents 1.0 type naming, AsyncAPI, the type catalogue ADR, the encoder, the transactional outbox, the consumer handlers and inbox. Use when touching internal/adapters kafka or outbox code, a domain event, or apis/asyncapi*.yaml.
---

# How to add an integration event

order-sourcing publishes ten events and consumes 32 event types of nine sibling topics (ADR 0003, ADR 0004). The
envelope is CloudEvents 1.0 only; read `.claude/rules/integration-events.md` and `.claude/rules/fleet/cloudevents.md`
first. The worked publishing example is `FulfillmentCancelRefused`; the consuming one is `PurchaseOrderAccepted`.

## Publishing a new event

1. **Domain event.** A typed struct in `internal/domain/assignment/events.go` (`FulfillmentCancelRefused`): embeds
   `Base` (a `shared.Header` plus `AssignmentID`), fields exactly the model's `data` list, `EventName()` and, for an
   analytics-only event, a `Stream()` override. `Entity()` is `assignment` and `SubjectID()` the `assignmentId` (both
   from `Base`). The aggregate command (`Assignment.RefuseCancel` in `commands.go`) returns it.
2. **Contract, one commit.** Add the message and its `data` schema to `apis/asyncapi.yaml`, AND one
   `type: com.retailnetwork.commerce.order-sourcing.assignment.<Event> stream: events` line to
   `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`. `TestEventCatalogueMatchesContract` fails when the two
   disagree, so the push fails when they are not in the same commit. Lint:
   `npx --yes @stoplight/spectral-cli lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml`. Evolution is
   additive only; a breaking change is a new `:v2` dataschema.
3. **Encoder.** In `internal/adapters/outbound/kafka/encoder.go` add the payload struct (camelCase json tags, instants
   through `utc`, arrays never null) and a `case` in `payload`. The envelope itself is built by
   `internal/adapters/kafka/cloudevents` only. Add the event to `allEvents()` in `encoder_test.go`, run
   `go test ./internal/adapters/outbound/kafka -run Golden -update` to write its file under `testdata/golden/`, review
   that JSON by eye against the AsyncAPI example, and bump the count in `TestEncoderMatchesThePublishedContract`.
4. **Publish from the use case** through `writer.publish` inside the same `ports.UnitOfWork` as the `Save`
   (`internal/application/usecases/writer.go`): the aggregate row and the outbox row commit or roll back together.
   Never call Kafka from a use case.
5. **Nothing else.** The relay (`internal/adapters/outbound/outbox/relay.go`) already drains every row; the topic is
   `retailnetwork.order-sourcing.events` (`kafka.TopicEvents`), or `.analytics` for an analytics-only event.
6. **Prove it.** `go test -tags=integration ./cmd/api/ -run TestRealWiringEndToEnd -race -count=1` reads the events
   back from a real broker; extend `cmd/api/e2e_integration_test.go` when the event is part of a REST or Kafka flow.

## Consuming a new event from a sibling context

1. Read the producer's contract now: `git -C ~/warehouse-systems/<producer> show origin/feature/service:apis/asyncapi.yaml`
   (cross-context shapes are decided in contracts-delta, never invented here). Report a mismatch instead of adapting
   silently, and record the choice in ADR 0003.
2. Add a `receive` message to `apis/asyncapi.yaml` with only the fields you rely on and an example whose `type` is the
   producer's exact string. `TestEveryConsumedExampleIsRoutedAndApplied` (`internal/adapters/inbound/kafka/consumers_test.go`)
   then fails until a handler exists, and fails when a handler has no example.
3. Add the handler to the producer's method in `internal/adapters/inbound/kafka/handlers.go` (`typed(m, <full type>,
   ...)`): decode tolerantly into a small struct, call ONE use case with `meta(e)` (the CloudEvents id and type), and
   return the error for a transient failure. The use case claims the event id in `processed_events` (the inbox) inside
   its own transaction (`writer.claim`), so a redelivery changes nothing.
4. A new topic also needs its `<PRODUCER>_CONSUMER_GROUP` variable in `Handlers.Topics`, `cmd/api/main.go`,
   `docs/docs/overview/runtime.md` and the e2e environment; a missing group is a boot error naming it.

## Verify before opening the PR

`make check`, `go test ./internal/architecture/ -count=1` (catalogue, CloudEvents-only, relay cap, DLQ shutdown guard),
the spectral lint of the AsyncAPI, and the integration command above.
