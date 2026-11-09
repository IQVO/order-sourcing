-- order-sourcing analytics read model (ADR 0013).
--
-- This is the ANALYTICAL database, separate from the OLTP database. It is
-- written only by cmd/os-projector and read (read-only) by cmd/os-reports.
-- Everything here is a projection of this context's own topics
-- (retailnetwork.order-sourcing.events and retailnetwork.order-sourcing.analytics),
-- not a source of truth, and can be dropped and rebuilt from them.

-- Idempotency: every applied CloudEvents id is recorded here exactly once, in
-- the SAME transaction as its effect on a fact table. occurred_at is the
-- event's CloudEvents `time` (the basis of /reports/freshness).
CREATE TABLE analytics_processed_events (
    event_id    TEXT        PRIMARY KEY,
    event_type  TEXT        NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per assignment, from FulfillmentQueued (analytics topic): the
-- shipment group of an order and when it was queued (CloudEvents time).
CREATE TABLE queued_groups (
    assignment_id TEXT        PRIMARY KEY CHECK (assignment_id <> ''),
    order_id      TEXT        NOT NULL CHECK (order_id <> ''),
    group_no      INTEGER     NOT NULL CHECK (group_no >= 1),
    queued_at     TIMESTAMPTZ NOT NULL,
    event_id      TEXT        NOT NULL
);

-- One row per release, from FulfillmentSourcingDecided (analytics topic). The
-- regions and in_region are NULL when unknown; estimated_cost_cents is present
-- exactly when cost_complete (ADR 0011).
CREATE TABLE sourcing_decisions (
    assignment_id         TEXT        NOT NULL CHECK (assignment_id <> ''),
    release_no            INTEGER     NOT NULL CHECK (release_no >= 1),
    order_id              TEXT        NOT NULL CHECK (order_id <> ''),
    group_no              INTEGER,
    node_id               TEXT        NOT NULL CHECK (node_id <> ''),
    node_region_id        TEXT,
    destination_region_id TEXT,
    in_region             BOOLEAN,
    legs                  INTEGER     NOT NULL CHECK (legs >= 0),
    estimated_cost_cents  BIGINT      CHECK (estimated_cost_cents >= 0),
    cost_complete         BOOLEAN     NOT NULL,
    units                 INTEGER     NOT NULL CHECK (units >= 1),
    candidate_count       INTEGER     NOT NULL CHECK (candidate_count >= 1),
    decided_at            TIMESTAMPTZ NOT NULL,
    event_id              TEXT        NOT NULL,
    PRIMARY KEY (assignment_id, release_no),
    CONSTRAINT sourcing_decisions_cost_complete CHECK (cost_complete = (estimated_cost_cents IS NOT NULL))
);

-- One row per FulfillmentUnsourceable (events topic), on its CloudEvents time.
CREATE TABLE unsourceable_declarations (
    event_id      TEXT        PRIMARY KEY,
    assignment_id TEXT        NOT NULL CHECK (assignment_id <> ''),
    order_id      TEXT        NOT NULL CHECK (order_id <> ''),
    declared_at   TIMESTAMPTZ NOT NULL
);

-- The reports filter each fact on its instant.
CREATE INDEX idx_queued_groups_order ON queued_groups (order_id);
CREATE INDEX idx_sourcing_decisions_decided_at ON sourcing_decisions (decided_at);
CREATE INDEX idx_unsourceable_declarations_declared_at ON unsourceable_declarations (declared_at);
CREATE INDEX idx_analytics_processed_events_occurred_at ON analytics_processed_events (occurred_at);
