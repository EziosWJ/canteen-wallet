CREATE TABLE transactions (
    id INTEGER PRIMARY KEY,
    transaction_no TEXT NOT NULL UNIQUE CHECK (length(transaction_no) BETWEEN 1 AND 64),
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    type TEXT NOT NULL CHECK (type IN ('RECHARGE', 'RECHARGE_REVERSAL', 'CONSUME', 'REFUND', 'BALANCE_ADJUSTMENT', 'BALANCE_WITHDRAWAL')),
    amount INTEGER NOT NULL CHECK (typeof(amount) = 'integer' AND amount != 0),
    before_balance INTEGER NOT NULL CHECK (typeof(before_balance) = 'integer' AND before_balance >= 0),
    after_balance INTEGER NOT NULL CHECK (typeof(after_balance) = 'integer' AND after_balance >= 0 AND after_balance = before_balance + amount),
    administrator_id INTEGER REFERENCES administrators(id),
    terminal_id TEXT,
    business_type TEXT NOT NULL CHECK (length(business_type) BETWEEN 1 AND 64),
    business_id TEXT NOT NULL CHECK (length(business_id) BETWEEN 1 AND 128),
    related_transaction_id INTEGER REFERENCES transactions(id),
    idempotency_key TEXT NOT NULL UNIQUE CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    reason TEXT,
    created_at TEXT NOT NULL,
    CHECK ((administrator_id IS NOT NULL) != (terminal_id IS NOT NULL)),
    CHECK (terminal_id IS NULL OR length(terminal_id) BETWEEN 1 AND 64),
    CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 512),
    CHECK (type != 'BALANCE_ADJUSTMENT' OR (reason IS NOT NULL AND length(trim(reason)) > 0)),
    CHECK (type = 'BALANCE_ADJUSTMENT' OR
        (type IN ('RECHARGE', 'REFUND') AND amount > 0) OR
        (type IN ('RECHARGE_REVERSAL', 'CONSUME', 'BALANCE_WITHDRAWAL') AND amount < 0))
);
