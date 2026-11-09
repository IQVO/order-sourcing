ALTER TABLE rm_lanes
    DROP COLUMN cost_per_unit_cents,
    DROP COLUMN cost_declared_at;
ALTER TABLE rm_waves
    DROP COLUMN cost_per_stop_cents,
    DROP COLUMN cost_declared_at;
