DROP INDEX IF EXISTS assignments_consolidation_idx;
ALTER TABLE assignments
    DROP COLUMN IF EXISTS members,
    DROP COLUMN IF EXISTS consolidated_into,
    DROP COLUMN IF EXISTS consolidation_key,
    DROP COLUMN IF EXISTS deliver_by_earliest;
