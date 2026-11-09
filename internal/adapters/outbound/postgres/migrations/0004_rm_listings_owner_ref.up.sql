-- Network gap G47 (ADR 0012): AvailabilityView is keyed by (catalogue item,
-- node, owner). owner_ref '' is operator-owned (never NULL in a key): the
-- default backfills every existing row at ''. listing_id stays the primary key
-- because ListingSuppressed / ListingRestored address a listing by id only and
-- may arrive before ListingCreated (a stub row with no item yet); the unique
-- index covers every row whose item is known.
ALTER TABLE rm_listings
    ADD COLUMN owner_ref text COLLATE "C" NOT NULL DEFAULT '';
CREATE UNIQUE INDEX rm_listings_item_node_owner_key
    ON rm_listings (catalog_item_id, node_id, owner_ref) WHERE catalog_item_id <> '';
