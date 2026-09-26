CREATE TABLE employees (
    id INTEGER PRIMARY KEY,
    employee_no TEXT NOT NULL COLLATE NOCASE UNIQUE,
    name TEXT NOT NULL,
    phone TEXT NOT NULL UNIQUE,
    department TEXT NOT NULL,
    photo_url TEXT,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'FROZEN', 'CLOSED')),
    password_hash TEXT NOT NULL,
    must_change_password INTEGER NOT NULL DEFAULT 1 CHECK (must_change_password IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
