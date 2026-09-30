-- Consumption entrances are switchable since SPEC-004: an administrator may
-- enable the payment code, the fixed self-service link, or both. The row is
-- pinned to id 1 and the table CHECK makes "at least one entrance" an invariant
-- the database itself enforces.
CREATE TABLE consumption_modes (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    payment_code INTEGER NOT NULL CHECK (payment_code IN (0, 1)),
    self_service INTEGER NOT NULL CHECK (self_service IN (0, 1)),
    updated_by INTEGER REFERENCES administrators(id),
    updated_at TEXT NOT NULL,
    CHECK (payment_code = 1 OR self_service = 1)
);
