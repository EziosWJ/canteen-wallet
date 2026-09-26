CREATE TABLE import_previews (
    id TEXT PRIMARY KEY,
    administrator_id INTEGER NOT NULL REFERENCES administrators(id),
    rows_json TEXT NOT NULL,
    errors_json TEXT NOT NULL,
    confirmed_at TEXT,
    created_at TEXT NOT NULL
);
