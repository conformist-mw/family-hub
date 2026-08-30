-- Version the weekly schedule, so changing it stops rewriting the past.
--
-- Editing a regular_slots row moved the lesson everywhere at once: the feed,
-- the evening reminder and any "what was the schedule in September" question
-- all read the current row, so moving Логопед from Tuesday to Thursday claimed
-- it had always been Thursday. Attendance was safe — visits are dated rows —
-- but the schedule itself had no history.
--
-- Same split as reminder_rules, for the same reason and with the same shape:
--
--   regular_slots   that this course has a weekly slot at all — identity
--   slot_versions   when it happened, and from when — a LIST of versions
--
-- A window is then expanded with the version that was in force over it, so
-- September keeps September's schedule no matter what today's says.
--
-- Why valid_from_at is a datetime and not a date: a change made "from today"
-- at 10:00 must not claim this morning's 08:00 lesson, which already happened
-- under the old schedule.

-- +goose Up

CREATE TABLE slot_versions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    slot_id       INTEGER NOT NULL REFERENCES regular_slots(id) ON DELETE CASCADE,
    valid_from_at TEXT    NOT NULL CHECK (valid_from_at <> ''),  -- ISO local datetime
    weekday       INTEGER NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    time          TEXT    NOT NULL,
    duration_min  INTEGER NOT NULL DEFAULT 60,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    UNIQUE (slot_id, valid_from_at)
);

-- Every existing slot becomes its own first version, reaching back
-- indefinitely. Not the slot's created_at: these courses predate the app, so
-- "it started when somebody typed it in" would be a claim the data does not
-- support. A floor of 2000 says the honest thing — as far as anything here
-- records, it was always this.
INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time, duration_min)
SELECT id, '2000-01-01T00:00', weekday, time, duration_min FROM regular_slots;

ALTER TABLE regular_slots DROP COLUMN weekday;
ALTER TABLE regular_slots DROP COLUMN time;
ALTER TABLE regular_slots DROP COLUMN duration_min;

CREATE INDEX idx_slot_versions_slot ON slot_versions(slot_id, valid_from_at);

-- +goose Down
-- Collapses the history back onto the row: the newest version wins and every
-- earlier one is lost, which is exactly the behaviour this migration removes.
ALTER TABLE regular_slots ADD COLUMN weekday INTEGER NOT NULL DEFAULT 0;
ALTER TABLE regular_slots ADD COLUMN time TEXT NOT NULL DEFAULT '';
ALTER TABLE regular_slots ADD COLUMN duration_min INTEGER NOT NULL DEFAULT 60;

UPDATE regular_slots SET
    weekday      = COALESCE((SELECT v.weekday      FROM slot_versions v WHERE v.slot_id = regular_slots.id ORDER BY v.valid_from_at DESC, v.id DESC LIMIT 1), 0),
    time         = COALESCE((SELECT v.time         FROM slot_versions v WHERE v.slot_id = regular_slots.id ORDER BY v.valid_from_at DESC, v.id DESC LIMIT 1), ''),
    duration_min = COALESCE((SELECT v.duration_min FROM slot_versions v WHERE v.slot_id = regular_slots.id ORDER BY v.valid_from_at DESC, v.id DESC LIMIT 1), 60);

DROP INDEX IF EXISTS idx_slot_versions_slot;
DROP TABLE IF EXISTS slot_versions;
