-- A school charges a fixed sum per calendar month and does not expect a daily
-- "was there a lesson?" answer. Both needs are additive to the existing
-- `monthly` scheme: a payment already carries the transfer date and the range
-- it covers, which is all a fixed monthly fee needs.
--
-- No new billing scheme and no billing-period table: the term is the
-- enrollment. A new school year is a new course with its own price, details
-- and schedule; the old one goes to active = 0 and keeps its history.

-- +goose Up
-- attendance_mode gates reminders only, never money — a monthly club may want
-- an attendance journal while a school does not.
ALTER TABLE enrollments ADD COLUMN attendance_mode TEXT NOT NULL
    DEFAULT 'per_session'
    CHECK (attendance_mode IN ('per_session','exceptions_only'));

-- Free-form: payee, IBAN, reference. Goes into the payment reminder so nobody
-- has to dig it out of a chat. Never rendered into the ICS feed.
ALTER TABLE enrollments ADD COLUMN payment_instructions TEXT NOT NULL DEFAULT '';

-- One reminder per coverage end, so a restart cannot repeat it. Keyed by the
-- period's last day rather than by a date sent: that is the thing being warned
-- about, and it stays stable while the warning window is open.
CREATE TABLE billing_reminders (
    enrollment_id INTEGER NOT NULL REFERENCES enrollments(id) ON DELETE CASCADE,
    covers_until  TEXT NOT NULL,  -- YYYY-MM-DD, inclusive
    sent_at       TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (enrollment_id, covers_until)
);

-- +goose Down
DROP TABLE IF EXISTS billing_reminders;
ALTER TABLE enrollments DROP COLUMN payment_instructions;
ALTER TABLE enrollments DROP COLUMN attendance_mode;
