-- utility_deliveries existed to stop an automatic message repeating itself.
-- There are no automatic messages any more, so there is nothing to remember.
--
-- The old app posted two of them per address per month: "everything is
-- entered" and "everything is paid". The first said what a closed chore
-- already says — #5 «Записать данные счётчиков» carries its own reminders, the
-- evening nag, and the record of whether it was actually done, which is why
-- the old scheduler did not move across with the data. The second had to be
-- re-checked from two places (writing a reading, toggling paid) and a
-- forgotten second call meant it would never fire at all.
--
-- Both are replaced by one button on the month view. Pressing it is the
-- decision, and pressing it twice is a decision too — a delivery log would
-- only be there to argue with the person pressing.
--
-- The sixteen rows carried over from home-meters go with the table. They
-- record that a message was sent on a date, by a mechanism that no longer
-- exists, about months that are settled. Keeping a table nothing writes is how
-- the next reader loses an afternoon working out who fills it in.

-- +goose Up
DROP TABLE utility_deliveries;

-- +goose Down
-- Recreated empty. The rows are not restorable and were not worth restoring:
-- nothing reads this table, so an empty one is as useful as the original.
CREATE TABLE utility_deliveries (
    id          INTEGER PRIMARY KEY,
    type        TEXT    NOT NULL,
    address_id  INTEGER NOT NULL REFERENCES addresses(id) ON DELETE RESTRICT,
    period      TEXT    NOT NULL,
    sent_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    UNIQUE (type, address_id, period)
);
