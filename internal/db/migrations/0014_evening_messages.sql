-- The evening check of each day: where it was posted, so that an answer given
-- in the Mini App — several dishes for one meal, which the check's keyboard
-- cannot take — can redraw it in the group the way a tap would.
--
-- `away` holds the meals answered «Не вдома», comma separated. That answer
-- writes no meal row, so until the message had to be redrawn from the server
-- the only record of it was the message's own keyboard, which the Bot API
-- does not let the server read back.

-- +goose Up

CREATE TABLE evening_messages (
    date       TEXT PRIMARY KEY,           -- YYYY-MM-DD, local
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    away       TEXT NOT NULL DEFAULT ''    -- "lunch", "dinner" or "lunch,dinner"
);

-- +goose Down
DROP TABLE IF EXISTS evening_messages;
