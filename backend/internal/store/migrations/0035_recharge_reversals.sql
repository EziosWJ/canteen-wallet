CREATE TABLE recharge_reversals (
    id INTEGER PRIMARY KEY,
    recharge_transaction_id INTEGER NOT NULL UNIQUE REFERENCES transactions(id),
    reversal_transaction_id INTEGER NOT NULL UNIQUE REFERENCES transactions(id),
    administrator_id INTEGER NOT NULL REFERENCES administrators(id),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 512 AND length(trim(reason)) > 0),
    created_at TEXT NOT NULL
);
