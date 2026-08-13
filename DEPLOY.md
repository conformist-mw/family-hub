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
  `TZ=Europe/Kyiv`) comes from the role +
  `roles/family-hub/vars/secrets.sops.yaml`. Reminders only fire on prod
  because `TELEGRAM_NOTIFY_CHAT` is unset locally.
- The appointment digests stay off in prod (`NOTIFICATIONS_ENABLED` unset):
  Home Assistant sends those summaries from the ICS feed.
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
appointment digest ticker logs `bot: digests disabled (NOTIFICATIONS_ENABLED
not set)` — that line is expected in prod. The cost-prompt ticker should log
`bot: cost prompts started cost_prompt_delay_min=60`, and the billing reminder
`bot: billing reminders started`. Neither that one nor the pre-lesson warning
takes any configuration: how far ahead each course warns is its own
«Нагадати про оплату за» field, and like the other tickers they go quiet
without `TELEGRAM_NOTIFY_CHAT`.

## Migrations

Migrations run twice, on purpose. The Ansible role runs `/app/migrate` in a
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
