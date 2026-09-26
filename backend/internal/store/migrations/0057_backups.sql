CREATE TABLE backups (
    id INTEGER PRIMARY KEY,
    filename TEXT NOT NULL,
    local_path TEXT NOT NULL,
    external_path TEXT,
    status TEXT NOT NULL CHECK (status IN ('COMPLETE', 'EXTERNAL_FAILED')),
    created_by INTEGER REFERENCES administrators(id),
    created_at TEXT NOT NULL
);
