-- REST guideline: the Idempotency-Key is global (looked up by key alone and
-- compared by request fingerprint = method + path + sha256(body)). Rows stored
-- under the retired (route, key) design carry no fingerprint and belong to the
-- retired route, so they are dropped.
DELETE FROM idempotency_keys;
ALTER TABLE idempotency_keys DROP CONSTRAINT idempotency_keys_pkey;
ALTER TABLE idempotency_keys DROP COLUMN route;
ALTER TABLE idempotency_keys ADD COLUMN fingerprint text NOT NULL;
ALTER TABLE idempotency_keys ADD PRIMARY KEY (key);
