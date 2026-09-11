-- What the teacher wrote about *this* child at a lesson, as opposed to about
-- the lesson: the portal's "Заохочення" (what they were singled out for) and
-- the "Коментар" beside it.
--
-- These come from the lesson detail page's fourth tab, which the collector
-- read past until now: the other three panes describe the class — one topic,
-- one set of notes, one homework for everybody — while this one is a table
-- with a row per pupil. That is the whole reason they are separate columns
-- rather than more text appended to `notes`. A parent reading the Friday
-- review needs to know which half is about their child, and "Опрацювали
-- вправи 1,2,3" and "молодець на уроці" answer different questions.
--
-- Both default to '' so the rows collected before this migration read as what
-- they are — not collected — rather than as a teacher who wrote nothing. The
-- distinction is not recoverable and not worth a third state: the portal only
-- keeps the current term's lesson pages, so re-collecting an old week to fill
-- these in is not possible either way (see 0010).
--
-- Nothing is added to `school_lessons`. That table is the rolling timetable
-- mirror, wiped whole every sync, and a praise written onto it would live
-- twelve hours.

-- +goose Up
ALTER TABLE school_lesson_details ADD COLUMN praise        TEXT NOT NULL DEFAULT '';
ALTER TABLE school_lesson_details ADD COLUMN pupil_comment TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE school_lesson_details DROP COLUMN pupil_comment;
ALTER TABLE school_lesson_details DROP COLUMN praise;
