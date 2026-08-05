-- +goose Up
-- What a one-off visit cost. NULL means "not recorded", which is deliberately
-- different from 0 ("it was free") — plenty of appointments never have a price.
ALTER TABLE appointments ADD COLUMN cost REAL;

-- Message id of the bot's "how much was it?" prompt in the notify chat. Two
-- jobs in one column: it routes a reply back to this appointment (a reply
-- carries the id of the message it answers), and its presence means the prompt
-- was already sent, so a restart cannot ask twice. Persisted rather than kept
-- in memory precisely because deploys are frequent and the prompt message
-- outlives the process.
ALTER TABLE appointments ADD COLUMN cost_prompt_msg_id INTEGER;

CREATE INDEX idx_appointments_cost_prompt ON appointments (cost_prompt_msg_id);

-- +goose Down
DROP INDEX idx_appointments_cost_prompt;
ALTER TABLE appointments DROP COLUMN cost_prompt_msg_id;
ALTER TABLE appointments DROP COLUMN cost;
