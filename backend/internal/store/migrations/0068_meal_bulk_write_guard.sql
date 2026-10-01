CREATE TABLE meal_periods_bulk_write_guard (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    active INTEGER NOT NULL CHECK (active IN (0, 1))
);
