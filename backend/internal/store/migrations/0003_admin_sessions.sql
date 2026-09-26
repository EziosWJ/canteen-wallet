CREATE TABLE admin_sessions (
    token_hash BLOB PRIMARY KEY,
    administrator_id INTEGER NOT NULL REFERENCES administrators(id),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);
