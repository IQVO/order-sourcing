---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/outbox/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

order-sourcing PUBLISHES ten events (eight integration events to `retailnetwork.order-sourcing.events`, two
analytics-only events to `retailnetwork.order-sourcing.analytics`) and CONSUMES 32 event types from nine sibling topics
(ADR 0003, ADR 0004). Never produce or consume a `warehouse.*` topic.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes is a CloudEvents 1.0 event in structured content mode. This
is a hard rule, not a preference:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write, no envelope toggle env var.
  `internal/architecture/fitness_test.go`'s `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build and validate the envelope with `github.com/cloudevents/sdk-go/v2/event` through ONE helper package,
  `internal/adapters/kafka/cloudevents` (`New`, `Decode`, `ContentTypeHeader`). Transport stays
  `segmentio/kafka-go`; no hand-rolled CloudEvent structs. The producing caller is the encoder
  `internal/adapters/outbound/kafka/encoder.go`; the consuming caller is `internal/adapters/inbound/kafka`.
- Every message carries the Kafka header `content-type: application/cloudevents+json; charset=UTF-8`. Message key =
  `assignmentId`, `kafkago.Hash{}` balancer (`internal/adapters/outbound/kafka/writer.go`).
- Attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per event at outbox append and stored in
  `outbox_events.event_id`, so a redelivery carries the same id); `source=/retailnetwork/order-sourcing`; `type`;
  `subject` (the `assignmentId`, never empty); `time` (the domain occurred-at, UTC); `datacontenttype=application/json`;
  `dataschema=urn:retailnetwork:order-sourcing:<events|analytics>:<EventName>:v1`. No custom extension attributes without an ADR.
- `type` = `com.retailnetwork.commerce.order-sourcing.assignment.<EventName>`. A breaking payload change is a new
  `:v2` dataschema, never a mutation of v1; evolution is additive only.
- Payloads follow the wire conventions of contracts-delta: ids are strings, instants RFC 3339 UTC, durations integers
  ending in `Minutes`/`Seconds`, enums UPPER_SNAKE, optional fields omitted, arrays always present.
- Consumers (`internal/adapters/inbound/kafka`) decode with `cloudevents.Decode`, dispatch on the FULL `type`
  string (`Handlers.Topics`), ignore unknown types and unknown fields, dedupe on the CloudEvents `id` in
  `processed_events`, and dead-letter a message that keeps failing to `<topic>.dlq` after a bounded retry.
- Tests: a golden exact-JSON file per published type (`internal/adapters/outbound/kafka/golden_test.go`, files in
  `testdata/golden/`; refresh with `go test ./internal/adapters/outbound/kafka -run Golden -update`), the
  encoder-vs-contract key test (`encoder_test.go`), the catalogue-vs-contract sensor
  (`TestEventCatalogueMatchesContract`), the contract-example routing test
  (`TestEveryConsumedExampleIsRoutedAndApplied`), and the Kafka proof against a real broker
  (`cmd/api/e2e_integration_test.go`, `internal/adapters/outbound/postgres/outbox_drain_integration_test.go`),
  all via testcontainers.

### Published types

Subject and Kafka key = `assignmentId`; `dataschema` is `urn:retailnetwork:order-sourcing:<stream>:<Event>:v1` (`events` or `analytics`).

| `type` | topic |
| --- | --- |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentQueued` | `retailnetwork.order-sourcing.analytics` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode` | `retailnetwork.order-sourcing.events` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReassigned` | `retailnetwork.order-sourcing.events` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentLocked` | `retailnetwork.order-sourcing.events` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped` | `retailnetwork.order-sourcing.events` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentDelivered` | `retailnetwork.order-sourcing.analytics` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentUnsourceable` | `retailnetwork.order-sourcing.events` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled` | `retailnetwork.order-sourcing.events` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelRefused` | `retailnetwork.order-sourcing.events` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentConstraintRevised` | `retailnetwork.order-sourcing.events` |

### Consumed types

The exact `type` strings of the producers' catalogues; the handlers are in
`internal/adapters/inbound/kafka/handlers.go`, the vendored contract in `apis/asyncapi.yaml` (`receive` operations).

| `type` | topic | producer |
| --- | --- | --- |
| `com.retailnetwork.commerce.customer-ordering.customerorder.CustomerOrderCancelled` | `retailnetwork.customer-ordering.events` | customer-ordering |
| `com.retailnetwork.commerce.customer-ordering.customerorder.CustomerOrderPlaced` | `retailnetwork.customer-ordering.events` | customer-ordering |
| `com.retailnetwork.commerce.delivery-promise.promise.PromiseRevised` | `retailnetwork.delivery-promise.events` | delivery-promise |
| `com.retailnetwork.commerce.delivery-promise.promisebook.PromiseBookExhausted` | `retailnetwork.delivery-promise.events` | delivery-promise |
| `com.retailnetwork.logistics.last-mile-delivery.wave.DeliveryWaveCancelled` | `retailnetwork.last-mile-delivery.events` | last-mile-delivery |
| `com.retailnetwork.logistics.last-mile-delivery.wave.DeliveryWaveCapacityChanged` | `retailnetwork.last-mile-delivery.events` | last-mile-delivery |
| `com.retailnetwork.logistics.last-mile-delivery.wave.DeliveryWaveScheduled` | `retailnetwork.last-mile-delivery.events` | last-mile-delivery |
| `com.retailnetwork.logistics.linehaul-transportation.departure.BookingFailed` | `retailnetwork.linehaul-transportation.events` | linehaul-transportation |
| `com.retailnetwork.logistics.linehaul-transportation.departure.BookingMoved` | `retailnetwork.linehaul-transportation.events` | linehaul-transportation |
| `com.retailnetwork.logistics.linehaul-transportation.departure.DepartureCancelled` | `retailnetwork.linehaul-transportation.events` | linehaul-transportation |
| `com.retailnetwork.logistics.linehaul-transportation.departure.DepartureCapacityChanged` | `retailnetwork.linehaul-transportation.events` | linehaul-transportation |
| `com.retailnetwork.logistics.linehaul-transportation.departure.DepartureScheduled` | `retailnetwork.linehaul-transportation.events` | linehaul-transportation |
| `com.retailnetwork.partner.node-performance.scorecard.NodeScorecardUpdated` | `retailnetwork.node-performance.events` | node-performance |
| `com.retailnetwork.logistics.node-topology.lane.LaneClosed` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.lane.LaneOpened` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.node.NodeActivated` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.node.NodeOperationalProfileDeclared` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.node.NodeRetired` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.node.NodeSuspended` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.region.RegionDefined` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.logistics.node-topology.servicearea.ServiceAreaAssigned` | `retailnetwork.node-topology.events` | node-topology |
| `com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemDiscontinued` | `retailnetwork.offer-availability.events` | offer-availability |
| `com.retailnetwork.commerce.offer-availability.listing.ListingAvailabilityChanged` | `retailnetwork.offer-availability.events` | offer-availability |
| `com.retailnetwork.commerce.offer-availability.listing.ListingCreated` | `retailnetwork.offer-availability.events` | offer-availability |
| `com.retailnetwork.commerce.offer-availability.listing.ListingRestored` | `retailnetwork.offer-availability.events` | offer-availability |
| `com.retailnetwork.commerce.offer-availability.listing.ListingSuppressed` | `retailnetwork.offer-availability.events` | offer-availability |
| `com.retailnetwork.logistics.shipping-and-tracking.journey.PackageDelivered` | `retailnetwork.shipping-and-tracking.events` | shipping-and-tracking |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted` | `retailnetwork.vendor-connectivity.events` | vendor-connectivity |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderExpired` | `retailnetwork.vendor-connectivity.events` | vendor-connectivity |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderRejected` | `retailnetwork.vendor-connectivity.events` | vendor-connectivity |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.VendorShipmentConfirmed` | `retailnetwork.vendor-connectivity.events` | vendor-connectivity |

## Transactional outbox

A use case saves the aggregate and appends the events in ONE Postgres transaction (`ports.UnitOfWork`,
`ports.EventOutbox`, `internal/adapters/outbound/postgres/outbox.go`). The relay
(`internal/adapters/outbound/outbox/relay.go`, always on, started by `cmd/api/main.go` `startOutboxRelay`) drains
`outbox_events` in id order, at-least-once. The cap `OUTBOX_MAX_ATTEMPTS` (default 20, 0 = unlimited) excludes a
poison row; re-arm it with `UPDATE outbox_events SET attempts = 0 WHERE id = ...` (ADR 0005). The first write to a
topic that does not exist yet can fail once with an unknown-topic error: the relay retries it with the same id. The
caller of `NewOutboxRelay` must pass `WithMaxAttempts(...)` inline (`TestOutboxRelaysPassAttemptCap`).

## Consumer group ids

One mandatory environment variable per consumed topic, never a string literal
(`TestKafkaConsumerGroupNeverHardcodedInline`): `CUSTOMER_ORDERING_CONSUMER_GROUP`,
`DELIVERY_PROMISE_CONSUMER_GROUP`, `LAST_MILE_DELIVERY_CONSUMER_GROUP`, `LINEHAUL_TRANSPORTATION_CONSUMER_GROUP`,
`NODE_PERFORMANCE_CONSUMER_GROUP`, `NODE_TOPOLOGY_CONSUMER_GROUP`, `OFFER_AVAILABILITY_CONSUMER_GROUP`,
`SHIPPING_AND_TRACKING_CONSUMER_GROUP`, `VENDOR_CONNECTIVITY_CONSUMER_GROUP`. A missing one is a boot error naming it
(`cmd/api/main.go` `loadRuntime`). The group ids are stable, service-prefixed values set by the deployment. The
read models live in Postgres (committed offsets), so a restart resumes instead of replaying: no per-process-unique
group id. See `.claude/rules/fleet/kafka-testing-and-consumers.md` for the generic rules.
