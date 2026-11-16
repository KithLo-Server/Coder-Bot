# OpenCode Telegram Bot

A Go Telegram bot that runs the [OpenCode](https://opencode.ai) CLI inside per-repository Docker worker containers. The bot itself runs in a privileged Docker-in-Docker base container, so no host Docker socket is exposed. It lets you plan, implement, run commands, transfer files, and manage Git repositories from a chat.

## Features

- **Repository management** — create, select, inspect, reload, and stop repositories from Telegram.
- **OpenCode sessions** — plan mode (`/ask`, plain text) and build mode (`/code`, `#`), with conversation continuity per chat and repository.
- **Execute Plan & Commit** — plan responses carry a button that re-runs OpenCode in build mode on the same session with an instruction to commit the result.
- **Model selection** — interactive `/model` picker and `/think` variant selection, driven by an `OPENCODE_MODELS` catalog.
- **Cancellable runs** — `/cancel` terminates the running process tree in the base, worker, or shell container that is running it.
- **File transfer** — `/dl` downloads files or directory archives from the active repository's shell container; `/ul` uploads through it, while photo uploads write through the base container.
- **Git sync** — `/pull` and `/push` operate directly on the base container's repository checkout.
- **Saved command sets** — `/cmd` records bot commands per chat, `/exec` replays them with argument placeholders.
- **Multi-chat support** — sessions, active repositories, and command sets are scoped per chat, including groups.

## Architecture

```
Telegram  ──►  base bot container (Go, privileged, runs its own dockerd)
                    │
                    │ inner /var/run/docker.sock
                    ├── per-repo worker: opencode_<repo>
                    │     image: ghcr.io/kithlo-server/coder-worker:latest
                    │     /workspace = /repos/<repo>
                    │     /opt/<mount>, /upload
                    │     /opencode/opencode.db (persisted)
                    │
                    └── per-repo shell: opencode_shell_<repo>
                          image: ghcr.io/kithlo-server/coder-shell:latest
                          /workspace = /repos/<repo>
                          /opt/<mount>, /upload
                          /sh only, no OpenCode/provider credentials

base binds:
  ${OPENCODE_DATA}/repos  -> /repos
  ${OPENCODE_DATA}/uploads -> /upload
  ${OPENCODE_DATA}/data   -> /app/data
  ${OPENCODE_DATA}/docker -> /var/lib/docker
```

- The **base container** runs the bot process and a Docker daemon. The bot talks to that inner daemon through `/var/run/docker.sock`; the host Docker socket is not mounted.
- Repository management and Git operations run directly in the base container against `/repos/<repo>`; `/dl` and `/ul` run in the repository's shell container against `/workspace`.
- Each repository gets a **worker** (`opencode_<repo>`) and **shell** (`opencode_shell_<repo>`), both created on demand through the base's inner daemon and reused once running.
- Only `/ask`, `/code`, plain text, `#`-prefixed text, and the OpenCode step of photo handling run in a worker. Only `/sh`, `/dl`, and `/ul` use a shell container.
- A **per-repository lock** serializes work, while different repositories can run in parallel.
- A **local Telegram Bot API server** is supported through `TELEGRAM_API_BASE_URL` and `TELEGRAM_API_FILE_BASE_URL`. Bot file transfers through `/dl` remain capped at 50 MB.

## Prerequisites

- Docker (with Compose) on the host.
- Support for privileged containers, required by the base's Docker-in-Docker daemon.
- A Telegram bot token from [@BotFather](https://t.me/BotFather).
- A DeepSeek API key (or another provider configured through the model catalog).
- A Docker network named `telegram_bot` if you use the provided `docker-compose.yml`.
- For the bundled Compose setup: a Telegram Bot API server reachable as `telegram_bot_api_server` on the `telegram_bot` network. It must be run separately; the Compose file references it but does not start it. Compose also expects `TELEGRAM_BOT_API_DATA` to point at an existing host directory.

## Quick start

1. Export the required variables and create the external network and directories used by Compose:

   ```bash
   export TELEGRAM_BOT_TOKEN="123456:ABC..."
   export TELEGRAM_ALLOWED_USER_ID="123456789"
   export OPENCODE_DATA="/srv/opencode"
   export OPENCODE_SSH_KEY="$(cat ~/.ssh/opencode_key)"
   export TELEGRAM_BOT_API_DATA="/srv/telegram-bot-api"
   export DEEPSEEK_API_KEY="sk-..."
   export OPENCODE_MODELS='{"version":1,"models":{...},"default_model":{"id":"..."}}'
   export GIT_USER_NAME="OpenCode Bot"
   export GIT_USER_EMAIL="bot@example.com"

   mkdir -p "$OPENCODE_DATA/data" "$OPENCODE_DATA/repos" "$OPENCODE_DATA/uploads" "$OPENCODE_DATA/docker" "$TELEGRAM_BOT_API_DATA"
   docker network create telegram_bot
   ```

2. Start the bot:

   ```bash
   docker compose up -d --build
   ```

   The bot image is built from the local `Dockerfile`; its runtime is based on Docker-in-Docker and includes Git, Bash, OpenSSH, and tar. Worker and shell images are pulled into the base's inner Docker daemon on demand. For local development, Go 1.26+ is required for the bot: `go build ./cmd/bot`.

3. Message the bot. Start with `/create <name> <git-url>` to clone a repository and select it. Its worker and shell containers start on first use.

## Configuration

The bot reads configuration from environment variables. The Compose file passes host variables such as `TELEGRAM_BOT_TOKEN` and `TELEGRAM_ALLOWED_USER_ID` into the bot.

### Required

| Variable | Description |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token. |
| `DEEPSEEK_API_KEY` | API key passed to OpenCode workers. |
| `OPENCODE_MODELS` | JSON model catalog. The bot refuses to start if it is missing or malformed. |

### Optional

| Variable | Default | Description |
| --- | --- | --- |
| `TELEGRAM_ALLOWED_USER_ID` | unset | Comma-separated Telegram user IDs allowed to use the bot. Unset/empty allows everyone. |
| `TELEGRAM_API_BASE_URL` | official API | Local Bot API server base URL, e.g. `http://telegram_bot_api_server:8081/bot`. |
| `TELEGRAM_API_FILE_BASE_URL` | official API | Local Bot API file URL, e.g. `http://telegram_bot_api_server:8081/file/bot`. |
| `OPENROUTER_API_KEY` | unset | Enables OpenRouter models in workers. |
| `GOOGLE_VERTEX_PROJECT` | unset | Enables Google Vertex AI in workers (also accepts `GOOGLE_CLOUD_PROJECT`). |
| `GOOGLE_VERTEX_LOCATION` | `global` | Vertex AI location. |
| `GOOGLE_APPLICATION_CREDENTIALS_JSON` | unset | Service-account JSON written into each worker; no credential file is stored on the host. |
| `REPOS_DIR` | `/repos` | Repository checkout directory inside the base container. Compose mounts `${OPENCODE_DATA}/repos` here. |
| `UPLOADS_DIR` | `/upload` | Shared upload directory inside the base container. Compose mounts `${OPENCODE_DATA}/uploads` here. |
| `OPENCODE_SSH_KEY` | unset | SSH private key material written into the base, worker, and shell containers for git auth and commit signing. |
| `DATA_DIR` | `/app/data` | Bot data directory. The SQLite state database lives here. |
| `GIT_USER_NAME` / `GIT_USER_EMAIL` | unset | Commit identity configured in the base, worker, and shell containers. |
| `TELEGRAM_READ_TIMEOUT` | `60` | Telegram read timeout (seconds). |
| `TELEGRAM_WRITE_TIMEOUT` | `300` | Telegram write timeout (seconds). |
| `TELEGRAM_CONNECT_TIMEOUT` | `30` | Telegram connect timeout (seconds). |
| `TELEGRAM_POOL_TIMEOUT` | `30` | Telegram connection pool timeout (seconds). |
| `TELEGRAM_MEDIA_WRITE_TIMEOUT` | `300` | Timeout for media/document uploads (seconds). |

The worker and shell images are fixed:

- `ghcr.io/kithlo-server/coder-worker:latest`
- `ghcr.io/kithlo-server/coder-shell:latest`

### Model catalog

`OPENCODE_MODELS` must be a JSON object with `version` set to `1`, a non-empty `models` object, and a `default_model` that references a listed model:

```json
{
  "version": 1,
  "models": {
    "DeepSeek": {
      "OpenRouter": {
        "deepseek/deepseek-flash": {
          "name": "DeepSeek Flash",
          "variants": ["low", "high", "max"]
        }
      }
    }
  },
  "default_model": {
    "id": "deepseek/deepseek-flash",
    "variant": "low"
  }
}
```

`variants` are provider-specific reasoning effort values shown by `/think` and the `/model` picker.

## Commands

| Command | Description |
| --- | --- |
| `/create <name> <git-url>` | Clone a repository in the base container (or adopt an existing matching checkout) and make it active. Its worker and shell containers start on first use. |
| `/set <name>` | Select an existing repository as the chat's active context. |
| `/info` | Show the active repository, worker and shell status, and model. |
| `/list` | List configured repositories and mark the active one. |
| `/mount <name>` | Mount another configured repository's checkout at `/opt/<name>` in the active repository's worker and shell. |
| `/unmount <name>` | Remove a repository previously mounted with `/mount` from the active worker and shell. |
| `/stop <name>` | Stop and remove the repository's worker and shell containers, keeping its bot state, OpenCode sessions, and checkout on disk. |
| `/purge <name>` | Delete the repository's checkout from disk using the base container, including checkouts of previously stopped or purged repos (refused if changes are not pushed, no remote can be verified, or the checkout is not a git repository). |
| `/reload <name>` | Stop and remove the worker and shell, refresh the checkout with a fast-forward pull in the base, and keep its OpenCode sessions. Containers start again on first use. |
| `/reloadall` | Stop and remove the containers and refresh the checkout of every repository, keeping their OpenCode sessions. Containers start again on first use. |
| `/upgrade` | Pull the latest worker and shell images, replace containers still running older images while keeping their OpenCode sessions, then prune unused old images. |
| `/dl <path>` | Download a file or a compressed directory (up to 50 MB) from the active checkout in the shell container. |
| `/ul <path>` | Upload an attached file to a destination in the active checkout through the shell container. |
| `/sh <command>` | Run a cancellable shell command in the active repository's shell container. |
| `/pull` | Pull the active repository with `--rebase`, aborting and reporting a conflict. |
| `/push` | Push the active repository, rebasing onto upstream first when it has new commits. |
| `/ask <prompt>` | Run OpenCode in plan mode (read-only). |
| `/code <prompt>` | Run OpenCode in build mode. |
| `/think [n\|variant\|off]` | Show or set the model's reasoning variant. |
| `/model` | Open the interactive model picker. |
| `/clear` | Start a fresh OpenCode session for the active repository. |
| `/cancel [repo]` | Cancel the running command in the active (or named) repository, restarting its child container if the process cannot be signalled. |
| `/cmd <name>` | Create, view, edit, or delete a saved command set for this chat. |
| `/exec <name> [args...]` | Run a saved command set in order. |

Plain text messages run plan mode. Text starting with `#` runs build mode with the `#` stripped. Photos are saved to `/upload/<name>` on the base container and inspected with OpenCode in the active worker.

Paths passed to `/dl`, `/ul`, and `/sh` are resolved relative to `/workspace` inside the active repository's shell container unless they start with `/`.

`/mount <name>` exposes another configured repository's checkout inside the active repository's worker and shell at `/opt/<name>`, so OpenCode sessions and `/sh` can read and edit it. Mounts are stored per repository and survive `/reload`. Because Docker bind mounts are fixed at container creation, mounting or unmounting recreates both child containers; OpenCode sessions and git state are preserved.

### Plan mode and commits

Plan responses (`/ask`, plain text, and photo analysis) include an **Execute Plan & Commit** button. Clicking it continues the same OpenCode session in build mode with the prompt `Execute the plan and git commit`. Build-mode commands do not show the button.

### Saved command sets

`/cmd <name>` collects one line per message until you press **Done**. Every saved line must be a supported bot command; plain shell lines and unknown commands are rejected. Use `/sh` to save a shell command. `/cancel` remains a collection control and is not saved. Sets are scoped per chat and persisted.

Use `%1`, `%2`, … in saved lines to insert `/exec` arguments, and `%%` for a literal `%`:

```text
/cmd deploy
/sh make %1
/sh git status
```

```text
/exec deploy release
```

## Project structure

```
cmd/bot/main.go        Startup validation, git setup, signals and polling
base-entrypoint.sh     Starts the inner Docker daemon before the bot
internal/config/       Environment configuration and the ordered model catalog
internal/state/        SQLite persistence, schema migrations and normalization
internal/opencode/     JSONL output parsing, session extraction and command building
internal/telegram/     Telegram interface, update conversion and Bot API adapter
internal/docker/       Docker interface and the official Docker SDK adapter
internal/base/         Bot-container filesystem/process executor and git setup
internal/locks/        Reentrant per-repository locks
internal/app/          Handlers, routing, persistent lookups and the in-memory run registry
  app.go               Shared state, lookups and the base-container adapter
  helpers.go           Authorization, response delivery and container environments
  container.go         Docker helpers, file transfer, workspace inspection, runs
  commands.go          Repository management, per-repo containers, /dl and /ul
  shell.go             /sh
  git_ops.go           /pull and /push
  sessions.go          /ask, /code, /think, /clear and text/photo handlers
  model_menu.go        Interactive /model picker
  command_sets.go      /cmd and /exec
  plan_execute.go      Execute Plan & Commit button
  runs.go              /cancel and forced child-container restarts
  router.go            Handler ordering and callback dispatch
*_test.go              Behaviour tests with Docker, base and Telegram stubbed
Dockerfile             Base bot image with a Docker engine runtime
docker-compose.yml
```

## Development and tests

The Go tests stub Docker, the base adapter, and Telegram through interfaces and run without external services:

```bash
go test ./...
gofmt -l .
go vet ./...
```

## Deployment

- **Bot** — `.github/workflows/deploy.yml` deploys on pushes to `master` (and via manual dispatch) using a self-hosted runner. It validates that every `${VAR}` referenced by `docker-compose.yml` is set, then runs `docker compose up -d --build --remove-orphans`.
- **Worker and shell images** — `ghcr.io/kithlo-server/coder-worker:latest` and `ghcr.io/kithlo-server/coder-shell:latest`; the bot pulls them into the base's inner Docker daemon on demand. The deployment workflow only pre-pulls images that are missing. Run `/upgrade` to pull newer versions, replace containers still on older images, and prune unused old images.

Secrets and variables are supplied through GitHub Actions secrets/variables, not committed to the repository.

## Data and security

- Repository definitions, per-chat active contexts, command sets, and OpenCode session IDs are persisted in the SQLite database `$DATA_DIR/repos_state.db` (WAL mode; `-wal` and `-shm` sidecar files may appear alongside it). Existing `repos_state.json` files are not migrated and are ignored. Pending prompts and running-command records live in memory and are lost on restart.
- Each worker's OpenCode database is persisted under `$DATA_DIR/opencode/<repo>/opencode.db` and mounted at `/opencode/opencode.db` through `OPENCODE_DB`. It survives `/stop`, `/reload`, `/reloadall`, `/mount`, `/unmount`, and `/upgrade`; `/purge` removes it.
- The base container is privileged because it runs its own Docker daemon. The host Docker socket is not mounted into the base, worker, or shell containers.
- Worker and shell containers are named `opencode_<repo>` and `opencode_shell_<repo>`. They are created on demand, removed by `/stop`, `/reload`, `/reloadall`, and `/purge`, and recreated on the next command that needs them.
- Worker containers receive provider credentials. Shell containers receive only Git/SSH configuration and no OpenCode or provider tokens.
- Set `TELEGRAM_ALLOWED_USER_ID` to restrict access. Leaving it unset allows any Telegram user who finds the bot to use it.
- `/upgrade` runs a system prune in the base's inner Docker daemon, so unrelated unused images there are removed as well.
- API keys, the Vertex service-account JSON, and the SSH signing key are passed through environment variables. The SSH key is written into the base and child containers at startup; no credential file is stored on the host.
- Repository files are untrusted input. Base commands operate on `/repos`, worker/shell commands operate on their mounted checkout, and `/upload` is read-only inside child containers.
- Inner Docker images and container metadata persist in `${OPENCODE_DATA}/docker`. Back up that directory alongside repositories, uploads, and `${OPENCODE_DATA}/data` if you need to preserve them.

## License

No license file is currently included in this repository.
