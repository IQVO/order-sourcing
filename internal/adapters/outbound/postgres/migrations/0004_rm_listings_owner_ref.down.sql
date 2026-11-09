DROP INDEX IF EXISTS rm_listings_item_node_owner_key;
ALTER TABLE rm_listings
    DROP COLUMN owner_ref;
