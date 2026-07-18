-- +goose Up
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

ALTER TABLE enrollments ADD COLUMN trainer_id INTEGER REFERENCES trainers(id);

-- +goose Down
ALTER TABLE enrollments DROP COLUMN trainer_id;
DROP TABLE trainer_absences;
DROP TABLE trainers;
