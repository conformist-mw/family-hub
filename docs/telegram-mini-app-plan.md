# Telegram Mini App plan

## Goal

Add a Telegram Mini App for the family's frequent, mobile-first workflows
without creating a second backend, a second database, or a second deployment.

The Mini App is not a mobile skin over the web UI. The two have different jobs:

- the web UI is an **overview and administration** surface — dense, wide,
  everything in one place;
- the Mini App is a set of **task-shaped screens** — one thing at a time, held
  in one hand, opened for ten seconds;
- the bot keeps reminders, one-tap callback actions, and free-text capture.

Because the jobs differ, the Mini App gets its own HTTP surface, its own view
layer, and its own screens. Sharing a template layer with the web UI would
keep pulling it back toward "the website, but narrower".

The existing architecture and domain model stay documented in
[`ARCHITECTURE.md`](../ARCHITECTURE.md). This document owns the Mini App
boundary and delivery shape.

## Shape

A JSON API plus a small client-rendered frontend, both inside the existing Go
binary, both under a single `/mini` prefix:

```text
/mini              bootstrap shell (HTML, no family data)
/mini/assets/*     JS, CSS, vendored libraries
/mini/api/*        JSON, authenticated
```

One prefix, not two, so Traefik needs exactly **one** bypass rule. Given that
the parenthesization of compound router rules is already a recorded footgun
(ARCHITECTURE.md), fewer rules is worth more than tidier URLs.

Same process, same `store.Store`, same SQLite file, same image, same
`just deploy-hetzner-tag family-hub`. A separate API does **not** mean a
separate deployment.

### Frontend

Preact + htm, vendored as htm's prebuilt `preact/standalone` bundle — one 13 KB
ES module under `internal/mini/static/vendor/`, with no imports of its own, so
no import map and no bare specifiers. Served from the same origin and embedded
with `//go:embed` exactly like `internal/web` already embeds its templates and
static files.

No build step, no `package.json`, no node stage in the Dockerfile. The project
stays a single Go toolchain. Editing a screen means editing a file and
restarting `go run ./cmd/server`.

This is a deliberate reaction to `~/dev/ceilings-project`, where the same
feature was built with Vite + React + django-vite. Four of the five sources of
pain there had nothing to do with Telegram:

1. two build systems handshaking through a `manifest.json` written into the
   backend's static tree;
2. HTTPS for the Vite dev server (`vite-plugin-mkcert`), because Telegram will
   not open a Mini App over plain http;
3. CORS between the dev server's origin and the backend's;
4. an `initData` → JWT exchange whose token expired after an hour with no
   refresh path;
5. the shell rendered by the backend, with screen parameters passed through
   `data-*` attributes and `window.location.pathname.split('/')[3]`.

Items 1, 3, 4 and 5 disappear entirely with this shape. Item 2 halves: there is
no dev server to certify, and real-Telegram checks go through a tunnel that
terminates HTTPS itself.

## Authentication

**No cookies, no sessions, no server-side state.**

`initData` is Telegram's signed launch payload. It travels as
`Authorization: tma <initData>` on every request to `/mini/api/*` and is
verified per request.

This removes an entire class of problems that a cookie would have introduced.
On Telegram Web and Desktop the Mini App runs inside a cross-site iframe, where
a `SameSite=Lax` cookie is simply not sent; `SameSite=None` would work but
depends on third-party-cookie policy. A header carries no such baggage. It also
means there is no ambient credential, so **CSRF does not apply** to
`/mini/api/*` — the existing `csrfGuard` in `internal/web/router.go` neither
covers nor needs to cover it.

Verification per request:

1. Parse the `Authorization: tma …` header.
2. Recompute the HMAC and compare with `subtle.ConstantTimeCompare`. The
   derived secret — `HMAC-SHA256(key: "WebAppData", msg: bot_token)` — is
   computed **once** when the router is built, not per request.
3. Reject `auth_date` older than the freshness window.
4. Read the Telegram user id and check it against the allowlist.

There is no hash cache. One HMAC over a few hundred bytes is microseconds — it
disappears next to the SQLite query that follows. A cache would add eviction
and invalidation semantics in exchange for nothing.

### Freshness window

Telegram does **not** refresh `initData` while the Mini App stays open, so the
window doubles as the session lifetime. A short window (minutes) means the app
dies in the user's hand; a long one means a captured `initData` is replayable
for that long.

Use 12–24 hours. Over HTTPS, to our own origin, with an allowlist of three
Telegram ids, the allowlist is the real barrier — not signature freshness.
Tightening the window meaningfully would require server-side session state,
which is the cookie we just removed, wearing a different hat.

### Authorization

A valid signature proves the identity came from Telegram. It does not prove the
person belongs to the family.

Add `TELEGRAM_MINI_USERS` — comma-separated Telegram **user** ids. Do not reuse
`TELEGRAM_ALLOWED_CHATS`: that allowlist describes chats, including the family
group, while a Mini App authenticates the individual who opened it.

### Errors

One JSON shape, `{"error": {"code": "...", "message": "..."}}`, with the two
failures kept distinct:

| Code             | Status | Meaning                                  |
| ---------------- | ------ | ---------------------------------------- |
| `bad_init_data`  | 400    | Malformed, tampered, or stale signature  |
| `forbidden`      | 403    | Valid Telegram user, not in the family   |
| `internal`       | 500    | Anything else                            |

The shell reacts differently: 403 is terminal ("доступ заборонено"), 400
suggests reopening the app.

**Never log `initData`.** It carries the user's name and username. Log the
resolved user id and the rejection reason only. `csrfGuard` already logs
`Origin`, so the habit exists — the Mini App middleware must not extend it to
the payload.

### No bot token, no Mini App

`initData` verification is an HMAC keyed by the bot token. When
`TELEGRAM_BOT_TOKEN` is unset the `/mini/*` routes are **not mounted at all**.
This mirrors the existing precedent: without `GEMINI_API_KEY` the free-text
capture handlers are simply never registered.

### Development

Two modes, both needed.

**Everyday loop — `MINI_DEV_USER=<telegram_id>`.** The API accepts requests
without `initData` and treats them as that user. Enabled only when the variable
is set **and** `TELEGRAM_WEBHOOK_URL` is empty. Production always sets the
webhook URL, so this is a structural impossibility rather than a flag someone
can forget to unset. It logs loudly at boot. Crucially it skips **signature
verification only** — the injected id still has to be in `TELEGRAM_MINI_USERS`,
so the worst it can grant is access someone already has.

**Before shipping — a tunnel.** `cloudflared` in front of `localhost:8080` plus
the dev bot's Main Mini App URL. This is the only way to check the real theme,
safe-area insets, native buttons, and the Telegram clients themselves.

## API shape

Endpoints are **screen-shaped, not table-shaped**. There is one consumer and
that is a licence not to build generality: `GET /mini/api/appointments`
serves a screen, and does not pretend to be a resource in a REST hierarchy
that nothing else will ever traverse.

Responses use dedicated DTOs, never `internal/model` structs. The schema
changes for domain reasons; the client must not break when it does.

### Times are formatted server-side

`appointments.starts_at` is a naive local wall clock (`2006-01-02T15:04`) in
`Europe/Kyiv`. If it were sent as an ISO timestamp, the client would parse it
in the **device's** timezone and shift it — and the device is a phone that can
be anywhere.

So the API sends display strings, computed server-side from `time.Local` and
`model.WeekdayLabels`: `"14:30"`, `"Сьогодні, 6 серпня"`. The client never
parses a date. The raw `starts_at` travels only as an opaque identity/sort key.

This makes the API partly presentational. That is an accepted trade: it removes
a whole bug class, and there is one consumer.

### Write path

Reads can go straight from `store` to a DTO. Writes cannot: validation today
lives inside the web handlers (`internal/web/visits.go`, `appointments.go`,
`payments.go`), parsing form values.

With a second entry point into the same writes, **a thin use-case layer above
`store` stops being optional and becomes a precondition of the first write
slice**. Otherwise a rule like "one visit per enrollment per date" gets fixed
in two places, six months apart. Nothing is extracted before it is needed by
two callers — but the first Mini App write is that second caller.

## Timezone

Values stay wall-clock. They are not migrated to UTC, for three reasons:

- **`regular_slots` cannot be represented in UTC at all.** "Every Monday at
  17:00" is 15:00 UTC in winter and 14:00 UTC in summer. No single UTC value
  expresses the rule, and this is equally true of any European timezone.
- **Future appointments are wall-clock intentions, not instants.** The
  orthodontist's chair is in Kyiv at 14:30 on the clinic's clock. Converting at
  write time freezes the offset in force *when the row was written*; a change
  to the DST rules silently moves every booked appointment by an hour.
- **Rendering in the device timezone would be a bug here, not a feature.** A
  parent in Poland must still see 14:30 for a Kyiv appointment.

What *is* missing is that the zone is implicit — it lives in the container's
`TZ` and nowhere in the data. Change `TZ` and every historical row silently
changes meaning.

The fix is to record the zone, not to convert to UTC: add
`tz TEXT NOT NULL DEFAULT 'Europe/Kyiv'` to `appointments` (and later
`regular_slots`), display in each row's own zone, and label the time when that
zone differs from the app's current one.

A column is the mechanism, not a datetime type. Postgres `timestamptz` does not
store a zone despite the name — it converts the input to a UTC instant using
the session `TimeZone`, discards the zone, and renders back on output, which is
the UTC model rejected above. It is 8 bytes, the same as `timestamp`;
`'14:30+03'`, `'12:30+01'` and `'11:30+00'` compare equal; and the offset shown
on output is the reading session's, not a stored one.

The column must hold an IANA name, not an offset. An offset is a snapshot of a
rule on one date — `Europe/Kyiv` is `+03` in August and `+02` in December — so
a stored `+03` is already wrong for a booking six months out, and wrong again
if the DST rules change. Wall clock plus a retained IANA zone has no
native type in Postgres either; the standard pattern there for future events is
`timestamp` plus a text zone column. SQLite has no date/time storage class at
all — only NULL, INTEGER, REAL, TEXT and BLOB — so dates are already a TEXT
convention here and the second column costs nothing extra.

The migration is **additive** — no
value is rewritten, and ICS, the scheduler, digests, cost prompts and the
parser keep running on `time.Local` until each is taught to read the column.

This is a separate small task. The Mini App does not wait for it and renders
each row's zone, which today is always `Europe/Kyiv`.

## Delivery

### Slice 1 — upcoming appointments (read-only)

The most-used view in the family: what is coming up. Today it is the bot's
`/list`, a self-editing message paged one calendar week at a time. A scrolling
list is a better fit for the same information.

Scope:

- foundation — `/mini` shell, `initData` verification, allowlist, dev fixture,
  Traefik bypass;
- `GET /mini/api/appointments` — everything upcoming, grouped by day;
- one screen: sticky day headers, time on the left, title and person, location
  underneath, a status pill only when the status is not `planned`;
- empty state, `403` state.

No new store method is needed: `UpcomingAppointments(from, limit)`
(`internal/store/appointments.go:82`) already returns non-deleted,
non-cancelled rows from a point forward, ascending.

**No pagination.** The realistic horizon is tens of rows — a few kilobytes. A
cursor now is speculative complexity for a dataset that will not grow. One
response, capped at 100 as a fuse. If it ever binds, `UpcomingAppointments`
already has the right signature.

Response shape:

```json
{ "days": [
    { "date": "2026-08-06",
      "label": "Сьогодні, 6 серпня",
      "items": [
        { "id": 42, "time": "14:30", "endTime": "15:30",
          "title": "Ортодонт", "person": "Демид",
          "location": "вул. Хрещатик 1", "note": "", "status": "planned" }
      ] } ] }
```

`date` is a render key, not display text. Cost is omitted — it is a
past-facing field.

Staleness: the list goes out of date when someone edits a visit through the
bot. Read-only, so refetching on `visibilitychange` is enough.

### Later slices

Deliberately not scoped yet. Each earns its place from use, not from symmetry
with the web UI. Likely order: mark a lesson (the highest-frequency write, and
the trigger for the use-case layer), then balances, then appointment edits.

Two questions to answer before the first **write** slice:

- **Group visibility.** Marking a lesson through the bot posts to
  `TELEGRAM_NOTIFY_CHAT` and the whole family sees it. A Mini App write would
  be silent. Decide whether it should post too.
- **`start_param` deep links.** Treat every value as an untrusted routing hint:
  validate its shape, then perform normal authorization and record lookup.

## Telegram integration

Keep the JavaScript thin and confined to the bridge:

- read `Telegram.WebApp.initData` once at boot; use `initDataUnsafe` only for
  optimistic display, never for an authorization decision;
- apply the Telegram theme CSS variables, with browser fallbacks;
- respect safe-area insets;
- call `WebApp.ready()` after the first render, then `expand()`;
- call **`WebApp.disableVerticalSwipes()`** (Bot API 7.7+). Without it the
  client intercepts the vertical swipe to dismiss the app, and a scrolling list
  closes the Mini App instead of scrolling — the first screen would feel broken.

`BackButton` and `MainButton` are not used in slice 1: one screen, read-only.
They arrive with nested navigation and forms.

Language is **Ukrainian only**. The codebase is mid-translation — the lessons
half is still Russian, appointments are Ukrainian (ARCHITECTURE.md backlog) —
and new templates must not carry the Russian strings forward.

## Code organization

```text
internal/mini/
  router.go        routes: /mini, /mini/assets/*, /mini/api/*
  auth.go          initData verification, allowlist, dev fixture
  appointments.go  handler, DTO, day formatting
  static/
    index.html  app.js  style.css
    vendor/preact-htm.module.js
```

`internal/mini` depends on `internal/store` and `internal/model`. It does not
import `internal/bot`; the bot token arrives as a string in the constructor.

Mounting: `cmd/server/main.go` builds an outer mux —
`mux.Handle("/mini/", mini.NewRouter(...))` and
`mux.Handle("/", web.NewRouter(...))`. `/mini/*` therefore bypasses
`csrfGuard`, which is correct, and everything else keeps its current behavior.

Do not call web handlers from Mini App handlers, and do not share templates.
Share pure parsing and validation helpers when duplication actually appears.

## Deployment

- Traefik: one rule, `(Host(a) || Host(b)) && PathPrefix(/mini)` →
  `no-auth-chain@file`. The host alternation stays parenthesized — `||` binds
  looser than `&&`, and an unparenthesized rule exposes the whole app.
- The bypass covers only the `/mini` prefix. Existing web routes keep going
  through `auth-chain@file`; the webhook and ICS routers are untouched.
- Dockerfile unchanged. Image, volume and deploy command unchanged.
- New environment: `TELEGRAM_MINI_USERS`, and `MINI_DEV_USER` locally only.
  Document both in `.env.example`.
- BotFather: register the existing bot as a Main Mini App, giving a launch
  button on the bot profile and a `https://t.me/<bot>?startapp` link to pin in
  the family group. The command menu stays — `/add`, `/visit`, `/week`,
  `/list` remain useful independently.

## Verification

Automated:

- `initData` — valid, malformed, tampered, missing, stale;
- constant-time comparison path;
- allowed and denied Telegram user ids (403, not 400);
- `/mini/*` not mounted when `TELEGRAM_BOT_TOKEN` is unset;
- `MINI_DEV_USER` inert when `TELEGRAM_WEBHOOK_URL` is set, and still subject
  to the allowlist when active;
- route boundary: existing web routes, the webhook and `/calendar.ics` behave
  exactly as before, and `/mini/api/*` does not pass through `csrfGuard`;
- day grouping and labels (today / tomorrow / dated), and the empty list.

Manual, through the tunnel: iOS, Android and Desktop, light and dark themes,
opened both from the bot profile and from a direct link in the family group.
Specifically check that scrolling the list does not dismiss the app.

## Alternatives considered

**Server-rendered `/mini/*` sharing the web templates.** Rejected: the two
surfaces have different jobs, and a shared layout would keep dragging the Mini
App back toward the desktop administration view. Also forces a session cookie,
which is the iframe problem below.

**Cookie session.** Rejected. On Telegram Web and Desktop the Mini App is a
cross-site iframe, where `SameSite=Lax` cookies are not sent; `SameSite=None`
works today but rests on third-party-cookie policy. Verifying `initData` per
request is stateless and has neither failure mode.

**`initData` → JWT exchange.** Rejected. It reintroduces expiry and refresh for
no gain, since `initData` is already a signed bearer credential with its own
lifetime. `~/dev/ceilings-project` does exactly this with a one-hour token and
no refresh path, so the app dies after an hour open.

**Vite + React/Svelte.** Rejected for this project's size — see Frontend above.

**`Telegram.WebApp.sendData`.** Fits a short keyboard-launched form returning
one payload to the bot. It is launch-mode-specific, payload-limited, and closes
the app on submit. Not a transport.

**Opening the existing website unchanged.** Useful only as a disposable
integration check. OAuth inside a Telegram web view is friction, and the fixed
colors and top navigation read as a website placed inside Telegram.

## Acceptance criteria

- An authorized family member opens the Mini App with no OAuth login.
- An unauthorized Telegram user can neither read nor mutate family data, and
  gets 403 rather than 400.
- Bot commands, reminders, webhook handling, ICS and the web UI are unchanged.
- Writes through any surface are immediately visible in the others — one store,
  one database.
- Appointment times display in the appointment's own timezone regardless of the
  phone's.
- No second database, no second service, no node toolchain in the repo.
