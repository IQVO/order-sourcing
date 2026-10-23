-- order-sourcing schema (ADR 0002, ADR 0003).

CREATE TABLE assignments (
    assignment_id             text PRIMARY KEY,
    order_id                  text        NOT NULL,
    group_no                  integer     NOT NULL CHECK (group_no >= 1),
    destination_postal_prefix text        NOT NULL,
    promise_id                text        NOT NULL,
    tier                      text        NOT NULL,
    deliver_by_latest         timestamptz NOT NULL,
    witness_node_id           text        NOT NULL DEFAULT '',
    revision_no               integer     NOT NULL DEFAULT 0,
    lines                     jsonb       NOT NULL,
    state                     text        NOT NULL,
    node_id                   text        NOT NULL DEFAULT '',
    candidate                 jsonb,
    release_at                timestamptz,
    releases                  jsonb       NOT NULL DEFAULT '[]',
    excluded_nodes            jsonb       NOT NULL DEFAULT '[]',
    version                   bigint      NOT NULL CHECK (version >= 1),
    queued_at                 timestamptz NOT NULL,
    updated_at                timestamptz NOT NULL,
    locked_at                 timestamptz,
    lock_reason               text        NOT NULL DEFAULT '',
    pickup_departure_id       text        NOT NULL DEFAULT '',
    tracking_numbers          jsonb       NOT NULL DEFAULT '[]',
    shipped_at                timestamptz,
    delivered                 jsonb       NOT NULL DEFAULT '[]',
    delivered_at              timestamptz,
    cancel_reason             text        NOT NULL DEFAULT '',
    CONSTRAINT assignments_order_group_key UNIQUE (order_id, group_no)
);
CREATE INDEX assignments_promise_idx ON assignments (promise_id);
CREATE INDEX assignments_state_node_idx ON assignments (state, node_id);
CREATE INDEX assignments_queue_idx ON assignments (release_at, assignment_id) WHERE state = 'QUEUED';

-- NodeProfileView
CREATE TABLE rm_nodes (
    node_id           text PRIMARY KEY,
    node_type         text        NOT NULL DEFAULT '',
    subtype           text        NOT NULL DEFAULT '',
    operator          text        NOT NULL DEFAULT '',
    region_id         text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT '',
    status_at         timestamptz,
    profile_revision  integer     NOT NULL DEFAULT 0,
    handling_minutes  integer     NOT NULL DEFAULT 0,
    sort_dwell_minutes integer    NOT NULL DEFAULT 0
);

-- NetworkGraphView
CREATE TABLE rm_lanes (
    lane_id     text PRIMARY KEY,
    origin      text    NOT NULL DEFAULT '',
    destination text    NOT NULL DEFAULT '',
    mode        text    NOT NULL DEFAULT '',
    closed      boolean NOT NULL DEFAULT false
);
CREATE TABLE rm_departures (
    departure_id    text PRIMARY KEY,
    lane_id         text        NOT NULL DEFAULT '',
    kind            text        NOT NULL DEFAULT '',
    origin          text        NOT NULL DEFAULT '',
    destination     text        NOT NULL DEFAULT '',
    cutoff_at       timestamptz,
    departs_at      timestamptz,
    arrives_at      timestamptz,
    capacity        integer     NOT NULL DEFAULT 0,
    remaining       integer     NOT NULL DEFAULT 0,
    remaining_known boolean     NOT NULL DEFAULT false,
    scheduled       boolean     NOT NULL DEFAULT false,
    cancelled       boolean     NOT NULL DEFAULT false
);
CREATE INDEX rm_departures_cutoff_idx ON rm_departures (cutoff_at) WHERE scheduled;
CREATE TABLE rm_waves (
    wave_id           text PRIMARY KEY,
    station_id        text        NOT NULL DEFAULT '',
    induct_cutoff_at  timestamptz,
    window_start      timestamptz,
    window_end        timestamptz,
    capacity          integer     NOT NULL DEFAULT 0,
    remaining         integer     NOT NULL DEFAULT 0,
    remaining_known   boolean     NOT NULL DEFAULT false,
    scheduled         boolean     NOT NULL DEFAULT false,
    cancelled         boolean     NOT NULL DEFAULT false
);
CREATE INDEX rm_waves_station_idx ON rm_waves (station_id, induct_cutoff_at) WHERE scheduled;
CREATE TABLE rm_service_areas (
    postal_prefix text PRIMARY KEY,
    station_id    text    NOT NULL,
    region_id     text    NOT NULL,
    version       integer NOT NULL
);
CREATE TABLE rm_regions (
    region_id text PRIMARY KEY,
    code      text NOT NULL DEFAULT '',
    name      text NOT NULL DEFAULT ''
);

-- AvailabilityView
CREATE TABLE rm_listings (
    listing_id      text PRIMARY KEY,
    catalog_item_id text        NOT NULL DEFAULT '',
    node_id         text        NOT NULL DEFAULT '',
    available_qty   integer     NOT NULL DEFAULT 0,
    as_of           timestamptz,
    suppressed      boolean     NOT NULL DEFAULT false
);
CREATE INDEX rm_listings_item_idx ON rm_listings (catalog_item_id);
CREATE TABLE rm_discontinued_items (
    catalog_item_id text PRIMARY KEY
);

-- ScorecardView
CREATE TABLE rm_scorecards (
    node_id     text        NOT NULL,
    score_window text       NOT NULL,
    grade       text        NOT NULL DEFAULT '',
    computed_at timestamptz,
    PRIMARY KEY (node_id, score_window)
);

-- PromiseBookPressureView
CREATE TABLE rm_book_pressure (
    book_id             text PRIMARY KEY,
    node_id             text NOT NULL DEFAULT '',
    pickup_departure_id text NOT NULL DEFAULT ''
);

-- Transactional outbox (written in the same transaction as the aggregate).
CREATE TABLE outbox_events (
    id           bigserial PRIMARY KEY,
    event_id     text        NOT NULL,
    topic        text        NOT NULL,
    event_type   text        NOT NULL,
    subject      text        NOT NULL,
    key          bytea       NOT NULL,
    dataschema   text        NOT NULL,
    value        bytea       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '[]',
    attempts     integer     NOT NULL DEFAULT 0,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_events_unpublished_idx ON outbox_events (id) WHERE published_at IS NULL;

-- Inbox: one row per consumed CloudEvents id, written with the effect.
CREATE TABLE processed_events (
    event_id     text PRIMARY KEY,
    event_type   text        NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- REST idempotency (route, key), replayed for 24 hours.
CREATE TABLE idempotency_keys (
    route      text        NOT NULL,
    key        text        NOT NULL,
    status     integer     NOT NULL,
    body       bytea       NOT NULL,
    headers    jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (route, key)
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
