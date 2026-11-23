-- The chronicle: things done and when, written down so they are not forgotten —
-- the generator refuelled at 151.7 engine hours, the filter changed, the tyres
-- swapped. Until now these lived in Telegram's Saved Messages, where they could
-- be found only by scrolling.
--
-- An entry is a date and free text, nothing more structured: what is worth
-- recording varies too much for columns, and the reading is done by a person.
-- Tags are what groups entries for looking back ("генератор"), kept in their
-- own table so a filter is an index lookup rather than a LIKE over a list.
-- They are stored already normalised (lower case, no '#'), see model.ParseTags.

-- +goose Up

CREATE TABLE log_entries (
    id         INTEGER PRIMARY KEY,
    date       TEXT NOT NULL CHECK (date <> ''),   -- YYYY-MM-DD, local
    text       TEXT NOT NULL CHECK (text <> ''),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);

CREATE INDEX idx_log_entries_date ON log_entries(date);

CREATE TABLE log_entry_tags (
    entry_id INTEGER NOT NULL REFERENCES log_entries(id) ON DELETE CASCADE,
    tag      TEXT    NOT NULL CHECK (tag <> ''),
    PRIMARY KEY (entry_id, tag)
);

CREATE INDEX idx_log_entry_tags_tag ON log_entry_tags(tag);

-- +goose Down
DROP INDEX IF EXISTS idx_log_entry_tags_tag;
DROP TABLE IF EXISTS log_entry_tags;
DROP INDEX IF EXISTS idx_log_entries_date;
DROP TABLE IF EXISTS log_entries;
