DROP INDEX IF EXISTS idx_analytics_processed_events_occurred_at;
DROP INDEX IF EXISTS idx_unsourceable_declarations_declared_at;
DROP INDEX IF EXISTS idx_sourcing_decisions_decided_at;
DROP INDEX IF EXISTS idx_queued_groups_order;
DROP TABLE IF EXISTS unsourceable_declarations;
DROP TABLE IF EXISTS sourcing_decisions;
DROP TABLE IF EXISTS queued_groups;
DROP TABLE IF EXISTS analytics_processed_events;
