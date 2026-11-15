# OpenCode Telegram Bot

A Go Telegram bot that runs the [OpenCode](https://opencode.ai) CLI inside per-repository Docker worker containers. It lets you plan, implement, run commands, transfer files, and manage Git repositories from a chat. This is a Go port of the original Python bot and keeps the same commands, state format and container topology.

## Features

- **Repository management** — create, select, inspect, reload, and delete repositories from Telegram.
- **OpenCode sessions** — plan mode (`/ask`, plain text) and build mode (`/code`, `#`), with conversation continuity per chat and repository.
- **Execute Plan & Commit** — plan responses carry a button that re-runs OpenCode in build mode on the same session with an instruction to commit the result.
- **Model selection** — interactive `/model` picker and `/think` variant selection, driven by an `OPENCODE_MODELS` catalog.
- **Cancellable runs** — `/cancel` terminates the running process tree in the container running it (the repository worker for OpenCode, or the shared shell for `/sh` and saved commands) and restarts that container if signalling fails.
- **File transfer** — `/dl` downloads files or directory archives; `/ul` and photo uploads copy files into the active checkout via the shared shell container.
- **Git sync** — `/pull` rebases the active checkout onto its upstream; `/push` rebases first when upstream has new commits.
- **Saved command sets** — `/cmd` records commands per chat, `/exec` replays them with argument placeholders.
- **Multi-chat support** — sessions, active repositories, and command sets are scoped per chat, including groups.

## Architecture

```
Telegram  ──►  bot container (Go, cmd/bot)
                    │  Docker socket
                    ▼
              shared shell container (opencode_shell)   ── /sh, /dl, /ul, git
                    │  /workspace bind mount
                    ▼
              host repos directory (one checkout per /workspace/<repo>)

              worker container per repository (ghcr.io/kithlo-server/coder-worker:latest)  ── OpenCode sessions
                    │  /workspace bind mount
                    ▼
              host checkout of the repository
```

- The **bot container** polls Telegram, stores state, and manages the shell and worker containers through `/var/run/docker.sock`.
- A single long-lived **shell container** (`opencode_shell`) hosts every repository checkout at `/workspace/<name>` and runs `/sh`, `/dl`, `/ul` and all git work (clone/pull/config).
- Each repository also gets a long-lived **worker container** named `opencode_<repo>` with the checkout bind-mounted at `/workspace`, used only for OpenCode sessions (`/ask`, `/code`, plan/build runs).
- A **per-repository lock** serializes container work, while different repositories can run in parallel.
- A **local Telegram Bot API server** is supported through `TELEGRAM_API_BASE_URL` and `TELEGRAM_API_FILE_BASE_URL`, and the bundled Compose file points at one. The server itself is not defined by this repository's Compose file. Bot file transfers through `/dl` remain capped at 50 MB.

## Prerequisites

- Docker (with Compose) on the host.
- A Telegram bot token from [@BotFather](https://t.me/BotFather).
- A DeepSeek API key (or another provider configured through the model catalog).
- A Docker network named `telegram_bot` if you use the provided `docker-compose.yml`.
- For the bundled Compose setup: a Telegram Bot API server reachable as `telegram_bot_api_server` on the `telegram_bot` network. It must be run separately; the Compose file references it but does not start it. Compose also expects `TELEGRAM_BOT_API_DATA` to point at an existing host directory.

## Quick start

1. Export the required variables and create the external network and directories used by Compose:

   ```bash
   export OPENCODE_BOT_TOKEN="123456:ABC..."
   export OPENCODE_ALLOWED_USER_ID="123456789"
   export OPENCODE_DATA="/srv/opencode"
   export OPENCODE_SSH_KEY="$(cat ~/.ssh/opencode_key)"
   export TELEGRAM_BOT_API_DATA="/srv/telegram-bot-api"
   export DEEPSEEK_API_KEY="sk-..."
   export OPENCODE_MODELS='{"version":1,"models":{...},"default_model":{"id":"..."}}'
   export GIT_USER_NAME="OpenCode Bot"
   export GIT_USER_EMAIL="bot@example.com"

   mkdir -p "$OPENCODE_DATA/data" "$OPENCODE_DATA/repos" "$TELEGRAM_BOT_API_DATA"
   docker network create telegram_bot
   ```

   The bundled Compose file expects a Telegram Bot API server running separately as `telegram_bot_api_server` on the `telegram_bot` network. Alternatively, change the `TELEGRAM_API_BASE_URL` and `TELEGRAM_API_FILE_BASE_URL` values in `docker-compose.yml` to use the official API.

2. Start the bot:

   ```bash
   docker compose up -d --build
   ```

   The shell and worker images are pulled from `ghcr.io/kithlo-server`. The bot image itself is a multi-stage Go build produced by Compose from the local `Dockerfile`. For local development, Go 1.26+ is required for the bot: `go build ./cmd/bot`.

3. Message the bot. Start with `/create <name> <git-url>` to clone a repository and select it.

## Configuration

The bot reads configuration from environment variables. The Compose file maps host variables such as `OPENCODE_BOT_TOKEN` and `OPENCODE_ALLOWED_USER_ID` into the bot's variables.

### Required

| Variable | Description |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token. Compose supplies it from `OPENCODE_BOT_TOKEN`. |
| `DEEPSEEK_API_KEY` | API key passed to OpenCode workers. |
| `OPENCODE_MODELS` | JSON model catalog. The bot refuses to start if it is missing or malformed. |

### Optional

| Variable | Default | Description |
| --- | --- | --- |
| `ALLOWED_USER_ID` | unset | Comma-separated Telegram user IDs allowed to use the bot. Unset/empty allows everyone. Compose supplies it from `OPENCODE_ALLOWED_USER_ID`. |
| `TELEGRAM_API_BASE_URL` | official API | Local Bot API server base URL, e.g. `http://telegram_bot_api_server:8081/bot`. |
| `TELEGRAM_API_FILE_BASE_URL` | official API | Local Bot API file URL, e.g. `http://telegram_bot_api_server:8081/file/bot`. |
| `OPENROUTER_API_KEY` | unset | Enables OpenRouter models in workers. |
| `GOOGLE_VERTEX_PROJECT` | unset | Enables Google Vertex AI in workers (also accepts `GOOGLE_CLOUD_PROJECT`). |
| `GOOGLE_VERTEX_LOCATION` | `global` | Vertex AI location. |
| `GOOGLE_APPLICATION_CREDENTIALS_JSON` | unset | Service-account JSON written into each worker; no credential file is stored on the host. |
| `HOST_REPOS_DIR` | `/repos` | Host directory containing repository checkouts. Compose uses `${OPENCODE_DATA}/repos`; each checkout ends up at `/workspace/<name>` in the shell container. |
| `OPENCODE_SSH_KEY` | unset | SSH private key material written into the shell/worker containers for git auth and commit signing. Consumed by the entrypoint at container startup. |
| `DATA_DIR` | `/app/data` | Bot data directory. The state file lives here. |
| `GIT_USER_NAME` / `GIT_USER_EMAIL` | unset | Commit identity configured in every worker. |
| `TELEGRAM_READ_TIMEOUT` | `60` | Telegram read timeout (seconds). |
| `TELEGRAM_WRITE_TIMEOUT` | `300` | Telegram write timeout (seconds). |
| `TELEGRAM_CONNECT_TIMEOUT` | `30` | Telegram connect timeout (seconds). |
| `TELEGRAM_POOL_TIMEOUT` | `30` | Telegram connection pool timeout (seconds). |
| `TELEGRAM_MEDIA_WRITE_TIMEOUT` | `300` | Timeout for media/document uploads (seconds). |

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
| `/create <name> <git-url>` | Clone a repository (or adopt an existing matching checkout), create its worker, and make it active. |
| `/set <name>` | Select an existing repository as the chat's active context. |
| `/info` | Show the active repository, container status, and model. |
| `/list` | List configured repositories and mark the active one. |
| `/mount <name>` | Mount another configured repository's checkout at `/opt/<name>` in the active repository's worker. |
| `/unmount <name>` | Remove a repository previously mounted with `/mount` from the active worker. |
| `/delete <name>` | Remove the repository's worker container and bot state, leaving its checkout on disk. |
| `/purge <name>` | Delete the repository's checkout from disk, including checkouts of previously deleted repos (refused if changes are not pushed, no remote can be verified, or the checkout is not a git repository). |
| `/reload <name>` | Recreate the worker container, refresh the checkout with a fast-forward pull, and reset its OpenCode sessions. |
| `/reloadall` | Recreate the worker container and refresh the checkout of every repository, resetting their OpenCode sessions. |
| `/dl <path>` | Download a file or a compressed directory (up to 50 MB) from the active checkout in the shared shell container. |
| `/ul <path>` | Upload an attached file to a destination in the active checkout via the shared shell container. |
| `/sh <command>` | Run a cancellable shell command in the shared shell container. |
| `/pull` | Pull the active repository with `--rebase`, aborting and reporting a conflict. |
| `/push` | Push the active repository, rebasing onto upstream first when it has new commits. |
| `/ask <prompt>` | Run OpenCode in plan mode (read-only). |
| `/code <prompt>` | Run OpenCode in build mode. |
| `/think [n\|variant\|off]` | Show or set the model's reasoning variant. |
| `/model` | Open the interactive model picker. |
| `/clear` | Start a fresh OpenCode session for the active repository. |
| `/cancel [repo]` | Cancel the running command in the active (or named) repository, restarting its container if the process cannot be signalled. |
| `/cmd <name>` | Create, view, edit, or delete a saved command set for this chat. |
| `/exec <name> [args...]` | Run a saved command set in order. |

Plain text messages run plan mode. Text starting with `#` runs build mode with the `#` stripped. Photos are uploaded to the workspace and inspected with OpenCode.

`/mount <name>` exposes another configured repository's checkout inside the active repository's worker container at `/opt/<name>`, so OpenCode sessions can read and edit it. Mounts are stored per repository and survive `/reload`. Because Docker bind mounts are fixed at container creation, mounting or unmounting recreates the worker container and starts a fresh OpenCode session (git state is untouched). The shared shell container already exposes every repository at `/workspace/<name>`, so `/sh`, `/dl`, and `/ul` do not need a mount.

### Plan mode and commits

Plan responses (`/ask`, plain text, and photo analysis) include an **Execute Plan & Commit** button. Clicking it continues the same OpenCode session in build mode with the prompt `Execute the plan and git commit`. Build-mode commands do not show the button.

### Saved command sets

`/cmd <name>` collects one line per message until you press **Done**. Lines beginning with a supported bot command are dispatched to that command's handler; anything else runs as a shell command in the shared shell container. `/cmd` and `/exec` are not re-dispatched from saved lines, so they run in the shell like other text. Sets are scoped per chat and persisted.

Use `%1`, `%2`, … in saved lines to insert `/exec` arguments, and `%%` for a literal `%`:

```text
/cmd deploy
/sh make %1
git status
```

```text
/exec deploy release
```

## Project structure

```
cmd/bot/main.go        Startup validation, signal handling and polling
internal/config/       Environment configuration and the ordered model catalog
internal/state/        Version-3 JSON persistence, migrations and normalization
internal/ordered/      Insertion-ordered maps used for stable catalog/repo order
internal/opencode/     JSONL output parsing, session extraction and command building
internal/telegram/     Telegram interface, update conversion and Bot API adapter
internal/docker/       Docker interface and the official Docker SDK adapter
internal/locks/        Reentrant per-repository locks
internal/app/          App state, handlers, routing and the in-memory run registry
  app.go               Shared state and lookups
  helpers.go           Authorization, response delivery and worker environment
  container.go         Docker helpers, file transfer, workspace inspection, runs
  commands.go          Repository management, /dl and /ul
  shell.go             /sh
  git_ops.go           /pull and /push
  sessions.go          /ask, /code, /think, /clear and text/photo handlers
  model_menu.go        Interactive /model picker
  command_sets.go      /cmd and /exec
  plan_execute.go      Execute Plan & Commit button
  runs.go              /cancel and forced container restarts
  router.go            Handler ordering and callback dispatch
*_test.go              Behaviour tests with Docker and Telegram stubbed
Dockerfile             Bot image (multi-stage Go build)
docker-compose.yml
```

## Development and tests

The Go tests stub Docker and Telegram through interfaces and run without external services:

```bash
go test ./...
gofmt -l .
go vet ./...
```

## Deployment

- **Bot** — `.github/workflows/deploy.yml` deploys on pushes to `master` (and via manual dispatch) using a self-hosted runner. It validates that every `${VAR}` referenced by `docker-compose.yml` is set, then pulls the worker image and runs `docker compose up -d --build --remove-orphans`.
- **Shell and worker images** — pulled from `ghcr.io/kithlo-server/coder-shell:latest` and `ghcr.io/kithlo-server/coder-worker:latest`; they are not built by this repository. The bot pulls the worker image on demand if it is missing locally.

Secrets and variables are supplied through GitHub Actions secrets/variables, not committed to the repository.

## Data and security

- Repository definitions, per-chat active contexts, and command sets are persisted in `$DATA_DIR/repos_state.json` (state version 3). The Go port reads and writes the same format as the Python version. OpenCode session IDs, pending prompts, and running-command records live in memory and are lost on restart.
- Worker containers are long-lived and named `opencode_<repo>`; deleting a repository removes its container. The shared shell container is named `opencode_shell`.
- The bot mounts the Docker socket, which grants effective root-equivalent access to the host. Run it only in a trusted environment.
- Set `ALLOWED_USER_ID` to restrict access. Leaving it unset allows any Telegram user who finds the bot to use it.
- API keys, the Vertex service-account JSON, and the SSH signing key are passed through environment variables. The service-account JSON is written into each worker by its entrypoint, and the SSH key is written into both shell and worker containers by their entrypoints at startup. Neither is stored on the host.
- Repository files are untrusted input; shell and worker containers should be treated as having access to the mounted checkout and nothing more privileged than that.

## License

No license file is currently included in this repository.
