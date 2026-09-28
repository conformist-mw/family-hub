-- The menu: what the family cooks, what was planned for a meal and what was
-- actually eaten, and which morning menu message is on screen. It replaces the
-- Mealie meal plan, whose "cooked" history was mostly Mealie marking its own
-- plan as eaten every evening — a rotation built on it rotated over fiction.
--
-- Three tables:
--
--   dishes         something that is put on the table, as the family names it
--   meals          one dish at one meal of one day, planned or eaten
--   menu_messages  the morning menu of a day and everything it has shown
--
-- A dish is what is served, not a component. "Пюре зі скумбрією" is one dish;
-- there is no separate side dish, because a lone "Гречка" offered as lunch is
-- noise, and the combinations a family actually eats are few.
--
-- Why name_key and not a unique index on lower(name). SQLite's lower() only
-- folds ASCII, so "Борщ" and "борщ" would be two dishes. The key (collapsed
-- whitespace, lower case, one apostrophe) is computed in Go, where Unicode
-- case folding works.
--
-- Why `status` on dishes. New dishes the model suggests live in the same table
-- as `proposed` until they are either eaten (and become `active`) or turned
-- down (`rejected`, kept so the model is told not to suggest them again).
--
-- Why plan and fact share one row in `meals`. A morning tap writes `planned`;
-- the evening answer or a photo of the plate turns it into `eaten`, replaces
-- it, or deletes it. History reads only `eaten`, but rotation reads both —
-- otherwise yesterday's unanswered borscht would come back tomorrow as "not
-- had in a while". `leftover` marks a meal that finishes yesterday's pot;
-- there is no leftover flag on the dish, because "leftovers" is simply what
-- was eaten yesterday.
--
-- meals_once is the double-write guard: the same dish at the same meal of the
-- same day is one row, whichever of the tap, the evening check and the photo
-- got there first. It leads with dish_id, so it does nothing for the reads
-- that start from a day — the evening check, the plate card, a morning tap —
-- which is what meals_day is for.
--
-- Why menu_messages keeps `shown`. A dish that is offered every morning and
-- never picked would otherwise never age and sit at the top of the rotation
-- forever. What was shown yesterday is not offered today, so the set has to
-- outlive the message. The rows currently on screen are read back from the
-- message's own keyboard, not from here.

-- +goose Up

CREATE TABLE dishes (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,              -- in Ukrainian, as the family says it
    name_key   TEXT NOT NULL,              -- normalised name, computed in Go
    meal       TEXT NOT NULL DEFAULT 'any' CHECK (meal IN ('lunch','dinner','any')),
    days       TEXT NOT NULL DEFAULT 'any' CHECK (days IN ('any','weekend')),
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','proposed','rejected')),
    note       TEXT NOT NULL DEFAULT '',   -- the model's one-line pitch for a proposed dish
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);
CREATE UNIQUE INDEX dishes_name ON dishes(name_key);

CREATE TABLE meals (
    id         INTEGER PRIMARY KEY,
    dish_id    INTEGER NOT NULL REFERENCES dishes(id),
    date       TEXT NOT NULL,              -- YYYY-MM-DD, local date
    meal       TEXT NOT NULL CHECK (meal IN ('lunch','dinner')),
    who        TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL CHECK (status IN ('planned','eaten')),
    leftover   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);
CREATE UNIQUE INDEX meals_once ON meals(dish_id, date, meal);
CREATE INDEX meals_day ON meals(date, meal);

CREATE TABLE menu_messages (
    date       TEXT PRIMARY KEY,           -- one morning menu per day
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    shown      TEXT NOT NULL DEFAULT '{}'  -- JSON {"lunch":[ids], "dinner":[ids]}: everything shown that day
);

-- +goose Down
DROP INDEX IF EXISTS meals_day;
DROP INDEX IF EXISTS meals_once;
DROP TABLE IF EXISTS meals;
DROP TABLE IF EXISTS menu_messages;
DROP INDEX IF EXISTS dishes_name;
DROP TABLE IF EXISTS dishes;
