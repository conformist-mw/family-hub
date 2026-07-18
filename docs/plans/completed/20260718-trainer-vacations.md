# Trainer Vacations

## Overview

Add trainers as an entity and trainer absences (vacation / sick / other) as date
ranges. While an absence covers today, the bot stays silent for that trainer's
enrollments — no "было?" reminder and no empty-balance warning — while the
enrollment itself stays active (dashboard, balance, payments unchanged). The
ICS feed drops lesson occurrences inside the absence and shows one all-day
event per absence instead. No visits are auto-created for absence days —
silence is the point of the feature.

Design approved in a brainstorm session (2026-07-18).

## Context (from discovery)

- Go web app + Telegram bot, SQLite via `modernc.org/sqlite`, goose migrations
  embedded from `internal/db/migrations` (latest after PR #16: `0004_visits_unique.sql`).
- Reminders: `internal/bot/scheduler.go` ticks per minute and iterates
  `Store.SlotsForWeekday(weekday)` (`internal/store/slots.go`) — both the
  post-slot reminder and the pre-slot empty-balance warning walk that list, so
  filtering in that one query silences both.
- ICS: `internal/ics/ics.go` renders one weekly RRULE VEVENT per active slot;
  it already carries a comment anticipating vacations via EXDATE. Handler:
  `internal/web/calendar.go`, data from `Store.AllActiveSlots()`.
- Web: handlers per concern in `internal/web/*.go`, templates in
  `internal/web/templates/`, routes in `router.go`, nav in `base.html`. The
  enrollment form's person field is a free-text datalist with
  find-or-create on submit — reuse that pattern for the trainer field.
- No `*_test.go` files exist in the project; the owner chose to keep it that
  way for this plan (manual verification via the running server + dev bot).

## Development Approach

- **Testing approach**: no automated tests (project convention; owner's
  explicit choice for this plan). Each task ends with `go build ./...` and a
  manual check against the locally running server (`go run ./cmd/server`).
- Complete each task fully before moving to the next.
- Make small, focused changes; follow existing patterns (store file per
  concern, handler file per concern, template per page).
- Backward compatible: `enrollments.trainer_id` is nullable; enrollments
  without a trainer behave exactly as before.
- **Update this plan file when scope changes during implementation.**

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Solution Overview

- New tables `trainers` and `trainer_absences`; nullable
  `enrollments.trainer_id`. No data migration — trainers get attached
  gradually via the enrollment form.
- Reminder muting lives in SQL: `SlotsForWeekday` gains a `date` parameter and
  a `NOT EXISTS` subquery against `trainer_absences`. `scheduler.go` logic
  stays untouched apart from passing today's date.
- ICS gains EXDATE lines (occurrences inside absences, from today forward)
  plus one all-day VEVENT per absence of an active trainer.
- Web: a `/trainers` page (trainers + their absences, add/delete absence),
  a trainer datalist on the enrollment form, and a 🏖 badge on the dashboard
  for enrollments whose trainer is absent today.

## Technical Details

### Schema (migration `0005_trainers.sql`)

```sql
CREATE TABLE trainers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    notes      TEXT NOT NULL DEFAULT '',
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE trainer_absences (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    trainer_id INTEGER NOT NULL REFERENCES trainers(id),
    date_from  TEXT NOT NULL,  -- YYYY-MM-DD, inclusive
    date_to    TEXT NOT NULL,  -- YYYY-MM-DD, inclusive
    kind       TEXT NOT NULL DEFAULT 'vacation',  -- vacation | sick | other
    comment    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

ALTER TABLE enrollments ADD COLUMN trainer_id INTEGER REFERENCES trainers(id);
```

`AUTOINCREMENT` + `created_at` match every existing table's style. The `Down`
section must be fully symmetric: `ALTER TABLE enrollments DROP COLUMN
trainer_id` then drop both tables — otherwise down→up fails with "duplicate
column" and the leftover column dangles an FK at a dropped table
(`foreign_keys=1` is on). `0003_slot_duration.sql` already uses `DROP COLUMN`
the same way.

Both dates inclusive (matches `BETWEEN`). No overlap validation between
absences of one trainer — overlapping ranges are harmless (both mute).

### Model constants

`AbsenceVacation = "vacation"`, `AbsenceSick = "sick"`, `AbsenceOther =
"other"`; label map after the `StatusLabels` pattern: «отпуск», «болезнь»,
«другое». ICS summary prefixes: «Отпуск», «Болезнь», «Отсутствие».

### Scheduler muting

```sql
AND NOT EXISTS (
    SELECT 1 FROM trainer_absences a
    WHERE a.trainer_id = e.trainer_id
      AND ? BETWEEN a.date_from AND a.date_to
)
```

`e.trainer_id IS NULL` never matches the subquery → never muted.

### ICS

- EXDATE values must exactly equal the datetimes the RRULE generates, which
  expand weekly from the VEVENT's DTSTART at a fixed UTC instant. Compute them
  as `DTSTART.UTC() + n*7*24h` (n = 0, 1, 2, …), keeping those that fall
  inside `[date_from, date_to]` and are ≥ today. Do NOT re-derive local
  `HH:MM` → UTC per date: across a DST transition that shifts the UTC clock
  time by an hour and the EXDATE silently stops matching the recurrence.
- Absence event: `UID:absence-<id>@lessons`, `DTSTART;VALUE=DATE:<from>`,
  `DTEND;VALUE=DATE:<to + 1 day>` (DTEND exclusive per RFC 5545),
  `SUMMARY:<kind label>: <trainer name>`. Only absences of active trainers,
  only those not fully in the past.

## What Goes Where

- **Implementation Steps**: schema, model, store, scheduler wiring, ICS, web.
- **Post-Completion**: prod deploy, calendar/bot verification in prod.

## Implementation Steps

### Task 1: Schema migration and model types

**Files:**
- Create: `internal/db/migrations/0005_trainers.sql`
- Modify: `internal/model/model.go`

- [x] write migration `0005_trainers.sql` (tables + `ALTER TABLE enrollments`)
      with a fully symmetric goose `Down`: `ALTER TABLE enrollments DROP COLUMN
      trainer_id`, then `DROP TABLE trainer_absences`, `DROP TABLE trainers`
- [x] add `Trainer{ID, Name, Notes, Active}` and
      `TrainerAbsence{ID, TrainerID, DateFrom, DateTo, Kind, Comment}` to `model.go`
- [x] add absence kind constants and `AbsenceKindLabels` map (после `StatusLabels`)
- [x] add `TrainerID *int64` and `Trainer string` (joined name, read-only) to `model.Enrollment`
- [x] `go build ./...`; run server once — goose applies 0004, tables exist
      (`sqlite3 data/lessons.db '.schema trainers'`)

### Task 2: Trainer store

**Files:**
- Create: `internal/store/trainers.go`

- [x] `ListTrainers() ([]model.Trainer, error)` — active first, then name
- [x] `FindOrCreateTrainer(name string) (int64, error)` — mirror
      `getOrCreatePerson` (`internal/store/enrollment_write.go`) exactly:
      TrimSpace + exact-match lookup (case-sensitive), insert if missing
- [x] `ListAllAbsences()` — absences joined with trainer name, ordered by
      `date_from` desc (per-trainer `ListAbsences` skipped: the page groups
      the single list in the handler — YAGNI)
- [x] `CreateAbsence(trainerID int64, from, to, kind, comment string) error` —
      validate `from <= to`, kind in the allowed set
- [x] `DeleteAbsence(id int64) error`
- [x] `ActiveAbsenceByEnrollment(date string) (map[int64]model.TrainerAbsence, error)` —
      enrollment_id → covering absence for the given date (for the dashboard badge)
- [x] `go build ./...`

### Task 3: Enrollment CRUD carries trainer_id

**Files:**
- Modify: `internal/store/enrollments.go`
- Modify: `internal/store/enrollment_write.go`

- [x] `GetEnrollment` / `ListEnrollments` select `trainer_id` and joined
      trainer name into the new `Enrollment` fields (LEFT JOIN trainers)
- [x] `CreateEnrollment` / `UpdateEnrollment` accept `trainerID *int64`
- [x] fix the only callers — `internal/web/enrollments.go` (create at ~L99,
      update at ~L148) — pass `nil` for now; the form wiring lands in Task 6.
      The importer inserts enrollments via raw SQL and needs no change
      (`trainer_id` defaults to NULL)
- [x] `go build ./...`

### Task 4: Mute scheduler during absences

**Files:**
- Modify: `internal/store/slots.go`
- Modify: `internal/bot/scheduler.go`

- [x] `SlotsForWeekday(weekday int, date string)` — add the `NOT EXISTS`
      subquery from Technical Details
- [x] `sendDueReminders` passes `today` to `SlotsForWeekday`
- [x] manual check with dev bot: create an absence covering today for a
      trainer attached to a test enrollment → no reminder and no empty-balance
      warning fire; remove the absence → reminder fires again

### Task 5: ICS — EXDATE and all-day absence events

**Files:**
- Modify: `internal/ics/ics.go`
- Modify: `internal/store/slots.go` (or `trainers.go`)
- Modify: `internal/web/calendar.go`

- [x] store: expose upcoming absences with trainer names + which enrollments
      each trainer covers (extend `AllActiveSlots` result or add a method)
- [x] `Render` gains absences: per slot, emit `EXDATE:` for occurrences inside
      each covering absence, computed as `DTSTART + n*7d` in UTC (see
      Technical Details — never re-derive local time per date), from today forward
- [x] `Render` emits one all-day VEVENT per absence (UID/DTSTART/DTEND/SUMMARY
      per Technical Details), skipping absences fully in the past
- [x] update the stale "future support" comment at the top of `ics.go`
- [x] manual check: `curl localhost:8080/calendar.ics` — EXDATE lines present,
      absence VEVENT well-formed; sanity-check by importing into a calendar app

### Task 6: Web — /trainers page and enrollment form field

**Files:**
- Create: `internal/web/trainers.go`
- Create: `internal/web/templates/trainers.html`
- Modify: `internal/web/router.go`
- Modify: `internal/web/templates/base.html`
- Modify: `internal/web/enrollments.go`
- Modify: `internal/web/templates/enrollment_form.html`

- [x] routes: `GET /trainers`, `POST /trainers/{id}/absences`,
      `POST /trainers/{id}/absences/{absenceId}/delete`
- [x] `trainers.html`: trainer list with absences (period · kind label ·
      comment · delete button); add-absence form per trainer (two
      `<input type="date">`, kind `<select>`, comment); past absences dimmed
      at the bottom; nav link in `base.html`
- [x] enrollment form: trainer field as free-text datalist (options from
      `ListTrainers`), shown on both new and edit; empty = no trainer
- [x] `handleEnrollmentCreate` / `handleEnrollmentUpdate`: non-empty trainer
      name → `FindOrCreateTrainer` → pass id; empty → `nil`
- [x] manual check in browser: create trainer via enrollment form, add/delete
      absence on /trainers, reassign and clear a trainer on an enrollment

### Task 7: Dashboard absence badge

**Files:**
- Modify: `internal/web/router.go` (handleDashboard)
- Modify: `internal/web/templates/dashboard.html`

- [x] `handleDashboard` fetches `ActiveAbsenceByEnrollment(today)`; the
      template data changes from a bare `[]Balance` to a struct
      `{Balances, Absences}` — update `dashboard.html` to range over
      `.Balances`
- [x] badge on affected rows: «🏖 до DD.MM» (kind label in `title` attr)
- [x] manual check: absence covering today shows the badge; ended absence doesn't

### Task 8: Verify acceptance criteria

- [x] enrollment with absent trainer: bot silent (both message kinds), lesson
      hidden in ICS, all-day event present, badge on dashboard, enrollment
      still active with working balance/payments
- [x] enrollment without trainer: behavior identical to before the change
- [x] absence ends: reminders resume the next day without restarts
- [x] fresh DB path: `rm data/lessons.db && go run ./cmd/import` then start —
      migrations apply cleanly on both fresh and existing DBs
- [x] `go build ./...` and `go vet ./...` clean

### Task 9: [Final] Update documentation

- [x] ARCHITECTURE.md: add trainers/absences to the domain model section,
      describe scheduler muting + ICS behavior, update the routes list, drop
      the now-answered "Pauses vs archive" backlog wording (point it at
      trainer absences)
- [x] move this plan to `docs/plans/completed/`

## Post-Completion

**Deploy** (manual, from `~/dev/dotfiles`):
- build/push image, then `just deploy-hetzner-tag lessons`; goose migrates the
  prod DB on container start.

**Manual verification in prod:**
- Home Assistant Remote Calendar picks up EXDATE + the absence event after its
  next poll.
- During a real absence, confirm the notify chat stays silent for the
  affected enrollments.

**Explicitly out of scope** (deferred by design):
- absence editing (delete + recreate instead), bot commands for absences,
  extending monthly passes by absence length, per-trainer page, visit ↔
  trainer-reason attribution (the `kind` column is the hook for it later).
