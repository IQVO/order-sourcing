DROP INDEX IF EXISTS idx_sourcing_decisions_order;
ALTER TABLE sourcing_decisions DROP COLUMN IF EXISTS carrier_assignment_id;
