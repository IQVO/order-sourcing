DELETE FROM idempotency_keys;
ALTER TABLE idempotency_keys DROP CONSTRAINT idempotency_keys_pkey;
ALTER TABLE idempotency_keys DROP COLUMN fingerprint;
ALTER TABLE idempotency_keys ADD COLUMN route text NOT NULL;
ALTER TABLE idempotency_keys ADD PRIMARY KEY (route, key);
