# lessons

A small self-hosted web app for tracking kids' (and adults') extracurricular
lessons — replacing a fragile Excel spreadsheet. It keeps a journal of
attendance and payments, and shows, per course, how many prepaid lessons are
left or when a monthly pass runs out.

## Why

The original Excel had a few sharp edges this app removes:

- **Price changes** are first-class — bump a course price and historical
  payments stay intact (each payment records its own amount).
- **Monthly passes** are supported alongside per-lesson billing, with correct
  coverage detection (gaps between passes are not silently treated as "paid").
- **Fast entry** — a mobile-friendly form with quick "today / yesterday"
  date buttons instead of fighting a spreadsheet on a phone.

## Domain model

- **persons** — anyone attending (kids or adults).
- **enrollments** — a course a person attends. Owns the class `name`,
  a `description` to distinguish look-alikes (e.g. two "Gymnastics" with
  different trainers), `billing_type` (`per_lesson` | `monthly`), current
  price and a low-balance threshold. Identity is the row id, not the name.
- **regular_slots** — weekly schedule of a course (for future reminders).
- **visits** — one attendance event: date + status
  (`done` / `rescheduled` / `cancelled` / `skipped`).
- **payments** — money in: either N prepaid lessons, or a date range for a
  monthly pass.

The balance dashboard rolls these up: prepaid − attended for per-lesson
courses, and "is today covered by a paid period" for monthly passes.

## Stack

- Go (`net/http`, `html/template`)
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- `pressly/goose` migrations (embedded)
- `xuri/excelize` for the one-shot Excel import

## Running locally

```sh
go run ./cmd/server          # serves on :8080, db at data/lessons.db
go run ./cmd/import          # (re)seed the db from the bundled Excel file
```

Flags: `-addr` (listen address), `-db` (SQLite path).

## Deployment

Built on the dev machine, pushed to Docker Hub, pulled on the server — no CI:

```sh
docker build -t olegsmedyuk/lessons:latest .
docker push olegsmedyuk/lessons:latest
# then, from the dotfiles repo:
just deploy-hetzner-tag lessons
```

Runs as a container behind Traefik with oauth2-proxy auth. The image bundles
both the server and the importer plus the seed spreadsheet, so the production
database is seeded on first deploy and is the source of truth afterwards.

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the data model, deployment
pipeline (SOPS + Ansible + Docker Hub), Telegram bot internals, common
operational recipes, and the open backlog.
