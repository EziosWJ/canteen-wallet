CREATE TABLE self_service_intents (
    id TEXT PRIMARY KEY,
    employee_id INTEGER NOT NULL REFERENCES employees(id),
    session_id INTEGER NOT NULL REFERENCES employee_sessions(id),
    state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'CONSUMED', 'FAILED', 'SUPERSEDED')),
    meal_code TEXT NOT NULL,
    meal_name TEXT NOT NULL,
    business_date TEXT NOT NULL,
    amount_cents INTEGER NOT NULL CHECK (amount_cents > 0),
    existing_count INTEGER NOT NULL CHECK (existing_count >= 0),
    meal_end_at INTEGER NOT NULL,
    result_code TEXT,
    transaction_id INTEGER REFERENCES transactions(id),
    superseded_by TEXT REFERENCES self_service_intents(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX self_service_intents_by_session ON self_service_intents(session_id, created_at DESC);
CREATE UNIQUE INDEX self_service_intents_one_transaction ON self_service_intents(transaction_id)
    WHERE transaction_id IS NOT NULL;
CREATE TRIGGER self_service_intent_no_delete BEFORE DELETE ON self_service_intents
BEGIN SELECT RAISE(ABORT, 'self service intents are retained for audit'); END;
