-- order-sourcing analytics read model: the shipment key of a decision
-- (ADR 0018). Additive and nullable.
--
-- carrier_assignment_id is the carrier of a merged release, set on a FOLLOWER's
-- decision only (FulfillmentSourcingDecided.carrierAssignmentId, ADR 0015);
-- NULL means the decision ships on its own assignment. Rows projected before
-- this migration are NULL (their own shipment) until the projector's
-- earliest-offset replay upserts them again.
ALTER TABLE sourcing_decisions
    ADD COLUMN carrier_assignment_id TEXT CHECK (carrier_assignment_id <> '');

-- The split-shipment report groups the decisions of one order.
CREATE INDEX idx_sourcing_decisions_order ON sourcing_decisions (order_id);
