CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY,
    administrator_id INTEGER REFERENCES administrators(id),
    action TEXT NOT NULL,
    subject_type TEXT,
    subject_id TEXT,
    details_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
