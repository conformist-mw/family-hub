-- How long before a payment is due to warn about it moves out of the env and
-- onto the course, where the other reminder settings already live.
--
-- One column in minutes, entered on the form as a number plus a unit, because
-- the two kinds of course measure the wait differently: a per-lesson course
-- wants a couple of hours before the lesson it cannot pay for, a monthly one
-- wants a few days before the pass runs out. Same question, same field,
-- different anchor — which the billing type already decides.
--
-- It also takes over the yellow badge for monthly courses, so `low_threshold`
-- goes back to meaning one thing: how many lessons are left. It stays the
-- badge threshold for per-lesson courses and is unused for monthly ones.

-- +goose Up
ALTER TABLE enrollments ADD COLUMN payment_notice_min INTEGER NOT NULL DEFAULT 120;

-- Carry over what monthly courses already had. low_threshold was days there,
-- and dropping it silently would move the school's reminder from five days
-- ahead back to two hours.
UPDATE enrollments
SET payment_notice_min = low_threshold * 1440
WHERE billing_type = 'monthly' AND low_threshold > 0;

-- +goose Down
ALTER TABLE enrollments DROP COLUMN payment_notice_min;
