CREATE TABLE receipt_reviews (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL UNIQUE REFERENCES transactions(id),
    reviewer_id INTEGER NOT NULL REFERENCES administrators(id),
    status TEXT NOT NULL CHECK (status IN ('MATCHED', 'DIFFERENCE', 'RESOLVED')),
    note TEXT NOT NULL DEFAULT '',
    reviewed_at TEXT NOT NULL
);
