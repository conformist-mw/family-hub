-- The new school runs on Moodle, and what it offers a parent is grades rather
-- than a timetable. These two tables mirror the child's gradebook so the bot
-- can tell which grades are new since the last sync, answer /grades without a
-- login, and look back over a week for the Friday review.
--
-- Moodle is the source of truth and the only writer: a sync replaces what it
-- saw, and a course that is no longer in the enrolment list (a new school year)
-- is dropped with its grades rather than mixed into the next year's averages.
-- The ids are Moodle's own — the course id and the grade item id — because the
-- grade item id is the one thing that tells "a new grade" from "the same grade
-- seen again".
--
-- A course that is not in school_courses yet is seeded silently: its existing
-- grades are history, not news. That is why the course row exists even before
-- it has any grade.

-- +goose Up

CREATE TABLE school_courses (
    id           INTEGER PRIMARY KEY,                  -- Moodle course id
    name         TEXT    NOT NULL CHECK (name <> ''),  -- without the trailing grade number
    position     INTEGER NOT NULL,                     -- order in Moodle's enrolment list
    average      REAL,                                 -- NULL while the course has no average
    average_text TEXT,                                 -- Moodle's rendering ("5,5")
    synced_at    TEXT    NOT NULL
);

CREATE TABLE school_grades (
    item_id       INTEGER PRIMARY KEY,                 -- Moodle grade item id
    course_id     INTEGER NOT NULL,
    item_module   TEXT,                                -- quiz, lesson, assign...
    item_instance INTEGER,
    name          TEXT    NOT NULL CHECK (name <> ''),
    grade_raw     REAL    NOT NULL,
    grade_text    TEXT    NOT NULL,
    graded_at     TEXT    NOT NULL                     -- YYYY-MM-DDTHH:MM, local
);

CREATE INDEX idx_school_grades_course_id ON school_grades(course_id);

-- +goose Down
DROP INDEX IF EXISTS idx_school_grades_course_id;
DROP TABLE IF EXISTS school_grades;
DROP TABLE IF EXISTS school_courses;
