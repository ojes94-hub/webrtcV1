CREATE TABLE IF NOT EXISTS protected_rooms (
    meeting_id VARCHAR(10) PRIMARY KEY
        CHECK (meeting_id ~ '^[0-9]{10}$'),
    room_id VARCHAR(64) NOT NULL UNIQUE
        CHECK (room_id ~ '^[A-Za-z0-9_-]{3,64}$'),
    password_hash BYTEA NOT NULL
        CHECK (octet_length(password_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
