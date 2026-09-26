CREATE TABLE daily_reconciliation (
    business_date TEXT PRIMARY KEY,
    opening_cents INTEGER NOT NULL,
    movement_cents INTEGER NOT NULL,
    expected_cents INTEGER NOT NULL,
    actual_cents INTEGER NOT NULL,
    difference_cents INTEGER NOT NULL,
    generated_by INTEGER NOT NULL REFERENCES administrators(id),
    generated_at TEXT NOT NULL
);
