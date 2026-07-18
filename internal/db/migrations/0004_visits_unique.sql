-- +goose Up
-- One visit per enrollment per date is the model's invariant (the bot and
-- the web both assume it); enforce it in the schema. Existing duplicates are
-- the double-tap bug's artifact — keep the earliest row of each pair.
DELETE FROM visits WHERE id NOT IN (
    SELECT MIN(id) FROM visits GROUP BY enrollment_id, date
);
DROP INDEX idx_visits_enrollment_date;
CREATE UNIQUE INDEX idx_visits_enrollment_date ON visits(enrollment_id, date);

-- +goose Down
DROP INDEX idx_visits_enrollment_date;
CREATE INDEX idx_visits_enrollment_date ON visits(enrollment_id, date);
