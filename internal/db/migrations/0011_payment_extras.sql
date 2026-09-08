-- A course sometimes asks for money that buys neither lessons nor a month:
-- a kimono for karate, a kit for football, a fee for a grading, a trip to a
-- competition. Until now such a payment had nowhere to go. Recording it as a
-- pack of lessons is worse than not recording it at all — it lies in the
-- balance, so the dashboard credits lessons nobody bought, the bot stops
-- asking for the money that is actually due, and the forecast lays the
-- phantom lessons out over future dates.
--
-- So payments gets a third kind of row: `kind = 'extra'`, named by a
-- free-text `label`. It is visible in the payment list, in the statistics and
-- in the audit ledger, and deliberately invisible to the balance.
--
-- Nothing here enforces the shape of an extra (empty lessons_paid, empty
-- coverage, non-empty label). That invariant lives in payments.Form.Parse,
-- the single door both surfaces write through — it is more than a list of
-- allowed values, and SQLite cannot drop a CHECK without rebuilding the table.
--
-- `label` is its own column rather than a reuse of `comment`, because they are
-- two different facts: the label is what the payment IS and it shows in every
-- list, while the comment is a note beside it ("Olya transferred it, she has
-- the receipt") that no table renders.
--
-- DEFAULT 'course' is what carries the existing rows over without an UPDATE.
-- It is NOT what keeps new writes honest: once a query names the column —
-- and store.CreatePayment does — the default no longer applies, and a Kind
-- left empty in Go would be written as ''. payments.Form.Parse sets it on
-- both paths for exactly that reason.

-- +goose Up
ALTER TABLE payments ADD COLUMN kind  TEXT NOT NULL DEFAULT 'course';
ALTER TABLE payments ADD COLUMN label TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE payments DROP COLUMN label;
ALTER TABLE payments DROP COLUMN kind;
