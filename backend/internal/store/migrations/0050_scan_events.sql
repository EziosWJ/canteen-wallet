CREATE TABLE scan_events (
    id INTEGER PRIMARY KEY,
    terminal_id TEXT NOT NULL REFERENCES terminals(id),
    token_fingerprint TEXT,
    employee_id INTEGER REFERENCES employees(id),
    result_code TEXT NOT NULL,
    transaction_id INTEGER REFERENCES transactions(id),
    created_at TEXT NOT NULL
);
