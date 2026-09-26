CREATE TABLE manual_supplies (
    id INTEGER PRIMARY KEY,
    receipt_ref TEXT NOT NULL UNIQUE,
    employee_id INTEGER NOT NULL REFERENCES employees(id),
    meal_code TEXT NOT NULL,
    business_date TEXT NOT NULL,
    amount_cents INTEGER NOT NULL CHECK (amount_cents > 0),
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'POSTED', 'EXCEPTION')),
    note TEXT NOT NULL DEFAULT '',
    created_by INTEGER NOT NULL REFERENCES administrators(id),
    resolved_by INTEGER REFERENCES administrators(id),
    transaction_id INTEGER REFERENCES transactions(id),
    created_at TEXT NOT NULL,
    resolved_at TEXT
);
