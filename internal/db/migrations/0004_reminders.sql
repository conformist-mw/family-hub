-- Recurring reminders: the things that are not lessons and not appointments —
-- "enable cashback on the 1st", "log the car mileage", "water the cactus every
-- other week". They replace what the family kept in iOS Reminders.
--
-- Three tables rather than one, because a repeating chore has three separate
-- facts and conflating them makes history lie.
--
--   reminders            what the chore is
--   reminder_rules       how it repeated, and from when — a LIST of versions
--   reminder_occurrences what actually came due, one row per instance
--
-- Why the rule is versioned. Schedules change: the cashback moves from the 1st
-- to the 5th. With a single mutable rule row, every past occurrence would be
-- recomputed under today's rule, so the record of what was scheduled — and of
-- what was missed — would silently change under you. A version carries the
-- moment it took effect, so any window is expanded with the rule that was
-- actually in force then.
--
-- Why occurrences are stored at all, given a rule can generate them. Because a
-- generated occurrence proves nothing. A row written when the moment arrived is
-- evidence that it did arrive; its absence after a rule edit would be
-- indistinguishable from "it was never scheduled". The split is by instant, not
-- by date: due_at <= now is read from these rows, due_at > now is expanded from
-- the rules, and nothing is ever both.
--
-- Why due_at and not due_date. A full RRULE can put several occurrences on one
-- calendar day (BYHOUR=8,20), so identity is the whole datetime. Keying on the
-- date would silently collapse them into one.

-- +goose Up

CREATE TABLE reminders (
    id            INTEGER PRIMARY KEY,
    title         TEXT    NOT NULL CHECK (title <> ''),  -- what: Кешбек / Пробіг авто / ...
    person        TEXT    NOT NULL DEFAULT '',   -- who, as written; decorative, routes nothing
    duration_min  INTEGER NOT NULL DEFAULT 15,   -- how long the calendar event is
    active        INTEGER NOT NULL DEFAULT 1,
    -- The floor for the catch-up backfill. Without it, switching a paused
    -- reminder back on would invent a month of "you forgot" rows covering the
    -- time it was deliberately off.
    active_since  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M','now','localtime')),
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),
    deleted_at    TEXT                           -- soft delete, like appointments
);

CREATE TABLE reminder_rules (
    id            INTEGER PRIMARY KEY,
    reminder_id   INTEGER NOT NULL REFERENCES reminders(id) ON DELETE CASCADE,
    -- Inclusive, and a datetime rather than a date on purpose: a new rule made
    -- "from today" at 10:00 must not claim today's 08:00 occurrence, which has
    -- already happened under the old one.
    valid_from_at TEXT    NOT NULL CHECK (valid_from_at <> ''),  -- ISO local datetime
    -- DTSTART: fixes both the time of day and the phase of any INTERVAL.
    -- "Every two weeks" says nothing until this decides which week is yours.
    dtstart       TEXT    NOT NULL CHECK (dtstart <> ''),  -- ISO local datetime
    -- An empty rule expands to nothing, which would read as "this chore never
    -- happens" rather than as the broken row it is. The service validates the
    -- body; the schema refuses the empty case outright.
    rrule         TEXT    NOT NULL CHECK (rrule <> ''),  -- RFC 5545 body
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    UNIQUE (reminder_id, valid_from_at)
);

CREATE TABLE reminder_occurrences (
    id          INTEGER PRIMARY KEY,
    reminder_id INTEGER NOT NULL REFERENCES reminders(id) ON DELETE CASCADE,
    -- Which version produced this row, so the record explains itself later.
    rule_id     INTEGER NOT NULL REFERENCES reminder_rules(id),
    due_at      TEXT    NOT NULL,              -- ISO local wall-clock: 2006-01-02T15:04
    -- pending is not a derived state: a pending row in the past is the evidence
    -- that the moment came and nobody closed it. skipped is a deliberate pass.
    status      TEXT    NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','done','skipped')),
    done_at     TEXT    NOT NULL DEFAULT '',
    done_by     TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    -- The same medicine as the visits index from #10: the materialiser, the
    -- Mini App checkbox and the bot's inline button all race for this row.
    UNIQUE (reminder_id, due_at)
);

CREATE INDEX idx_reminder_occ_due   ON reminder_occurrences(due_at, status);
CREATE INDEX idx_reminder_rules_rid ON reminder_rules(reminder_id, valid_from_at);

-- +goose Down
DROP INDEX IF EXISTS idx_reminder_rules_rid;
DROP INDEX IF EXISTS idx_reminder_occ_due;
DROP TABLE IF EXISTS reminder_occurrences;
DROP TABLE IF EXISTS reminder_rules;
DROP TABLE IF EXISTS reminders;
