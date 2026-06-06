# Deploy runbook

How to ship a code change to production. The flow is: **build image → push to
Docker Hub → deploy via dotfiles Ansible role**. Slots/data live in the prod
SQLite DB and are *not* shipped this way — they are entered through the web UI
on prod.

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
#    roles/lessons/defaults/main.yml -> lessons_image.
docker build -t olegsmedyuk/lessons:latest .

# 2. Push to Docker Hub.
docker push olegsmedyuk/lessons:latest

# 3. Deploy. The role pulls :latest and recreates the container.
cd ~/dev/dotfiles
just deploy-hetzner-tag lessons
```

## Notes

- The image bundles `Доп. занятия.xlsx` as `seed.xlsx`. The Ansible role runs
  the importer **only on the first deploy** (gated by the absence of
  `lessons.db`). After that the prod DB is the source of truth — redeploys do
  not touch data.
- Migrations (`goose`) run automatically on container start.
- Prod env (token, webhook secret/path, `TELEGRAM_NOTIFY_CHAT`,
  `TELEGRAM_REMINDER_DELAY_MIN`, `TZ=Europe/Kyiv`) comes from the role +
  `roles/lessons/vars/secrets.sops.yaml`. Reminders only fire on prod because
  `TELEGRAM_NOTIFY_CHAT` is unset locally.

## Verify after deploy

```sh
# On the VPS (or via your usual ssh shortcut):
docker logs --tail=50 lessons        # expect "listening", "scheduler started"
```

A working scheduler logs `bot: scheduler started notify_chat=... reminder_hour=20`
on boot. `scheduler disabled` means `TELEGRAM_NOTIFY_CHAT` is missing.
