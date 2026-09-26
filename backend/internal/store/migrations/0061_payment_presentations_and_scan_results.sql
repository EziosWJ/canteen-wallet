CREATE TABLE payment_presentations (
    id TEXT PRIMARY KEY,
    employee_id INTEGER NOT NULL REFERENCES employees(id),
    session_id INTEGER NOT NULL REFERENCES employee_sessions(id),
    state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'SUCCESS', 'FAILED')),
    result_code TEXT,
    transaction_id INTEGER REFERENCES transactions(id),
    created_at TEXT NOT NULL
);
CREATE INDEX payment_presentations_by_session ON payment_presentations(session_id, created_at DESC);

ALTER TABLE payment_tokens ADD COLUMN presentation_id TEXT REFERENCES payment_presentations(id);
ALTER TABLE payment_tokens ADD COLUMN result_code TEXT;
ALTER TABLE payment_tokens ADD COLUMN result_message TEXT;
CREATE INDEX payment_tokens_by_presentation ON payment_tokens(presentation_id, id);

ALTER TABLE pending_consumptions RENAME TO pending_consumptions_old;
CREATE TABLE pending_consumptions (
    id TEXT PRIMARY KEY,
    token_id INTEGER NOT NULL UNIQUE REFERENCES payment_tokens(id),
    terminal_id TEXT NOT NULL REFERENCES terminals(id),
    employee_id INTEGER NOT NULL REFERENCES employees(id),
    meal_code TEXT NOT NULL,
    meal_name TEXT NOT NULL,
    business_date TEXT NOT NULL,
    amount_cents INTEGER NOT NULL CHECK (amount_cents > 0),
    meal_end_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'PROCESSED', 'EXPIRED', 'CANCELLED', 'FAILED')),
    result_code TEXT,
    transaction_id INTEGER REFERENCES transactions(id),
    created_at TEXT NOT NULL
);
INSERT INTO pending_consumptions
    (id, token_id, terminal_id, employee_id, meal_code, meal_name, business_date,
     amount_cents, meal_end_at, expires_at, state, result_code, transaction_id, created_at)
SELECT old.id, old.token_id, old.terminal_id, token.employee_id, old.meal_code,
       COALESCE(meal.name, old.meal_code), old.business_date, old.amount_cents,
       old.expires_at, old.expires_at,
       CASE WHEN old.state = 'PENDING' AND (old.expires_at <= CAST(strftime('%s','now') AS INTEGER) OR EXISTS (
           SELECT 1 FROM pending_consumptions_old newer
           JOIN payment_tokens newer_token ON newer_token.id = newer.token_id
           WHERE newer_token.employee_id = token.employee_id
             AND newer.business_date = old.business_date AND newer.meal_code = old.meal_code
             AND newer.state = 'PENDING' AND newer.rowid > old.rowid
       )) THEN 'EXPIRED' ELSE old.state END,
       CASE WHEN old.state = 'EXPIRED' OR old.expires_at <= CAST(strftime('%s','now') AS INTEGER)
            THEN 'PENDING_EXPIRED' ELSE NULL END,
       old.transaction_id, old.created_at
FROM pending_consumptions_old old
JOIN payment_tokens token ON token.id = old.token_id
LEFT JOIN meal_periods meal ON meal.code = old.meal_code;
DROP TABLE pending_consumptions_old;
CREATE UNIQUE INDEX pending_one_active_meal ON pending_consumptions(employee_id, business_date, meal_code)
    WHERE state = 'PENDING';

ALTER TABLE scan_events ADD COLUMN payment_token_id INTEGER REFERENCES payment_tokens(id);
ALTER TABLE scan_events ADD COLUMN business_date TEXT;
ALTER TABLE scan_events ADD COLUMN meal_code TEXT;
ALTER TABLE scan_events ADD COLUMN received_at_ms INTEGER;
ALTER TABLE scan_events ADD COLUMN result_json TEXT;
ALTER TABLE scan_events ADD COLUMN first_event_id INTEGER REFERENCES scan_events(id);
CREATE INDEX scan_events_dedup ON scan_events(employee_id, business_date, meal_code, received_at_ms DESC);

CREATE TABLE consumption_details (
    transaction_id INTEGER PRIMARY KEY REFERENCES transactions(id),
    meal_code TEXT NOT NULL,
    meal_name TEXT NOT NULL,
    business_date TEXT NOT NULL,
    amount_cents INTEGER NOT NULL CHECK (amount_cents > 0)
);
CREATE TRIGGER consumption_details_no_update BEFORE UPDATE ON consumption_details
BEGIN SELECT RAISE(ABORT, 'consumption details are immutable'); END;
CREATE TRIGGER consumption_details_no_delete BEFORE DELETE ON consumption_details
BEGIN SELECT RAISE(ABORT, 'consumption details are immutable'); END;
CREATE TRIGGER pending_consumption_snapshot_immutable BEFORE UPDATE OF
    token_id,terminal_id,employee_id,meal_code,meal_name,business_date,amount_cents,meal_end_at,created_at
ON pending_consumptions
BEGIN SELECT RAISE(ABORT, 'pending consumption snapshot is immutable'); END;
