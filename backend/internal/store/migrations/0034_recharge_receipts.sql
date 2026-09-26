CREATE TABLE recharge_receipts (
    id INTEGER PRIMARY KEY,
    receipt_ref TEXT NOT NULL UNIQUE CHECK (length(receipt_ref) BETWEEN 1 AND 128 AND length(trim(receipt_ref)) > 0),
    employee_id INTEGER NOT NULL REFERENCES employees(id),
    amount_cents INTEGER NOT NULL CHECK (typeof(amount_cents) = 'integer' AND amount_cents > 0),
    collected_at TEXT NOT NULL,
    payment_method TEXT NOT NULL CHECK (payment_method IN ('CASH', 'BANK_TRANSFER', 'OTHER')),
    recorded_by INTEGER NOT NULL REFERENCES administrators(id),
    recharge_transaction_id INTEGER NOT NULL UNIQUE REFERENCES transactions(id),
    created_at TEXT NOT NULL
);
