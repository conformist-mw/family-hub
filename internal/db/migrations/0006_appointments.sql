-- +goose Up
-- appointments: one-off scheduled visits (manicure, orthodontist, ...).
-- SQLite is the source of truth; HA is an export target (outbox pattern):
-- ha_uid + ha_synced_at track what has been pushed, updated_at drives re-sync.
CREATE TABLE appointments (
    id           INTEGER PRIMARY KEY,
    title        TEXT    NOT NULL,              -- what: Педикюр / Ортодонт / ...
    person       TEXT    NOT NULL DEFAULT '',   -- who, as written: я / Олєжа / обоє
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
    deleted_at   TEXT                           -- soft delete; keeps HA export able to issue a delete
);

CREATE INDEX idx_appointments_starts_at ON appointments (starts_at);
CREATE INDEX idx_appointments_sync
    ON appointments (ha_synced_at, updated_at);

-- +goose Down
DROP TABLE appointments;
