-- The academic school day, mirrored from the school-today.com pupil portal.
--
-- This is NOT a course the family enrolled in and pays for — it is the child's
-- ordinary weekday timetable (Алгебра, Українська мова, ...), pulled from an
-- external system nobody here edits. So it is deliberately kept out of the
-- enrollments/regular_slots model: those rows drive billing, evening
-- "did the lesson happen?" reminders and payment forecasts, none of which mean
-- anything for a school subject. Mixing the two would put meals and recess into
-- the payment math.
--
-- Unlike reminders, this table keeps no history and has no soft delete. The
-- portal is the source of truth; a sync replaces a whole time window at once
-- (see store.ReplaceSchoolLessons), so a lesson the school cancelled simply
-- stops being written. The calendar is a forward-looking mirror of what the
-- portal says now — what actually happened is the school's record, not ours.
--
-- Identity is event_id, the portal's own stable id for one occurrence. It is
-- what makes a re-sync an upsert rather than a duplicate, and it is unique
-- across the source regardless of which child the lesson belongs to.

-- +goose Up

CREATE TABLE school_lessons (
    id          INTEGER PRIMARY KEY,
    -- The portal's eventID. Stable per occurrence and globally unique in the
    -- source, so it is the natural key a re-sync reconciles on.
    event_id    INTEGER NOT NULL,
    -- Which child. Carried even though there is one today, because the portal
    -- is queried per pupil and a second child must not silently overwrite the
    -- first's window.
    pupil_id    INTEGER NOT NULL,
    subject     TEXT    NOT NULL CHECK (subject <> ''),
    -- Naive local wall-clock, the same layout appointments use (model.LocalDatetime).
    -- The portal reports local times and the container runs in a fixed TZ.
    starts_at   TEXT    NOT NULL CHECK (starts_at <> ''),  -- 2006-01-02T15:04
    ends_at     TEXT    NOT NULL CHECK (ends_at <> ''),    -- 2006-01-02T15:04
    -- The lesson topic, when the teacher has filled it in — usually empty for
    -- future lessons. Kept as-is; the category (lesson / meal / recess) is not
    -- stored but derived from the subject at read time, so re-classifying never
    -- needs a re-sync.
    topic       TEXT    NOT NULL DEFAULT '',
    -- The portal's per-lesson flag that a mark exists. This school records none
    -- today, but the flag is mirrored so a future digest can surface them
    -- without a schema change.
    has_marks   INTEGER NOT NULL DEFAULT 0,
    -- The colour the portal assigns the subject, carried through to the ICS so
    -- a subscribed calendar can keep the school's own palette.
    theme_color TEXT    NOT NULL DEFAULT '',
    synced_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    UNIQUE (event_id)
);

-- The feed and the window-replace both scan by time; the pupil is on the front
-- so a per-child replace hits an index rather than the whole table.
CREATE INDEX idx_school_lessons_window ON school_lessons(pupil_id, starts_at);

-- +goose Down
DROP INDEX IF EXISTS idx_school_lessons_window;
DROP TABLE IF EXISTS school_lessons;
