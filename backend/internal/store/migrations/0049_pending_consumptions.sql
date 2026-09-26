CREATE TABLE pending_consumptions (
    id TEXT PRIMARY KEY,
    token_id INTEGER NOT NULL UNIQUE REFERENCES payment_tokens(id),
    terminal_id TEXT NOT NULL REFERENCES terminals(id),
    meal_code TEXT NOT NULL,
    business_date TEXT NOT NULL,
    amount_cents INTEGER NOT NULL CHECK (amount_cents > 0),
    expires_at INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'PROCESSED', 'EXPIRED')),
    transaction_id INTEGER REFERENCES transactions(id),
    created_at TEXT NOT NULL
);
