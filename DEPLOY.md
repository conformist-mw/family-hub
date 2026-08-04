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
   --entrypoint /app/import olegsmedyuk/family-hub:latest -src /seed.xlsx -db /data/lessons.db`
- Migrations (`goose`) run automatically on container start.
- Prod env (bot token, webhook secret/path, `TELEGRAM_NOTIFY_CHAT`,
  `TELEGRAM_REMINDER_DELAY_MIN`, `GEMINI_API_KEY`, `VISIT_PEOPLE`,
  `TZ=Europe/Kyiv`) comes from the role +
  `roles/family-hub/vars/secrets.sops.yaml`. Reminders only fire on prod
  because `TELEGRAM_NOTIFY_CHAT` is unset locally.
- The appointment digests stay off in prod (`NOTIFICATIONS_ENABLED` unset):
  Home Assistant sends those summaries from the ICS feed.
- The DB file is still `lessons.db` inside `~/server_data/lessons` — renaming
  it buys nothing and every runbook path points there.

## Verify after deploy

```sh
# On the VPS (or via your usual ssh shortcut):
docker logs --tail=50 family-hub     # expect "listening", "scheduler started"
```

A working scheduler logs `bot: scheduler started notify_chat=... reminder_delay_min=60`
on boot. `scheduler disabled` means `TELEGRAM_NOTIFY_CHAT` is missing. The
appointment digest ticker logs `bot: digests disabled (NOTIFICATIONS_ENABLED
not set)` — that line is expected in prod.
