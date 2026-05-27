-- +goose Up
CREATE TABLE persons (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    kind       TEXT    NOT NULL DEFAULT 'child' CHECK (kind IN ('child','adult')),
    active     INTEGER NOT NULL DEFAULT 1,
    notes      TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE enrollments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    person_id     INTEGER NOT NULL REFERENCES persons(id),
    name          TEXT    NOT NULL,
    description   TEXT    NOT NULL DEFAULT '',
    billing_type  TEXT    NOT NULL CHECK (billing_type IN ('per_lesson','monthly')),
    current_price REAL    NOT NULL,
    low_threshold INTEGER NOT NULL DEFAULT 2,
    active        INTEGER NOT NULL DEFAULT 1,
    notes         TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_enrollments_person ON enrollments(person_id);

CREATE TABLE regular_slots (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER NOT NULL REFERENCES enrollments(id) ON DELETE CASCADE,
    weekday       INTEGER NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    time          TEXT    NOT NULL,
    active        INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_regular_slots_enrollment ON regular_slots(enrollment_id);

CREATE TABLE visits (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER NOT NULL REFERENCES enrollments(id),
    date          TEXT    NOT NULL,
    status        TEXT    NOT NULL CHECK (status IN ('done','rescheduled','cancelled','skipped')),
    comment       TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_visits_enrollment_date ON visits(enrollment_id, date);
CREATE INDEX idx_visits_date             ON visits(date);

CREATE TABLE payments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER NOT NULL REFERENCES enrollments(id),
    date          TEXT    NOT NULL,
    amount        REAL    NOT NULL,
    lessons_paid  INTEGER,
    covers_from   TEXT,
    covers_until  TEXT,
    comment       TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_payments_enrollment ON payments(enrollment_id);
CREATE INDEX idx_payments_date       ON payments(date);

-- +goose Down
DROP TABLE payments;
DROP TABLE visits;
DROP TABLE regular_slots;
DROP TABLE enrollments;
DROP TABLE persons;
