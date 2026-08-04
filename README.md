# family-hub

A small self-hosted app for the family's schedule and money, replacing a
fragile Excel spreadsheet and a second bot. Two domains live in it:

- **Lessons** — recurring extracurricular courses: attendance journal,
  payments, and how many prepaid lessons are left or when a monthly pass runs
  out. Evening reminders and a per-course reconciliation view.
- **Appointments** — one-off visits (orthodontist, manicure, doctors). Captured
  from plain text in Telegram via Gemini ("завтра 11:30 ортодонт Демід" → a
  confirmation card → saved), editable on the web.

One web UI, one Telegram bot in the family group, one SQLite file, one ICS feed
that Home Assistant polls for both kinds of events.

## Why

The original Excel had a few sharp edges this app removes:

- **Price changes** are first-class — bump a course price and historical
  payments stay intact (each payment records its own amount).
- **Monthly passes** are supported alongside per-lesson billing, with correct
  coverage detection (gaps between passes are not silently treated as "paid").
- **Fast entry** — a mobile-friendly form with quick "today / yesterday"
  date buttons instead of fighting a spreadsheet on a phone, or just a message
  to the bot.

## Domain model

- **persons** — anyone attending (kids or adults).
- **enrollments** — a course a person attends. Owns the class `name`,
  a `description` to distinguish look-alikes (e.g. two "Gymnastics" with
  different trainers), `billing_type` (`per_lesson` | `monthly`), current
  price and a low-balance threshold. Identity is the row id, not the name.
- **regular_slots** — weekly schedule of a course; drives reminders and the
  recurring ICS events.
- **visits** — one attendance event: date + status
  (`done` / `rescheduled` / `cancelled` / `skipped`).
- **payments** — money in: either N prepaid lessons, or a date range for a
  monthly pass.
- **trainers** + **trainer_absences** — who teaches a course and their
  date-range absences, which mute reminders and punch holes in the feed.
- **appointments** — one-off events: title, who (free text), location, start
  (and optional end), status (`planned` / `done` / `cancelled`), note, plus
  the raw message the parse came from.

The balance dashboard rolls the lesson side up: prepaid − attended for
per-lesson courses, and "is today covered by a paid period" for monthly passes.

## Stack

- Go (`net/http`, `html/template`)
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- `pressly/goose` migrations (embedded)
- `gopkg.in/telebot.v3` for the Telegram bot
- `google.golang.org/genai` for free-text parsing (Gemini)
- `xuri/excelize` for the one-shot Excel import

## Running locally

```sh
go run ./cmd/server          # serves on :8080, db at data/lessons.db
go run ./cmd/import          # (re)seed the db from a local Excel file
```

Flags: `-addr` (listen address), `-db` (SQLite path). Configuration is env
only — copy `.env.example` to `.env` and fill in what you need. Without
`GEMINI_API_KEY` everything works except free-text capture.

## Deployment

Built on the dev machine, pushed to Docker Hub, pulled on the server — no CI:

```sh
docker build -t olegsmedyuk/family-hub:latest .
docker push olegsmedyuk/family-hub:latest
# then, from the dotfiles repo:
just deploy-hetzner-tag family-hub
```

Runs as a container behind Traefik with oauth2-proxy auth, except the bot
webhook and the ICS feed, which bypass it (secret path / token).

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the data model, deployment
pipeline (SOPS + Ansible + Docker Hub), Telegram bot internals, common
operational recipes, and the open backlog.
