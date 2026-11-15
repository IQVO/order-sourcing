-- Roadmap C9: the planner-declared emissions of the carbon estimate (ADR 0017).
-- rm_lanes gains the per-unit lane emission, rm_waves the per-stop wave
-- emission, both in grams of CO2e; emission_declared_at NULL means no
-- declaration was seen (the component contributes 0 to the carbon estimate and
-- marks it incomplete).
ALTER TABLE rm_lanes
    ADD COLUMN emission_per_unit_g bigint     NOT NULL DEFAULT 0,
    ADD COLUMN emission_declared_at timestamptz;
ALTER TABLE rm_waves
    ADD COLUMN emission_per_stop_g bigint     NOT NULL DEFAULT 0,
    ADD COLUMN emission_declared_at timestamptz;
