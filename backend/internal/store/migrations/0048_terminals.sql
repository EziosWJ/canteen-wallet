CREATE TABLE terminals (
    id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    credential_hash BLOB NOT NULL UNIQUE,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    last_seen_at TEXT,
    scanner_status TEXT NOT NULL DEFAULT 'UNKNOWN',
    voice_status TEXT NOT NULL DEFAULT 'UNKNOWN',
    created_at TEXT NOT NULL
);
