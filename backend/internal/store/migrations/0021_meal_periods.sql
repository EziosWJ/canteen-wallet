CREATE TABLE meal_periods (
    id INTEGER PRIMARY KEY,
    code TEXT NOT NULL UNIQUE CHECK (code IN ('BREAKFAST', 'LUNCH', 'DINNER')),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    start_minute INTEGER NOT NULL CHECK (start_minute BETWEEN 0 AND 1439),
    end_minute INTEGER NOT NULL CHECK (end_minute BETWEEN 1 AND 1440 AND end_minute > start_minute),
    price_cents INTEGER NOT NULL DEFAULT 0 CHECK (typeof(price_cents) = 'integer' AND price_cents >= 0),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1) AND (enabled = 0 OR price_cents > 0)),
    updated_at TEXT NOT NULL
);
