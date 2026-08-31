-- Utilities: the household bills that used to live in a separate app
-- (home-meters). Electricity, gas, water, security, internet — for two
-- properties, with meter readings entered once a month and a running record of
-- what was paid.
--
-- It moves here because the two apps had already grown into each other: both
-- posted to the same family group, and the "enter the readings" nudge existed
-- twice — once as a scheduled message there, once as a chore here. A chore
-- knows things a scheduled message cannot (whether it was actually done, what
-- was missed, what belongs in the calendar feed), so the chore wins and the
-- scheduler does not come along.
--
-- Four tables plus a delivery log:
--
--   addresses           a property you pay bills for
--   tariffs             a price list, shared between properties
--   utilities           one billed service at one property
--   readings            one month of one service
--   utility_deliveries  which event message already went out
--
-- Why `addresses` is not about appointments. An address here is a property —
-- "Дім", "Тьоща" — the thing a bill belongs to. It has nothing to do with
-- appointments.location, which is where you go to see a dentist.
--
-- Why `utilities` and not `services`. A row is "Електрика в Домі": one service
-- at one property. In an app that also has trainers and courses, the bare word
-- "service" names nothing.
--
-- Why tariffs are global rather than per-service. Confirmed against the data:
-- the same gas tariff is used by both properties. The calculation method is a
-- property of the tariff, not of the service that uses it.
--
-- Why a reading points at a tariff. It is the tariff that applied THEN. Change
-- the price today and every past month must keep the number it was actually
-- billed at — the same reason reminder_rules is a list of versions rather than
-- one mutable row. Recomputing history from today's rule is how a record starts
-- lying.
--
-- Note on created_at. Rows carried over from the old app keep its format
-- (`datetime('now')`, UTC, space-separated); new rows get this app's
-- (ISO, localtime). The column is informational and nothing parses it, so
-- rewriting 400 historical values to make them match would be churn for
-- appearance.

-- +goose Up

CREATE TABLE addresses (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL CHECK (name <> ''),   -- "Дім", "Тьоща"
    comment     TEXT    NOT NULL DEFAULT '',           -- the street address, one line
    area        REAL,                                   -- m², for per-area tariffs
    currency    TEXT    NOT NULL DEFAULT 'UAH',
    active      INTEGER NOT NULL DEFAULT 1,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);

CREATE TABLE tariffs (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL CHECK (name <> ''),
    -- meter:       consumption × rate1
    -- meter_zoned: two-zone meter, zone 1 × rate1 + zone 2 × rate2
    -- flat:        a fixed monthly sum in rate1, no meter at all
    kind            TEXT    NOT NULL CHECK (kind IN ('meter','meter_zoned','flat')),
    unit            TEXT,                               -- 'м3', 'кВт', NULL for flat
    rate1           REAL    NOT NULL,
    rate2           REAL,                               -- zone 2, meter_zoned only
    effective_from  TEXT,                               -- informational, may be NULL
    effective_to    TEXT,
    active          INTEGER NOT NULL DEFAULT 1,
    comment         TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);

CREATE TABLE utilities (
    id                 INTEGER PRIMARY KEY,
    address_id         INTEGER NOT NULL REFERENCES addresses(id) ON DELETE RESTRICT,
    name               TEXT    NOT NULL CHECK (name <> ''),  -- "Електрика", "Газ", "Охорона"
    -- The tariff to use for the NEXT reading. A stored reading keeps its own.
    current_tariff_id  INTEGER REFERENCES tariffs(id) ON DELETE RESTRICT,
    icon               TEXT    NOT NULL DEFAULT '',   -- emoji: ⚡ 🔥 💧 🛡️ 🌐 🚚
    color              TEXT    NOT NULL DEFAULT '',   -- #hex, the accent in lists and the report
    active             INTEGER NOT NULL DEFAULT 1,
    sort_order         INTEGER NOT NULL DEFAULT 0,
    comment            TEXT    NOT NULL DEFAULT '',
    url                TEXT    NOT NULL DEFAULT '',   -- the provider's site, for paying
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);

CREATE TABLE readings (
    id            INTEGER PRIMARY KEY,
    utility_id    INTEGER NOT NULL REFERENCES utilities(id) ON DELETE RESTRICT,
    -- The tariff that applied to THIS month. Never re-read after the write.
    tariff_id     INTEGER NOT NULL REFERENCES tariffs(id) ON DELETE RESTRICT,
    period        TEXT    NOT NULL,          -- 'YYYY-MM'
    reading_date  TEXT,                      -- when the meter was actually read
    prev1         REAL,
    curr1         REAL,
    prev2         REAL,                      -- zone 2, meter_zoned only
    curr2         REAL,
    consumed1     REAL,
    consumed2     REAL,
    amount        REAL    NOT NULL,
    paid_date     TEXT,                      -- NULL means not paid yet
    comment       TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    -- Three columns, not two, and deliberately so: the month a meter is
    -- replaced carries TWO readings — the final one on the old meter and
    -- tariff, and the first one on the new. Keying on (utility, period) alone
    -- would make that month unrecordable. The data carried over from the old
    -- app contains three such months.
    UNIQUE (utility_id, period, tariff_id)
);

-- Which event message already went out, so a re-check does not repeat it.
-- Only event-driven types live here ('all_readings_entered', 'all_paid_summary');
-- the old app's scheduled reminders are chores now and record themselves in
-- reminder_occurrences.
CREATE TABLE utility_deliveries (
    id          INTEGER PRIMARY KEY,
    type        TEXT    NOT NULL,
    address_id  INTEGER NOT NULL REFERENCES addresses(id) ON DELETE RESTRICT,
    period      TEXT    NOT NULL,
    sent_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime')),

    UNIQUE (type, address_id, period)
);

CREATE INDEX idx_readings_period    ON readings(period);
CREATE INDEX idx_readings_utility   ON readings(utility_id, period DESC);
CREATE INDEX idx_readings_unpaid    ON readings(paid_date) WHERE paid_date IS NULL;
CREATE INDEX idx_utilities_address  ON utilities(address_id, sort_order);

-- +goose Down
DROP INDEX IF EXISTS idx_utilities_address;
DROP INDEX IF EXISTS idx_readings_unpaid;
DROP INDEX IF EXISTS idx_readings_utility;
DROP INDEX IF EXISTS idx_readings_period;
DROP TABLE IF EXISTS utility_deliveries;
DROP TABLE IF EXISTS readings;
DROP TABLE IF EXISTS utilities;
DROP TABLE IF EXISTS tariffs;
DROP TABLE IF EXISTS addresses;
