ALTER TABLE protected_rooms
    ADD COLUMN IF NOT EXISTS host_key_hash BYTEA NULL
        CHECK (host_key_hash IS NULL OR octet_length(host_key_hash) = 32);

ALTER TABLE protected_rooms
    ADD COLUMN IF NOT EXISTS ended_at TIMESTAMPTZ NULL;
