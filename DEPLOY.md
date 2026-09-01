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

- The image carries **no seed spreadsheet** — `Доп. занятия.xlsx` holds
  personal data and is gitignored + dockerignored. Prod was seeded long ago;
  the DB is the source of truth and redeploys never touch it. To seed a
  fresh install, copy the local Excel to the host and run the bundled
  importer against it once:
  `docker run --rm -v <dir>:/data -v "$PWD/Доп. занятия.xlsx":/seed.xlsx \
   --entrypoint /app/import olegsmedyuk/family-hub:latest -src /seed.xlsx -db /data/family-hub.db`
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
