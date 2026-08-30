-- Who wrote the row, stored rather than inferred.
--
-- Until now the author existed only in the group notification's byline: the
-- Mini App verified it from initData, passed it to the write layer to render
-- "🆕 Новий візит (Оксана)", and dropped it. A row that says person = "Я" is
-- therefore unattributable — the only copy of the author is a chat message.
--
-- `person` answers "who is this visit for" and stays free text on purpose:
-- it is often somebody who never opens the app. created_by answers "who
-- entered it", which is a different question and one the server already knew
-- the answer to.
--
-- Existing rows keep '': the author was never stored, so there is nothing to
-- backfill them from, and inventing one would be worse than admitting the gap.

-- +goose Up
ALTER TABLE appointments ADD COLUMN created_by TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE appointments DROP COLUMN created_by;
