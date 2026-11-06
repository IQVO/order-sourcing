-- Roadmap F7: the planner-declared costs of the money-denominated AssignmentCost
-- (ADR 0011). rm_lanes gains the per-unit lane cost, rm_waves the per-stop wave
-- cost; cost_declared_at NULL means no declaration was seen (the component
-- contributes 0 to an estimate and marks it incomplete).
ALTER TABLE rm_lanes
    ADD COLUMN cost_per_unit_cents bigint     NOT NULL DEFAULT 0,
    ADD COLUMN cost_declared_at    timestamptz;
ALTER TABLE rm_waves
    ADD COLUMN cost_per_stop_cents bigint     NOT NULL DEFAULT 0,
    ADD COLUMN cost_declared_at    timestamptz;
