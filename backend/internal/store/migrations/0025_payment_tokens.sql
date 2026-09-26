CREATE TABLE payment_tokens (
    id INTEGER PRIMARY KEY,
    token_hash BLOB NOT NULL UNIQUE,
    employee_id INTEGER NOT NULL REFERENCES employees(id),
    session_id INTEGER NOT NULL REFERENCES employee_sessions(id),
    issued_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at > issued_at),
    state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'PROCESSED', 'EXPIRED', 'REVOKED')),
    transaction_id INTEGER REFERENCES transactions(id)
);
