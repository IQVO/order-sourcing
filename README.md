# order-sourcing

Retail network, bounded context **Order Sourcing** (Core, commerce tier).

Decides, as late as the promise allows, which node and path will actually fulfil each shipment group. Keeps assignments in a virtual queue, re-optimises them against current capacity and cost until the release point, sends the group to the node, reassigns on rejection or expiry, and locks the assignment once the node commits.

Status: under construction. The design is proposed in
[retail-network-docs](https://iqvo.github.io/retail-network-docs/contexts/order-sourcing); `model/model.yaml` there is the single source of truth.
This repository is a service of the retail network, not of the warehouse-systems fleet; the fleet reaches the network only through the
`vendor-connectivity` Vendor API.

Runtime: Go, hexagonal, real PostgreSQL and Kafka only (`DATABASE_URL`, `KAFKA_BROKERS` are mandatory), CloudEvents 1.0 on every message
(`com.retailnetwork.commerce.order-sourcing.<entity>.<Event>`), transactional outbox.

See `docs/adr/` for decisions and `docs/before-first-release.md` for the packaging checklist.
