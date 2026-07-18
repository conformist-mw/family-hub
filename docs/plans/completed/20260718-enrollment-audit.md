# Enrollment Audit Page

## Overview

A read-only reconciliation ("сверка") page per enrollment:
`/enrollments/{id}/audit`. It answers the trainer-audit scenario — "you're
asked to pay for more lessons than actually happened" — by showing a single
chronological ledger of visits (date, status, comment) and payments with a
running balance, over a selectable period (since last payment / this month /
all time / custom range). A period extending into the future adds a forecast:
upcoming lessons expanded from the weekly schedule, greyed out, marked
covered/uncovered by the paid balance, with "paid through DD.MM" and "top up:
N × price = amount". The summary is shareable: copy as plain text
(Telegram/Viber-safe) or send to the family group via the existing bot.

Design approved in a brainstorm session (2026-07-18).

## Context (from discovery)

- Store: `ListVisits(VisitFilter)` / `ListPayments(PaymentFilter)` exist but
  are page-oriented; the ledger needs its own lean queries in a new
  `internal/store/audit.go` (file-per-concern convention).
- `internal/web`: handler file + template per page; routes in
  `router.go` (`NewRouter(db, logger, webhookPath, webhook)` — called once in
  `cmd/server/main.go:97`). Templates embedded, styled via
  `internal/web/static/style.css`; status pills and quick-pick chips already
  exist as patterns. No JS anywhere yet.
- Bot: `internal/bot/bot.go` wraps telebot; `scheduler.go` already sends to
  `cfg.NotifyChat`. No generic "send text to notify chat" method yet.
- `model.Balance` (`internal/model/model.go`) holds per-enrollment rollups;
  `Payment` carries `LessonsPaid` (per-lesson) or `CoversFrom`/`CoversUntil`
  (monthly).
- Prod sits behind oauth2-proxy — outsiders can't open links, hence
  copy-text / screenshot / bot-send instead of shareable URLs.
- No `*_test.go` in the project; owner explicitly chose no automated tests
  (manual verification via running server + dev bot).
- Planned neighbor feature (separate plan, `20260718-trainer-vacations.md`):
  trainer absences should later drop dates from the forecast expansion — keep
  the expansion a single pure function so that's a one-line filter.

## Development Approach

- **Testing approach**: no automated tests (project convention; owner's
  explicit choice). Each task ends with `go build ./...` and a manual check
  against the running server (`go run ./cmd/server`).
- Complete each task fully before moving to the next; follow existing
  patterns (store file per concern, handler + template per page).
- New logic that is pure (ledger merge, forecast, text rendering) lives in
  `internal/audit` — no DB, no HTTP — so it stays trivially testable later.
- **Update this plan file when scope changes during implementation.**

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Solution Overview

- `internal/store/audit.go`: raw data for a period — visits, payments,
  opening balance, last payment date.
- `internal/audit`: pure functions — merge into ledger rows with running
  balance, expand future occurrences from slots with coverage, compute
  summary, render plain text.
- `internal/web/audit.go` + `templates/audit.html`: the page with period
  chips, ledger table, forecast block, copy button, send-to-group form.
- Sharing via a `Notifier` interface implemented by the bot, injected from
  `main.go`; nil → button hidden. Web never imports telebot.

## Technical Details

### Data (store, `internal/store/audit.go`)

- `AuditData(enrollmentID int64, from, to string) (AuditData, error)` — one
  struct holding:
  - visits in `[from, to]` (date, status, comment), ascending;
  - payments in `[from, to]` (date, amount, lessons_paid, covers_from/until,
    comment), ascending;
  - `OpeningBalance int` — per-lesson only: `SUM(lessons_paid)` −
    `COUNT(done)` strictly before `from` (single query, `COALESCE` to 0 —
    `lessons_paid` is NULL on monthly payments);
  - `Slots []model.Slot` — the enrollment's active slots (no per-enrollment
    slots query exists today: `SlotsForWeekday`/`AllActiveSlots` are
    cross-enrollment; `ListSlots(enrollmentID)` returns all incl. inactive —
    filter or add a lean query). The pure forecast function receives this
    slice, it never touches the DB;
  - empty `from` means "all time" (no lower bound, opening balance 0).
- Monthly coverage: the handler additionally calls `Store.BalanceFor(id)`
  and passes `Balance.CoversUntil` into the forecast — the
  `coveragePeriods` machinery in `store.go` is unexported and must not be
  duplicated.
- `LastPaymentDate(enrollmentID int64) (string, error)` — `MAX(date)` from
  payments; empty string when the enrollment has no payments.

### Ledger + forecast (`internal/audit`, pure)

```go
type Row struct {
    Date    string
    Kind    string  // visit | payment | future
    Status  string  // visits
    Amount  float64 // payments
    Lessons int     // payment: +lessons_paid; future/visit: 1
    Comment string
    Balance int     // running per-lesson balance after this row
    Covered bool    // future rows and monthly rows: covered by payment
}
```

- Merge visits + payments by date (payment first on equal dates — money
  arrives before the lesson it pays for); running balance: start at
  `OpeningBalance`, payment `+lessons_paid`, `done` visit `−1`, other
  statuses no-op. Balance meaningful for per-lesson; for monthly the balance
  column is hidden entirely and past visit rows carry date + status pill +
  comment only (no coverage marking on past rows).
- Forecast (only when `to` > today): expand the enrollment's active slots
  (weekday+time) into concrete dates ascending. Expansion starts **today** if
  no visit is recorded for today yet (a pending today's lesson must not
  vanish from the audit), otherwise tomorrow; ends at `to`. Coverage:
  - per-lesson: `max(0, closing balance)` allocated to future dates in
    order; dates beyond it get `Covered=false`;
  - monthly: `Covered = date ≤ CoversUntil` (passed in from `BalanceFor`);
    `CoversUntil == ""` (not covered now) → all future rows uncovered.
  - future hook: when trainer absences land, filter their dates out of the
    expansion here (single spot, pure function).
- Summary struct: counts per status, paid lessons/amount in period, opening →
  closing balance, and for future periods: "paid through" date (last covered
  future date), uncovered count, top-up amount:
  - per-lesson: lessons to square up = `uncovered + max(0, −closing)` — an
    existing debt (negative closing balance, the core audit case) adds its
    magnitude on top of uncovered future lessons; shown split as «долг X +
    вперёд N» when debt > 0; amount = total × `CurrentPrice`;
  - monthly: `M × CurrentPrice` where M = calendar months from
    `CoversUntil` (exclusive) through `to` (inclusive), counted by month
    boundaries, min 0; when `CoversUntil == ""` count months from the first
    forecast date through `to`.

### Text rendering (`internal/audit`)

Plain text, no markdown (survives Telegram and Viber paste): header
"Person · Class", period line, one line per row
(`DD.MM  ✓ проведено — комментарий` / `DD.MM  оплата +8 занятий (3200 ₴)` /
`DD.MM  — по расписанию (не оплачено)`), totals block, forecast block when
present. Status glyphs reuse the bot's set (✓ → ✗ ⤵).

### Web

- `GET /enrollments/{id}/audit?range=last_payment|month|all|custom&from=&to=`
  - default `range=last_payment`: `from` = last payment date (inclusive, the
    payment itself is the first row), `to` = today; **no payments → fall back
    to `all`**;
  - `month`: start of current month → today; `all`: no bounds → today;
    `custom`: both dates required, `from ≤ to` validated, `to` may be in the
    future (this is how the forecast is reached from any preset).
- Page: header (person · class, period in words, status totals, paid
  lessons/amount, opening → closing balance, forecast block when `to` >
  today), period chips (existing quick-pick chip styling), custom range
  reveals two `<input type="date">` + submit (plain GET form, no JS needed).
- Ledger table: no pagination; payments rows background-highlighted, visit
  status pills as on `/visits`, future rows greyed (`.future`), uncovered
  future rows grey-red (`.future.uncovered`). Read-only — no edit links.
- Entry links: dashboard card and `/enrollments` list row → "сверка".
- Flash after send: redirect to same URL + `&sent=1`, the audit template
  shows an "отправлено в группу" banner from its own data flag. The existing
  `pageData.Flash` slot in `base.html` is intentionally not used — `render()`
  never populates it and threading it through is a bigger change than the
  local flag.

### Sharing

- Copy: the rendered text sits in a hidden `<textarea id="audit-text">`;
  button calls `navigator.clipboard.writeText` (fallback:
  `textarea.select()` + `document.execCommand('copy')` for older mobile
  webviews). First JS in the project — small inline `<script>` in
  `audit.html`, no files, no deps.
- Send: `POST /enrollments/{id}/audit/send` (form carries the same range
  params) → handler recomputes the same text → `Notifier.Send(text)` →
  redirect back with `sent=1`.
- `Notifier` interface declared in `internal/web`:
  `type Notifier interface { NotifyText(text string) error }`. Bot gains
  `func (b *Bot) NotifyText(text string) error` — sends to `cfg.NotifyChat`,
  splitting texts over ~4000 chars by lines into sequential messages
  (Telegram hard limit is 4096).
- `NewRouter` gains a `notifier web.Notifier` parameter. **Typed-nil trap**:
  in `main.go` do NOT pass `lessonsBot` directly from the outer scope — a nil
  `*bot.Bot` in an interface is non-nil. Declare `var notifier web.Notifier`
  and assign it only inside the `if token != ""` branch (and only when
  `NotifyChat != 0`). Template hides the send button when nil.

### Screenshot mode

`@media print` hides nav + period switcher + buttons; a "для скрина" toggle
button flips a `screenshot` class on `<body>` with the same hiding rules
(second use of the inline script). Result: clean card — header + ledger.

## What Goes Where

- **Implementation Steps**: store, audit package, web page, sharing, CSS.
- **Post-Completion**: prod deploy, real-chat verification.

## Implementation Steps

### Task 1: Store — audit data queries

**Files:**
- Create: `internal/store/audit.go`

- [x] `AuditData` struct + method: visits and payments for enrollment in
      `[from, to]` ascending; empty `from`/`to` mean unbounded
- [x] opening balance query (per-lesson): `SUM(lessons_paid) − COUNT(done)`
      strictly before `from`, `COALESCE` to 0, skipped when `from` is empty
- [x] active slots of the enrollment in `AuditData.Slots` (new lean query or
      `ListSlots` + active filter) — the forecast's only data source
- [x] `LastPaymentDate(enrollmentID)` — `MAX(date)`, "" when none
- [x] `go build ./...`

### Task 2: internal/audit — ledger merge and summary

**Files:**
- Create: `internal/audit/ledger.go`

- [x] `Row` struct per Technical Details; `BuildLedger(data, enrollment)`
      merges visits+payments by date (payment first on ties), computes
      running balance from `OpeningBalance`
- [x] `Summary` struct: status counts, paid lessons/amount, opening/closing
      balance
- [x] `go build ./...`

### Task 3: internal/audit — forecast

**Files:**
- Create: `internal/audit/forecast.go`

- [x] expand the enrollment's slots (passed in, no DB) into dates from today
      (if today has no visit yet) or tomorrow through `to`, ascending, as
      `Kind=future` rows — one pure function (future hook for trainer-absence
      filtering)
- [x] coverage: per-lesson — allocate `max(0, closing)` in date order;
      monthly — `date ≤ CoversUntil`, all uncovered when `CoversUntil == ""`
- [x] forecast summary: paid-through date, uncovered count, top-up
      (per-lesson: `(uncovered + max(0, −closing)) × CurrentPrice`, debt shown
      separately; monthly: months from `CoversUntil` exclusive — or from the
      first forecast date when "" — through `to` inclusive × `CurrentPrice`)
- [x] `go build ./...`

### Task 4: internal/audit — plain-text rendering

**Files:**
- Create: `internal/audit/text.go`

- [x] `RenderText(header, rows, summary, forecast)` → plain text per
      Technical Details (no markdown; ✓ → ✗ ⤵ glyphs; dates as DD.MM)
- [x] `SplitMessage(text string, limit int) []string` — split by lines at
      ~4000 chars (lives here so the bot stays dumb)
- [x] `go build ./...`

### Task 5: Web — audit page

**Files:**
- Create: `internal/web/audit.go`
- Create: `internal/web/templates/audit.html`
- Modify: `internal/web/render.go`
- Modify: `internal/web/router.go`
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/templates/enrollments.html`
- Modify: `internal/web/static/style.css`

- [x] register `"audit.html"` in the hard-coded `pages` slice in
      `parseTemplates()` (`render.go`) — without this the page 500s
- [x] range parsing per Technical Details (`last_payment` default with
      fallback to `all` when no payments; `custom` validates `from ≤ to`)
- [x] handler: store (`AuditData` + `BalanceFor` for monthly `CoversUntil`) →
      audit package → template data (header, chips state, rows, summary,
      forecast, rendered text, `sent` flag)
- [x] `audit.html`: header block, period chips + custom date form (plain GET),
      ledger table, forecast block, hidden textarea with the text version
- [x] route `GET /enrollments/{id}/audit`; "сверка" links on dashboard card
      and `/enrollments` rows
- [x] CSS: payment-row highlight, `.future`, `.future.uncovered`, forecast
      block — reuse existing pill/chip styles
- [x] manual check: all four presets on an enrollment with real data; custom
      range into next month shows grey rows and top-up math for both billing
      types; enrollment without payments falls back to `all`

### Task 6: Sharing — copy button and send-to-group

**Files:**
- Modify: `internal/web/audit.go`
- Modify: `internal/web/templates/audit.html`
- Modify: `internal/web/router.go`
- Modify: `internal/bot/bot.go`
- Modify: `cmd/server/main.go`

- [x] inline `<script>`: clipboard copy with textarea-select fallback;
      "для скрина" toggle flipping `screenshot` class; `@media print` +
      `.screenshot` CSS hiding nav/chips/buttons
- [x] `Notifier` interface in `internal/web`; `NewRouter` gains the param;
      send button rendered only when non-nil
- [x] `bot.NotifyText(text)` — sends to `cfg.NotifyChat` using
      `audit.SplitMessage` chunks
- [x] `main.go`: `var notifier web.Notifier`, assigned only inside the bot
      branch when `NotifyChat != 0` (typed-nil trap — see Technical Details)
- [x] `POST /enrollments/{id}/audit/send` → recompute text → send → redirect
      with `sent=1`; template shows the flash banner
- [x] manual check with dev bot: copy pastes cleanly into Telegram; send
      lands in the notify chat; long "all time" ledger arrives split; with
      `TELEGRAM_BOT_TOKEN` unset the button is absent and the page works

### Task 7: Verify acceptance criteria

- [x] the audit scenario end-to-end: "trainer asks to pay for 8, ledger since
      last payment shows what actually happened" — numbers reconcile with
      dashboard balance for the same enrollment
- [x] forecast: per-lesson enrollment with 2 paid lessons and a period ending
      next month shows exactly 2 covered future rows, the rest grey-red, and
      correct top-up; monthly shows pass boundary and months × price
- [x] debt case: enrollment with negative closing balance (more done than
      paid) — top-up includes the debt («долг X + вперёд N») and no future
      row is marked covered; monthly with no active pass — all future rows
      uncovered, months counted from the first forecast date
- [x] screenshot mode leaves only header + ledger; print preview matches
- [x] edge cases: enrollment with zero visits and zero payments; custom range
      with `from > to` shows validation error, not a 500
- [x] `go build ./...` and `go vet ./...` clean

### Task 8: [Final] Update documentation

- [x] ARCHITECTURE.md: add the audit page to the routes list, describe
      `internal/audit` in the repository layout, note the Notifier seam
      between web and bot
- [x] move this plan to `docs/plans/completed/`

## Post-Completion

**Deploy** (manual, from `~/dev/dotfiles`): build/push image, then
`just deploy-hetzner-tag lessons`.

**Manual verification in prod:**
- send-to-group posts into the real family chat; forward/copy to the trainer
  from Telegram and Viber and confirm the text renders legibly in both.
- phone screenshot of the screenshot-mode page is readable and fits one screen
  for a typical "since last payment" period.

**Coordination with the trainer-vacations plan:**
- whichever lands second wires absences into the forecast expansion (drop
  absence dates) — a one-line filter in `internal/audit/forecast.go`, noted
  there as the hook.

**Explicitly out of scope** (deferred by design):
- server-rendered PNG, bot `/audit` command (the text renderer is shared and
  ready for it), chat selection, editing from the ledger.


➕ Implemented after trainer-vacations landed: the forecast expansion drops dates inside the trainer's absences (`absentOn` in `internal/audit/forecast.go`).
⚠️ Send-to-group needs prod verification: dev env has no TELEGRAM_NOTIFY_CHAT, so only the nil-Notifier path (hidden button, 503 on direct POST) was exercised locally.
