-- ADR 0014: the versioned SourcingPolicy. One row per published version, every
-- version is kept; the current policy is the highest version and version 0 (no
-- row) is the built-in default. The snapshot is jsonb, as in the sibling
-- PromisePolicy of delivery-promise, so a later additive policy field needs no
-- migration.
CREATE TABLE sourcing_policies (
    version   integer     PRIMARY KEY CHECK (version >= 1),
    snapshot  jsonb       NOT NULL
);
