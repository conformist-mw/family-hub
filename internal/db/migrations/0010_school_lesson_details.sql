-- What actually happened at a school lesson: the topic, the teacher's notes,
-- the homework, the marks. Collected once a week from the school-today.com
-- portal's lesson detail page, which is the only place any of it exists — the
-- timetable JSON that feeds school_lessons carries a bare `hasMarks` boolean
-- and nothing else.
--
-- Deliberately NOT columns on school_lessons. That table is a rolling mirror:
-- ReplaceSchoolLessons wipes the whole [this Monday, +3 weeks) window every
-- sync, so anything written onto a lesson row is gone within twelve hours of
-- the review that collected it. These tables are the opposite kind of thing —
-- a record that accumulates and is never swept — so they keep their own
-- lifecycle and the mirror keeps its "no history" invariant intact.
--
-- Which is also why there is no FOREIGN KEY to school_lessons: the lesson row
-- is expected to vanish (it leaves the window, the school cancels it), and the
-- record of what happened at it must outlive that. event_id is the portal's
-- own stable id, unique across the source, so the join still works whenever
-- the lesson row happens to be there.
--
--   school_lesson_details  one lesson: topic, notes, homework, teacher
--   school_lesson_marks    the marks given at it (usually none, sometimes one)
--   school_lesson_files    attachments, stored as links only
--
-- The files table has no reader yet, on purpose. The homework a teacher sets
-- is sometimes only in the attached screenshot, and re-walking the portal for
-- past weeks is not possible once the week has scrolled out of the timetable
-- window — so the links are captured now and the renderer can start using them
-- whenever it wants. Not dead weight to be tidied away.

-- +goose Up

CREATE TABLE school_lesson_details (
    -- event_id is the primary key rather than the usual surrogate id + UNIQUE:
    -- every write is an upsert addressed by the portal's id, and there is no
    -- second way to name a row here.
    event_id   INTEGER PRIMARY KEY,
    pupil_id   INTEGER NOT NULL,
    -- Naive local wall-clock (model.LocalDatetime), copied from the timetable
    -- event. Carried so a week can be selected without joining a mirror row
    -- that may no longer exist.
    starts_at  TEXT    NOT NULL CHECK (starts_at <> ''),
    -- Taken from the timetable event, group tag and all ("Алгебра [9]"), the
    -- same shape the rest of the school code already strips and classifies.
    subject    TEXT    NOT NULL CHECK (subject <> ''),
    teacher    TEXT    NOT NULL DEFAULT '',
    -- The formal curriculum topic.
    topic      TEXT    NOT NULL DEFAULT '',
    -- What the class actually did, in the teacher's own words. Usually the
    -- more informative of the two.
    notes      TEXT    NOT NULL DEFAULT '',
    homework   TEXT    NOT NULL DEFAULT '',
    fetched_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);

-- The review selects a week; the pupil is not on the front because, unlike the
-- mirror, nothing here is ever replaced per-pupil.
CREATE INDEX idx_school_lesson_details_window ON school_lesson_details(starts_at);

CREATE TABLE school_lesson_marks (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL,
    -- The column the portal files the mark under: "Поточна", "Тематична".
    kind     TEXT    NOT NULL CHECK (kind <> ''),
    -- Stored as the portal renders it ("9,00"). Nothing computes with it, and
    -- a decimal comma or a non-numeric marker is the school's business.
    value    TEXT    NOT NULL CHECK (value <> '')
);

CREATE INDEX idx_school_lesson_marks_event ON school_lesson_marks(event_id);

CREATE TABLE school_lesson_files (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL,
    -- Which tab it hung on: 'homework' or 'lesson'.
    kind     TEXT    NOT NULL CHECK (kind IN ('homework', 'lesson')),
    -- A link into the school's blob storage. The file itself is not copied;
    -- whether the link still resolves in a year is not our guarantee.
    url      TEXT    NOT NULL CHECK (url <> ''),
    title    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_school_lesson_files_event ON school_lesson_files(event_id);

-- +goose Down
DROP INDEX IF EXISTS idx_school_lesson_files_event;
DROP INDEX IF EXISTS idx_school_lesson_marks_event;
DROP INDEX IF EXISTS idx_school_lesson_details_window;
DROP TABLE IF EXISTS school_lesson_files;
DROP TABLE IF EXISTS school_lesson_marks;
DROP TABLE IF EXISTS school_lesson_details;
