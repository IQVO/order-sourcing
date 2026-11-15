ALTER TABLE rm_lanes
    DROP COLUMN emission_per_unit_g,
    DROP COLUMN emission_declared_at;
ALTER TABLE rm_waves
    DROP COLUMN emission_per_stop_g,
    DROP COLUMN emission_declared_at;
