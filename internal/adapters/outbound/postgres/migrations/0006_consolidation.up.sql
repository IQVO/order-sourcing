-- ADR 0015: carrier and follower consolidation at release. The promise window
-- start (zero/NULL when unknown: a group queued before this migration), the
-- optional customer-ordering consolidation key, the carrier a follower follows
-- and, on a carrier, its follower ids. Every column is additive with a default,
-- so a row written before the migration reads as "never merged".
ALTER TABLE assignments
    ADD COLUMN deliver_by_earliest timestamptz NULL,
    ADD COLUMN consolidation_key   text        NOT NULL DEFAULT '',
    ADD COLUMN consolidated_into   text        NOT NULL DEFAULT '',
    ADD COLUMN members             jsonb       NOT NULL DEFAULT '[]';

-- The merge candidates of a carrier: QUEUED rows of one key, by latest delivery.
CREATE INDEX assignments_consolidation_idx
    ON assignments (consolidation_key, deliver_by_latest, assignment_id)
    WHERE state = 'QUEUED' AND consolidation_key <> '';
