-- The school-today integration is gone: the child no longer attends the
-- school whose parent portal it mirrored, so there is no timetable to sync, no
-- evening digest to send and no Friday review to collect.
--
-- The tables go with the code. school_lessons was only ever a rolling cache of
-- the portal and holds nothing the portal did not. The lesson details, marks
-- and files were a record, but a record of a school that is over, and nothing
-- in the app reads them any more — keeping tables nothing writes or reads is
-- how the next reader loses an afternoon working out who fills them in.

-- +goose Up
DROP INDEX IF EXISTS idx_school_lessons_window;
DROP TABLE IF EXISTS school_lessons;

DROP INDEX IF EXISTS idx_school_lesson_files_event;
DROP INDEX IF EXISTS idx_school_lesson_marks_event;
DROP INDEX IF EXISTS idx_school_lesson_details_window;
DROP TABLE IF EXISTS school_lesson_files;
DROP TABLE IF EXISTS school_lesson_marks;
DROP TABLE IF EXISTS school_lesson_details;

-- +goose Down
-- Recreated empty, in the shape 0007, 0010 and 0012 left them. The rows are
-- not restorable.
CREATE TABLE school_lessons (
    id          INTEGER PRIMARY KEY,
    event_id    INTEGER NOT NULL,
    pupil_id    INTEGER NOT NULL,
    subject     TEXT    NOT NULL CHECK (subject <> ''),
    starts_at   TEXT    NOT NULL CHECK (starts_at <> ''),
    ends_at     TEXT    NOT NULL CHECK (ends_at <> ''),
    topic       TEXT    NOT NULL DEFAULT '',
    has_marks   INTEGER NOT NULL DEFAULT 0,
    theme_color TEXT    NOT NULL DEFAULT '',
    synced_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    UNIQUE (event_id)
);

CREATE INDEX idx_school_lessons_window ON school_lessons(pupil_id, starts_at);

CREATE TABLE school_lesson_details (
    event_id      INTEGER PRIMARY KEY,
    pupil_id      INTEGER NOT NULL,
    starts_at     TEXT    NOT NULL CHECK (starts_at <> ''),
    subject       TEXT    NOT NULL CHECK (subject <> ''),
    teacher       TEXT    NOT NULL DEFAULT '',
    topic         TEXT    NOT NULL DEFAULT '',
    notes         TEXT    NOT NULL DEFAULT '',
    homework      TEXT    NOT NULL DEFAULT '',
    fetched_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),
    praise        TEXT    NOT NULL DEFAULT '',
    pupil_comment TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_school_lesson_details_window ON school_lesson_details(starts_at);

CREATE TABLE school_lesson_marks (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL,
    kind     TEXT    NOT NULL CHECK (kind <> ''),
    value    TEXT    NOT NULL CHECK (value <> '')
);

CREATE INDEX idx_school_lesson_marks_event ON school_lesson_marks(event_id);

CREATE TABLE school_lesson_files (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL,
    kind     TEXT    NOT NULL CHECK (kind IN ('homework', 'lesson')),
    url      TEXT    NOT NULL CHECK (url <> ''),
    title    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_school_lesson_files_event ON school_lesson_files(event_id);
