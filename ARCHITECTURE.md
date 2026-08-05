# Architecture

Reference for how this codebase is organized, what it deploys to, and where
to look when picking it back up after a break.

## Stack

- Go (`net/http`, `html/template`)
- SQLite via `modernc.org/sqlite` (pure Go, no CGO) — the binary stays
  statically linkable into `distroless/static`
- `pressly/goose` migrations, embedded into the binary
- `xuri/excelize/v2` for the one-shot Excel import
- `gopkg.in/telebot.v3` for the Telegram bot
- `google.golang.org/genai` (Gemini) to parse appointments out of free text
- `joho/godotenv` to load `.env` in local development

## Domain model

- **`persons`** — anyone attending (kids or adults). `kind = child|adult`.
- **`enrollments`** — one course one person attends. Owns `name`,
  `description`, `billing_type` (`per_lesson` | `monthly`), `current_price`,
  `low_threshold`, `active`. Identity is the row `id`; the `name` is not
  unique by design — two "Гимнастика" for different people coexist,
  distinguished by `description`.
- **`regular_slots`** — weekly schedule of a course (weekday + time). Used
  by the bot to drive evening reminders.
- **`visits`** — one attendance event: `date`, `status`
  (`done` / `rescheduled` / `cancelled` / `skipped`), `comment`. One visit
  per enrollment per date, enforced by a UNIQUE index.
- **`payments`** — money in. Either prepaid lessons (`lessons_paid`) or a
  monthly pass (`covers_from` + `covers_until`).
- **`trainers`** + **`trainer_absences`** — who teaches a course
  (`enrollments.trainer_id`, nullable) and their date-range absences
  (vacation / sick / other, both bounds inclusive). While an absence covers
  today, the bot stays silent for that trainer's enrollments and the ICS
  feed drops their occurrences — the enrollment itself stays active. A
  trainer is created on the fly from the enrollment form's free-text field;
  absences are managed on `/trainers`.
- **`appointments`** — one-off events, the second domain of the app
  (orthodontist, manicure, doctors). `title`, `person` (free text, not a FK to
  `persons`: it can be a guest or "обоє"), `location`, `starts_at` and optional
  `ends_at` as naive local `2006-01-02T15:04`, `status`
  (`planned` / `done` / `cancelled`), `note`, and `raw` — the message the parse
  came from. `cost` is optional money — NULL means nobody wrote it down, `0`
  means it was free — and `cost_prompt_msg_id` is the notify-chat message that
  asked for it (see Bot). `ha_uid`/`ha_synced_at` are an unused outbox for a future push
  exporter; `deleted_at` is a soft delete, so the row survives long enough for
  such an exporter to issue a calendar delete. Nothing links appointments to
  enrollments — they are deliberately independent.

The schema reflects deliberate choices after a model review (see
`review/model_review.md`): no separate `activities` dictionary, no
`offerings` hierarchy, no `pricing_terms`. Each payment row keeps its own
amount, so spend reports are exact even when the unit price changed
mid-month. Price changes are reflected by editing
`enrollment.current_price`; historical rows stay as they were.

## Repository layout

```
cmd/
  server/      # web + bot process
  import/      # one-shot Excel importer
internal/
  db/          # sql.Open + embedded goose migrations
  model/       # plain structs and constants
  store/       # repository layer (one file per concern)
  audit/       # pure reconciliation logic: ledger, forecast, text rendering
  web/         # http handlers, templates, static
  bot/         # telebot.v3 wrapper, command handlers, scheduler, callbacks
  parse/       # Gemini client: free text -> appointments (and a bare datetime)
  ics/         # the single VCALENDAR feed HA polls
  importer/    # Excel reader used by cmd/import
data/          # local SQLite (gitignored)
Доп. занятия.xlsx  # the original source; gitignored+dockerignored (personal data), local only
```

## Web

- `cmd/server/main.go` boots: loads `.env`, opens DB, runs migrations,
  optionally creates a Bot, registers the HTTP mux, starts the server.
- Routes (see `internal/web/router.go`):
  - `/` — balance dashboard
  - `/visits`, `/visits/new`, `/visits/{id}/edit`, …
  - `/appointments`, `/appointments/new`, `/appointments/{id}/edit`,
    `POST /appointments/{id}/delete` (soft) — the hands-on side of what the bot
    captures: the list puts upcoming first and dims the past, the form owns
    location/note/status/cost which free-text capture never sets — including
    filling in an amount long after the visit
  - `/payments`, `/payments/new`, …
  - `/enrollments`, `/enrollments/{id}/edit` (price, threshold, schedule,
    trainer)
  - `/enrollments/{id}/audit` — reconciliation ("сверка") for one course:
    a visits+payments ledger with a running balance over a period (since
    last payment / this month / all time / custom), a forecast of upcoming
    lessons (grey; unpaid ones flagged with the top-up amount; trainer
    absences excluded), copy-as-text, and "send to group" via the bot
    (`POST .../audit/send`). Pure logic lives in `internal/audit`; the bot
    is reached through the small `web.Notifier` interface, so `internal/web`
    never imports telebot and the button hides when the bot is off.
  - `/trainers` — trainers with their absences; add/delete an absence
  - `/stats` — totals (month/year/all time) and CSS bar charts by month,
    by person, by course
  - `/calendar.ics` — one feed for HA's Remote Calendar: weekly RRULE events
    per lesson slot (with EXDATE holes for trainer absences), all-day absence
    events, and one VEVENT per appointment (from 30 days back, non-cancelled;
    UIDs are `slot-N` / `absence-N` / `appointment-N`, all suffixed `@lessons`
    — HA keys on the uid, so the suffix must not change). Token-guarded via
    `ICS_TOKEN`.
  - `/static/…`, `/healthz`
- Templates and static assets are embedded into the binary
  (`//go:embed`), so the image carries everything except the SQLite file.
- Mobile-friendly: native HTML5 `<input type="date">`, quick-pick chips
  for frequent courses and recent comments, status pills, responsive CSS.
- Auth in production is provided by `oauth2-proxy` via the Traefik
  middleware `auth-chain@file`; the app itself has no login.

## Importer

- `cmd/import` is a separate binary. Reads the local Excel (`-src`),
  fixes a known typo status (`отмненео` → `cancelled`) and a mis-keyed
  date, and writes into the same SQLite file.
- Local re-import: `go run ./cmd/import` — wipes `visits` + `payments`,
  upserts `enrollments` based on the `current` sheet.
- Production seed: none — the image carries no spreadsheet (personal
  data). The prod database is the source of truth; a fresh install
  starts empty or is seeded once by hand (see DEPLOY.md).

## Bot

- `internal/bot` wraps `telebot.v3`.
- Two transport modes:
  - **Long polling** when `TELEGRAM_WEBHOOK_URL` is empty (local dev).
  - **Webhook** when `TELEGRAM_WEBHOOK_URL` is set (production).
- Webhook path is a **secret** held in SOPS (`family_hub_webhook_path`),
  composed into both `TELEGRAM_WEBHOOK_URL` and the Traefik
  `PathPrefix` of the bypass router. Telegram's
  `X-Telegram-Bot-Api-Secret-Token` header is the second layer.
- Allowlist via `TELEGRAM_ALLOWED_CHATS` (comma-separated chat IDs).
  Unknown chats receive a short "доступ запрещён".
- Commands: `/start`, `/help`, `/balance`, `/stats`, `/add` (lessons) and
  `/visit`, `/week`, `/list` (appointments). The "/" menu is set on startup.
- `/add` is an inline three-step flow (course chips → date chips →
  status buttons). The state is encoded into callback data; each tap
  edits the same message to advance.
- A non-done status (reminder or `/add`) adds a **reason step**: chips
  from `FrequentComments` (same source as the web form) plus "Другое".
  The visit is created before the step, so abandoning it loses only the
  comment; "Другое" leaves the comment empty for later editing in the
  web UI. The chosen reason is saved as the visit comment and shown in
  the final message ("… · отменено · заболел").
- **Appointment capture** is the free-text path: text → Gemini
  (`internal/parse`) → confirmation card → save. It only runs in private chats
  (`onText` bails out in groups, where every message is other people's
  chatter), so **`/visit <text>` is the only capture path in the family
  group** — do not remove it. A same-time collision offers "update the existing
  one" instead of silently adding a second row. Pending cards
  (`internal/bot/pending.go`) and armed field edits
  (`internal/bot/awaiting.go`) are in-memory with eviction: a restart drops
  them, the user just re-sends.
- `/list` is one self-editing message: a calendar week at a time, tap a number
  for the card → edit / cancel, all state encoded in the callback data. Text
  edits (reschedule, rename, change who) are private-chat only, because in a
  group the "next message" could be anyone's. `✕ Закрити` ends the interaction
  by stripping the keyboard while keeping the week readable — without it every
  `/list` ever sent keeps offering live buttons.
- Without `GEMINI_API_KEY` the parser is nil: `/visit` and free-text capture
  are not registered, everything else (including `/week`, `/list`, cancel and
  the lessons half) works.
- **Cost prompts** (`internal/bot/costprompts.go`): `APPOINTMENT_COST_PROMPT_MIN`
  minutes (default `60`, `<0` disables) after an appointment starts, the bot
  asks in the notify chat what it cost. The amount arrives as a **reply** to
  that message — deliberately not the button-armed "your next message is the
  value" flow the field edits use, because that one is private-chat only (in a
  group the next message can be anyone's), while a reply names its target and
  is delivered to bots even with group privacy mode on. The prompt→appointment
  link lives in `appointments.cost_prompt_msg_id`, not in memory: deploys are
  frequent and the prompt message outlives the process. That column doubles as
  the already-asked flag, so a restart cannot ask twice. The sweep only reaches
  24h back (`costPromptLookback`) — without that bound, the first tick after
  the feature shipped would have asked about every appointment in the history.
  `✗ Без суми` closes the prompt leaving `cost` NULL. Capture fills `cost`
  straight from the text when a price is stated («педикюр 800»); the parser is
  strict about it (anything that is not a plain number counts as no price)
  because a wrong amount is worse than an absent one, and an absent one just
  means the prompt asks later.
- Three independent tickers run as separate goroutines: `RunScheduler` for
  lesson reminders (below), `RunDigests` (`internal/bot/digests.go`) for the
  appointment daily/weekly digests, gated by `NOTIFICATIONS_ENABLED` (off in
  prod — HA owns those summaries), and `RunCostPrompts` for the above. All
  three need a configured notify chat.
- The **scheduler** (`internal/bot/scheduler.go`) is a once-a-minute
  ticker. `TELEGRAM_REMINDER_DELAY_MIN` minutes (default `60`, container
  TZ is `Europe/Kyiv`) after each active `regular_slot` matching today's
  weekday it sends a message to `TELEGRAM_NOTIFY_CHAT`, with the same
  four inline buttons. One reminder per enrollment per day; visits
  already recorded for today are skipped. Sent-state is in-memory, so a
  mid-day restart re-sends still-unanswered reminders. Enrollments whose
  trainer has an absence covering today are filtered out in SQL
  (`SlotsForWeekday`), which silences both the reminder and the
  empty-balance warning for the whole absence.
- The scheduler also sends a buttonless **empty-balance warning**
  `TELEGRAM_PRELESSON_LEAD_MIN` minutes (default `120`, `<0` disables)
  before a slot when nothing paid covers the lesson: zero/negative
  remaining (per-lesson) or no active pass (monthly). Only inside the
  `[slot−lead, slot)` window — never after the lesson has started — and
  once per enrollment per day.
- After a lesson is marked via inline buttons (reminder or `/add`), the
  final message carries a one-line balance: 🟢/🟡/🔴 per
  `Balance.State()`, "Осталось оплаченных: X из Y" for per-lesson
  (Y is the most recent pack size, not the all-time total),
  "Абонемент до …, осталось N дн." for monthly.

## Deployment

- Image built on the dev machine (Mac arm64), pushed to Docker Hub
  `olegsmedyuk/family-hub:latest`. Hetzner is arm64 too, so no cross-build.
- Ansible role `roles/family-hub` lives in `~/dev/dotfiles`, included from
  `hetzner.yml`. Deploy with:

  ```sh
  cd ~/dev/dotfiles
  just deploy-hetzner-tag family-hub
  ```

  Use `just`, not raw `ansible-playbook` — `just` loads the `.env` that
  carries the BWS access token used by other roles.
- Container runs as `1000:1000`, mounts `~/server_data/lessons` for the
  SQLite file (still `lessons.db` — the path predates the rename and changing
  it buys nothing).
- Three Traefik routers on the same host, which answers to both
  `family.conformist.name` and (transitionally) `lessons.conformist.name`:
  - the app router → `auth-chain@file` middleware (oauth2-proxy in front).
  - the bot router — host plus `PathPrefix(<webhook path>)` →
    `no-auth-chain@file`, so Telegram can POST without going through
    oauth. The path is loaded from SOPS, so the repo never reveals it.
  - the ICS router — host plus `PathPrefix(/calendar.ics)` →
    `no-auth-chain@file`, guarded by `ICS_TOKEN` instead, so HA can poll.
    On the compound routers the host alternation must stay parenthesized:
    `(Host(a) || Host(b)) && PathPrefix(...)` — `||` binds looser than `&&`,
    and an unparenthesized rule would expose the whole app without auth.
- No GitHub Actions. Master on GitHub is upstream history; deploy is a
  local Ansible run.

## Secrets

- SOPS + age. Private key at
  `~/Library/Application Support/sops/age/keys.txt` (macOS default; sops
  auto-detects). Public recipient lives in `dotfiles/.sops.yaml`.
- Encrypted file: `dotfiles/roles/family-hub/vars/secrets.sops.yaml`.
  Loaded into the play by `community.sops.load_vars` at the top of the
  role.
- Keys currently used:
  - `family_hub_bot_token`
  - `family_hub_webhook_path`
  - `family_hub_webhook_secret`
  - `family_hub_allowed_chats`
  - `family_hub_notify_chat`
  - `family_hub_gemini_api_key`
  - `family_hub_reminder_hour` (optional override)
- Edit a value: `cd ~/dev/dotfiles && sops edit roles/family-hub/vars/secrets.sops.yaml`.
- Rotate without echoing: `sops --set '["key"] "value"' …`.
- The rest of the dotfiles still uses Bitwarden Secrets Manager. Full
  migration is planned together with the k3s move
  (`dotfiles/MIGRATION.md`).

## Local development

- `.env` at repo root (gitignored); `.env.example` documents every variable.
  The minimum is:

  ```
  TELEGRAM_BOT_TOKEN=<dev bot token>
  GEMINI_API_KEY=<key>          # only needed to test free-text capture
  ```

- `go run ./cmd/server` — boots web on `:8080` plus a polling bot.
- Reset to a clean Excel-derived DB: `rm data/lessons.db && go run ./cmd/import`.
- The dev bot is a different Telegram bot than prod, so messaging it
  doesn't touch the production app.

## Common operations

- **Add a course or person**: web `/enrollments/new`. The person input
  is a datalist that accepts free text — a new person is created on
  submit if needed.
- **Change a price**: web `/enrollments/{id}/edit`, save. Historical
  payments untouched.
- **Archive a course**: same edit screen, uncheck Active. It disappears
  from the dashboard but stays on the courses list and keeps history.
- **Manage schedule**: same screen — add weekday/time slots; the bot
  scheduler reads from there.
- **Re-seed prod from Excel** (destructive, rarely): copy the local
  Excel to the VPS and run the bundled importer against the data volume
  by hand (command in DEPLOY.md); the image and the role no longer ship
  or run the seed.
- **Rotate a secret**: `sops --set` (or `sops edit`), then
  `just deploy-hetzner-tag family-hub`. The bot re-registers the webhook
  on startup, so a path rotation propagates to Telegram automatically.

## Backlog

Not yet built; will be picked off as the project is used.

- **Ukrainian translation.** The lesson half of the UI and the bot is still
  Russian; the appointment pages are Ukrainian. Lands as its own PR right
  after the merge — pure strings, status codes and DB values stay English.
- **Reminders.** "Записати щось", "передати щось", one-offs without a time.
  Not appointments: half of them have no start at all, so they get their own
  table and bot flow rather than a stretched `appointments`.
- **Bot — morning low-balance check.** A second daily push: courses with
  `remaining <= low_threshold` get listed in `TELEGRAM_NOTIFY_CHAT`
  around 08:00 Kyiv. Avoids "we ran out of paid lessons" surprises.
- **Bot — weekly summary.** Sunday evening: "за неделю прошло N
  занятий, потрачено M ₴", broken down by person.
- **Bot — edit / delete a visit.** Today only the web supports it.
- **Bot — `/add` doesn't capture a reason** (the comment field is left
  empty). Could add reason chips at a fourth step.
- **Scheduler is in-memory.** A container restart after the reminder
  hour re-fires the day's reminders. Acceptable in practice; if it
  bites, persist `last_sent_date` in SQLite.
- **Web UX polish.** Many small items the owner has been jotting down;
  expected to land as GitHub issues over time.
- **SOPS migration of the other roles.** Move homepage / segments /
  pihole / traefik / commeilfaut off Bitwarden Secrets Manager. No
  rush — planned with the k3s move.
- **SQLite backup.** No scheduled backup yet. The file lives only on
  the VPS volume.
- ~~**Pauses vs archive.**~~ Solved by trainer absences: a date-range
  absence mutes reminders while the course stays active. A course-level
  pause independent of the trainer hasn't been needed so far.
