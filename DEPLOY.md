# Deploy runbook

How to ship a code change to production. The flow is: **build image → push to
Docker Hub → deploy via dotfiles Ansible role**. Data (courses, visits,
payments, appointments) lives in the prod SQLite DB and is *not* shipped this
way — it is entered through the web UI or the bot on prod.

## Prerequisites (one-time)

- Docker running locally. The dev Mac is **arm64** and Hetzner is **arm64**,
  so a plain `docker build` produces a compatible image — no `buildx`/QEMU
  cross-build needed.
- Logged in to Docker Hub: `docker login` (user `olegsmedyuk`). If a push
  fails with `denied: requested access`, run `! docker login` in the session.
- `~/dev/dotfiles` checked out, with the `.env` that carries the BWS/SOPS
  access token used by the Ansible roles. Always deploy with `just`, never raw
  `ansible-playbook`, so that `.env` is loaded.

## Steps

```sh
# 1. Build the image (run from this repo root). Tag matches
#    roles/family-hub/defaults/main.yml -> family_hub_image.
docker build -t olegsmedyuk/family-hub:latest .

# 2. Push to Docker Hub.
docker push olegsmedyuk/family-hub:latest

# 3. Deploy. The role pulls :latest and recreates the container.
cd ~/dev/dotfiles
just deploy-hetzner-tag family-hub
```

## Notes

- The database is the source of truth and redeploys never touch it. The
  spreadsheet the app was originally seeded from is gone, and so is the
  importer that read it: it ran once, years of data have been entered through
  the UI since, and a tool that can only rebuild the beginning is a tool that
  can only lose the rest.
- Migrations (`goose`) run automatically on container start.
- The prod bot is **@family_core_hub_bot**; its token and everything else the
  container reads are `family_hub_*` keys in
  `host_vars/hetzner/secrets.sops.yaml`. The role has no `vars/` file —
  secrets belong to a host — and none of those keys has a default, so a
  missing one fails the play instead of starting a bot with an empty token.
- The Mini App needs three things beyond a normal deploy, all one-time:
  `family_hub_mini_users` (Telegram **user** ids, not chat ids), the
  `family-hub-mini` Traefik router in the role, and the Mini App URL
  `https://family.conformist.name/mini/` registered on the bot in
  BotFather. Miss the allowlist and everyone gets 403; miss the router and
  the app hits the oauth wall.
- A new bot also needs privacy mode **disabled** (`/setprivacy`) or it will
  not see plain messages in the family group — free-text capture and the
  replies to the "how much was it?" prompt both arrive that way.
- Prod env (bot token, webhook secret/path, `TELEGRAM_NOTIFY_CHAT`,
  `TELEGRAM_REMINDER_DELAY_MIN`, `GEMINI_API_KEY`, `VISIT_PEOPLE`,
  `SCHOOL_DIGEST_TIME=19:30`, `TZ=Europe/Kyiv`) comes from the role +
  `roles/family-hub/vars/secrets.sops.yaml`. Reminders only fire on prod
  because `TELEGRAM_NOTIFY_CHAT` is unset locally.
- The appointment digests stay off in prod (`NOTIFICATIONS_ENABLED` unset):
  Home Assistant sends those summaries from the ICS feed.
- Recurring reminders record what came due through their own ticker, which
  takes no configuration and does not depend on `NOTIFICATIONS_ENABLED` or on
  a notify chat — that record is data, not a message, and putting it behind a
  notification flag would mean switching the digests off silently stopped the
  history. A chore is announced in the group the minute it comes due, which
  takes no configuration either — a chore that does not tell you when it is
  time is not a reminder. Only the evening "still not done" nag is
  configurable, via `REMINDER_NAG_TIME` (unset = no nag; the due-time message
  and the record both continue).

  The due-time push reaches back at most ten minutes, so a restart after a long
  outage announces nothing: the materialiser backfills up to 30 days on boot,
  and without that clamp the whole backlog would arrive as one message. What it
  skips is not lost — the evening nag still reports what the day left open.
- The database is `family-hub.db` inside `~/server_data/family-hub`. The path
  is decided in two places that must agree: `family_hub_dir` in the Ansible
  role, and the `-db` flag in this image's `CMD`. If they disagree, SQLite
  creates an empty file at the new path and the app comes up healthy and
  blank — so after any change there, check row counts, not the log.

## Verify after deploy

```sh
# On the VPS (or via your usual ssh shortcut):
docker logs --tail=50 family-hub     # expect "listening", "scheduler started"
```

A working scheduler logs `bot: scheduler started notify_chat=... reminder_delay_min=60`
on boot. `scheduler disabled` means `TELEGRAM_NOTIFY_CHAT` is missing. The
The digest ticker now hosts four wall-clock messages with separate gates, so
its boot line reports each: `bot: digests started appointment_digests=false
… reminder_nag=20:00 reminder_push=true school_digest=19:30` is the expected
prod shape — the appointment summaries stay off because Home Assistant sends
those, while the chore nag and the school timetable run from here. It only
falls back to `bot: digests disabled (NOTIFICATIONS_ENABLED not set, no
reminders)` when none is configured. The cost-prompt ticker should log
`bot: cost prompts started cost_prompt_delay_min=60`, and the billing reminder
`bot: billing reminders started`. Neither that one nor the pre-lesson warning
takes any configuration: how far ahead each course warns is its own
«Нагадати про оплату за» field, and like the other tickers they go quiet
without `TELEGRAM_NOTIFY_CHAT`.

The reminder materialiser logs `reminders: materialiser started tick=1m0s
backfill=720h0m0s` on boot, and does so even with the bot and every
notification switched off. Its catch-up is self-healing over that backfill
window: a container down across a reminder writes the missing row on its next
tick, with no watermark to reset by hand. A gap longer than the window leaves
those occurrences unrecorded for good — 30 days is far past any outage worth
tolerating silently, but it is a real limit, not a rounding error.

## Migrations

A snapshot is taken first. The Ansible role snapshots the database into
`~/server_data/backups/premigrate-family-hub-<stamp>.db.gz` before it runs
`/app/migrate`, keeping the last five and leaving the nightly snapshots alone
(they rotate on a different prefix). It exists because the two events happen on
different clocks: the nightly backup runs at 03:20, migrations run whenever
somebody deploys, and `0006` dropped three columns off `regular_slots` sixteen
hours after the newest snapshot. Nothing was lost that time because the
snapshot was taken by hand — which is a person remembering, not a control.

It is taken with SQLite's backup API rather than by copying the file: the app
is running and the database is in WAL mode, so a copy taken under a live writer
yields a file that only looks intact. The snapshot reaches Backblaze on the
next nightly mirror, not immediately, so the ten minutes after a migration are
covered on disk but not yet offsite.

To roll back a bad migration, stop the container, `gunzip` the snapshot over
`family-hub.db` (removing the `-wal` and `-shm` beside it), and start it again.

Migrations then run twice, on purpose. The Ansible role runs `/app/migrate` in a
throwaway container **before** it touches the running one, and the server also
migrates on boot.

The explicit step is the one that matters. A migration that fails on boot fails
inside a container the play has already created: docker reports it started, the
play goes green, and the app restarts forever while nothing says so. Run first,
a bad migration stops the play with the old container still serving.

The boot-time run stays as the second line: a container started by hand, or a
fresh install nobody deployed with Ansible, should not come up against a schema
it does not understand.

To migrate by hand:

```sh
docker run --rm -v ~/server_data/family-hub:/data \
  --entrypoint /app/migrate olegsmedyuk/family-hub:latest -db /data/family-hub.db
```

## The home-meters cutover

Done on 1 September 2026. Kept here because the app still shows the data it
moved, and because the shape of the operation is the reusable part: schema
first, copy second, writing screens third.

`home-meters` was a separate container with its own SQLite file, its own domain
`meters.conformist.name`, and its own scheduled messages into **the same**
family group. It is stopped and not to be restarted — see the divergence note
below.

The order it ran in, and why:

1. **Migration 0008 deployed alone.** Five empty tables in prod, no UI, no
   routes. The copy script inserts *literal ids*, so its correctness rests on
   the target tables being empty; deploying the schema by itself is the
   cheapest way to guarantee that.
2. **Both containers stopped, database snapshotted, script run.** Stopping
   `family-hub` is what lets the copy assume nothing is writing; stopping
   `home-meters` is what stops the two diverging from the moment the copy
   finishes.
3. **Writing screens deployed after.** Once the tables hold data an id
   conflict is no longer possible, so the forms could go out freely.

The script, run from the host, which has no `sqlite3` — hence python out of an
image that happens to carry one:

```sh
docker stop home-meters family-hub
cp -a ~/server_data/family-hub/family-hub.db \
      ~/server_data/family-hub/family-hub.db.pre-utilities-$(date +%Y%m%d-%H%M%S)

docker run --rm --entrypoint python3 \
  -v ~/server_data/home-meters:/src -v ~/server_data/family-hub:/dst \
  ghcr.io/mealie-recipes/mealie:v3.22.0 -c '
import sqlite3
src = sqlite3.connect("/src/meters.db"); src.row_factory = sqlite3.Row
dst = sqlite3.connect("/dst/family-hub.db")

def copy(sql, to, cols, rename=None):
    rows = [tuple(r[c] for c in cols) for r in src.execute(sql)]
    tgt = ",".join((rename or {}).get(c, c) for c in cols)
    dst.executemany("insert into %s (%s) values (%s)" % (to, tgt, ",".join("?"*len(cols))), rows)
    print(to, len(rows))

copy("select * from addresses", "addresses",
     ["id","name","comment","area","currency","active","sort_order","created_at"])
copy("select * from tariffs", "tariffs",
     ["id","name","kind","unit","rate1","rate2","effective_from","effective_to",
      "active","comment","created_at"])
copy("select * from services", "utilities",
     ["id","address_id","name","current_tariff_id","icon","color","active",
      "sort_order","comment","created_at","url"])
copy("select * from readings", "readings",
     ["id","service_id","tariff_id","period","reading_date","prev1","curr1",
      "prev2","curr2","consumed1","consumed2","amount","paid_date","comment","created_at"],
     rename={"service_id":"utility_id"})
dst.commit()
'

docker start family-hub
```

What it printed, and what to expect if it is ever replayed from a fresh copy of
the old database:

```
addresses 2
tariffs 17
utilities 11
readings 406
```

`notification_deliveries` was **not** copied. The two scheduled types never
moved (see below), and the event types that did are gone too — migration 0009
dropped `utility_deliveries` entirely once the automatic messages were replaced
by a button.

Checks run against the result, before restarting anything:

- `pragma quick_check` → `ok`, `pragma foreign_key_check` → no rows
- no dangling `readings.utility_id`, `readings.tariff_id`, `utilities.address_id`
- `sum(amount)` identical on both sides — `256220.82`
- periods `2022-03 … 2026-08`
- the family's own tables untouched: `appointments`, `school_lessons` unchanged

Ids are preserved verbatim. Remapping them would have been the one place those
406 rows could be silently corrupted, because `readings.tariff_id` points at
the tariff that applied *then* — so there is no remapping step at all.

### What deliberately did not move

- **`notification_settings`** — the old app's scheduled reminders. They are
  chores here (#5 «Записать данные счётчиков», #7 «Проверить начисления
  коммунальных в банке»), and a chore knows what a scheduled message cannot:
  whether it was actually done, what was missed, and what belongs in the
  calendar feed. Two mechanisms posting to one chat was the reason for the
  merge, not a detail of it.
- **`services.category`** — a catalogue whose only job was to fill in `icon`
  and `color`, which are columns on the utility itself. A lookup table that
  populates two fields is a second place for them to disagree.
- **The PNG renderer.** `internal/notify/image.go` drew the month as an image;
  it needed `gg`, `freetype` and `x/image`, and could not even draw the emoji
  the services carry, because freetype does not rasterise them. `/meters/report`
  is a page made to be screenshotted instead, and no `Notifier` gained a
  send-photo method.

### After the copy, the two apps diverge

Anything entered in the old app from 1 September onward does not appear here.
August was closed in `home-meters` before the copy — all 11 services entered
and paid — which is what made the date safe to bring forward by a day.
`home-meters` has stayed stopped since; restarting it would start a second,
invisible record of the same bills.

To retire it for good: remove the container and its Traefik router from the
dotfiles role, drop the `meters.conformist.name` DNS record, and keep
`~/server_data/home-meters/meters.db` as the archive — it is the only copy of
the old app's `notification_settings` and `category` data, neither of which
came across.
