-- The seven migrations that built this schema are squashed into one. They were
-- a history of a database nobody else runs, and the first of them seeded two
-- real children's names into every fresh install.
--
-- A database that predates the squash was at version 7 and has had rows 2..7
-- pruned from goose_db_version by hand, so it reads as version 1 — exactly
-- what this file creates. Numbering therefore continues at 0002 as normal.
--
-- Do that pruning *after* deploying an image that contains this file, never
-- before: an older image still carries migrations 2..7, and on a restart it
-- would try to re-apply them against tables that already have those columns.

-- +goose Up
CREATE TABLE persons (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    kind       TEXT    NOT NULL DEFAULT 'child' CHECK (kind IN ('child','adult')),
    active     INTEGER NOT NULL DEFAULT 1,
    notes      TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE trainers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    notes      TEXT NOT NULL DEFAULT '',
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE trainer_absences (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    trainer_id INTEGER NOT NULL REFERENCES trainers(id),
    date_from  TEXT NOT NULL,  -- YYYY-MM-DD, inclusive
    date_to    TEXT NOT NULL,  -- YYYY-MM-DD, inclusive
    kind       TEXT NOT NULL DEFAULT 'vacation',  -- vacation | sick | other
    comment    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
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
    created_at    TEXT    NOT NULL DEFAULT (datetime('now')),
    -- Trailing because ALTER TABLE put it there. Column order is part of the
    -- shape a squash promises to reproduce, so it stays where production has
    -- it rather than where it reads best.
    trainer_id    INTEGER REFERENCES trainers(id)
);

CREATE TABLE regular_slots (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER NOT NULL REFERENCES enrollments(id) ON DELETE CASCADE,
    weekday       INTEGER NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    time          TEXT    NOT NULL,
    active        INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now')),
    duration_min  INTEGER NOT NULL DEFAULT 60   -- trailing: added by ALTER TABLE
);

CREATE TABLE visits (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER NOT NULL REFERENCES enrollments(id),
    date          TEXT    NOT NULL,
    status        TEXT    NOT NULL CHECK (status IN ('done','rescheduled','cancelled','skipped')),
    comment       TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

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

CREATE TABLE appointments (
    id           INTEGER PRIMARY KEY,
    title        TEXT    NOT NULL,              -- what: Педикюр / Ортодонт / ...
    person       TEXT    NOT NULL DEFAULT '',   -- who, as written by whoever asked
    location     TEXT    NOT NULL DEFAULT '',
    starts_at    TEXT    NOT NULL,              -- ISO local datetime: 2006-01-02T15:04
    ends_at      TEXT,                          -- optional
    status       TEXT    NOT NULL DEFAULT 'planned', -- planned|done|cancelled
    note         TEXT    NOT NULL DEFAULT '',
    raw          TEXT    NOT NULL DEFAULT '',   -- original text the parse came from

    -- HA export bookkeeping (outbox); the exporter is added later, no schema churn.
    ha_uid       TEXT,                          -- calendar event uid once pushed
    ha_synced_at TEXT,                          -- when last successfully pushed
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),
    deleted_at   TEXT,                          -- soft delete; keeps HA export able to issue a delete

    -- Trailing because ALTER TABLE put them there; see the note on enrollments.
    cost         REAL,                          -- NULL means nobody wrote it down; 0 means free
    cost_prompt_msg_id INTEGER                  -- the "how much was it?" message awaiting a reply
);

CREATE INDEX idx_enrollments_person ON enrollments(person_id);
CREATE INDEX idx_regular_slots_enrollment ON regular_slots(enrollment_id);
CREATE INDEX idx_visits_date ON visits(date);
CREATE UNIQUE INDEX idx_visits_enrollment_date ON visits(enrollment_id, date);
CREATE INDEX idx_payments_date ON payments(date);
CREATE INDEX idx_payments_enrollment ON payments(enrollment_id);
CREATE INDEX idx_appointments_starts_at ON appointments (starts_at);
CREATE INDEX idx_appointments_sync ON appointments (ha_synced_at, updated_at);
CREATE INDEX idx_appointments_cost_prompt ON appointments (cost_prompt_msg_id);

-- +goose Down
-- The inverse of "create the schema" is "drop the schema". Said plainly here
-- because the migration this replaces claimed to undo a two-row seed and in
-- fact deleted every person in the database.
DROP TABLE IF EXISTS appointments;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS visits;
DROP TABLE IF EXISTS regular_slots;
DROP TABLE IF EXISTS enrollments;
DROP TABLE IF EXISTS trainer_absences;
DROP TABLE IF EXISTS trainers;
DROP TABLE IF EXISTS persons;
