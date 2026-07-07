-- +goose Up
-- Duration lets the calendar feed render a slot as a timed event (start+end),
-- not just a point in time. Existing slots default to 60 minutes.
ALTER TABLE regular_slots ADD COLUMN duration_min INTEGER NOT NULL DEFAULT 60;

-- +goose Down
ALTER TABLE regular_slots DROP COLUMN duration_min;
