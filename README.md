# Pi Telegram Gateway

A standalone Telegram bot integration service written in Go. It acts as
a gateway between Telegram and the [Pi coding agent](https://pi.dev)
running on your server: Telegram messages arrive via webhook, are
forwarded to a persistent Pi RPC subprocess, and the agent's final
answer is delivered back to the chat.

The gateway is provider-agnostic: Pi is started with whatever
`--provider`/`--model` you configure (or Pi's own defaults if unset),
and provider credentials travel through the environment untouched —
the gateway never reads them.

```
Telegram User
    |
    v
Telegram Bot API
    |  HTTPS webhook
    v
Nginx Reverse Proxy
    |
    v
Pi Telegram Gateway (this program, Go)
    |  JSONL over stdin/stdout
    v
Pi RPC Process (persistent child)
    |
    v
AI Provider (whatever Pi is configured with)
```

## Features

- **Telegram webhook integration** — `setWebhook` registration,
  secret-token validation (`X-Telegram-Bot-Api-Secret-Token`),
  POST-only endpoint, duplicate `update_id` suppression, immediate
  200 response with asynchronous processing.
- **User allowlist** — only explicitly configured numeric Telegram
  user IDs may talk to the agent. Everyone else gets a generic
  access-denied reply.
- **Persistent Pi process** — Pi is started and supervised as a child
  process in RPC mode (`--mode rpc`). Conversation context is kept
  across messages; the process is restarted with backoff if it dies.
- **Official Pi RPC protocol** — commands (`prompt`, `bash`,
  `get_state`, `abort`, `new_session`, `switch_session`,
  `get_last_assistant_text`) and the event stream are implemented per
  the [RPC documentation](https://pi.dev/docs/latest/rpc). A prompt
  response only means *accepted*; the gateway waits for
  `agent_settled` before sending the final assistant message.
- **Provider-agnostic** — Pi runs with the `--provider`/`--model` you
  configure (or its own defaults when unset); provider credentials and
  any other environment variables flow from the env file through the
  gateway into Pi's child environment, unchanged.
- **Session management** — Telegram chats are mapped to Pi session
  files (persisted in a small JSON state file). Sessions survive
  gateway and Pi restarts; `/new` starts fresh ones.
- **Sequential task queue** — prompts run one at a time so concurrent
  messages never interleave in the same session.
- **Telegram delivery** — long responses are split at the 4096
  character limit, MarkdownV2 escaping is applied correctly (with
  plain-text fallback), and messages are retried on transient errors.
- **Bash safeguards** — `/bash` refuses destructive commands
  (privilege escalation, shutdown/reboot, disk destruction, firewall/
  SSH/user/sudoer modification, fork bombs, recursive `rm` of absolute
  paths or wildcards).

## Project layout

```
my-pi-gram/
├── cmd/server/main.go        # entrypoint, HTTP server, shutdown
├── internal/
│   ├── config/               # environment configuration
│   ├── auth/                 # Telegram user allowlist
│   ├── queue/                # sequential task queue
│   ├── session/              # chat <-> Pi session mapping (JSON file)
│   ├── pi/                   # RPC protocol, client, process manager
│   └── telegram/             # webhook, routing, formatting, sender
└── .env.example
```

## Requirements

- Go 1.23+ (to build)
- Linux server (tested with systemd + Nginx)
- `pi` (the Pi coding agent) installed on the **server**, with RPC mode
  available (`pi --help` mentions `--mode rpc`)
- An API key for your Pi provider, if it uses one (delivered through
  the environment)
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- TLS certificate for the public hostname (e.g. via certbot)

> Pi is not needed on your development machine. The RPC protocol
> shapes used here follow the official Pi RPC documentation; after
> installing on the server, verify against your installed version:
> `pi --mode rpc --no-session` and `pi --help` (authoritative for the
> installed version).

## Installation

All steps run on the target server unless noted.

### 1. Create the dedicated user and directories

```bash
sudo useradd --system --create-home --shell /usr/sbin/nologin pi
sudo mkdir -p /var/lib/pi/sessions /etc/pi-telegram /home/pi/pi-telegram
sudo chown -R pi:pi /var/lib/pi/sessions /home/pi/pi-telegram
```

The gateway and Pi run as `pi`. Never run either as root.

### 2. Build the gateway

From your development machine (or on the server):

```bash
go build -o pi-telegram ./cmd/server
scp pi-telegram server:/home/pi/pi-telegram/
```

### 3. Configure the environment

```bash
sudo cp .env.example /etc/pi-telegram/pi-telegram.env
sudo nano /etc/pi-telegram/pi-telegram.env   # fill in real values
sudo chown pi:pi /etc/pi-telegram/pi-telegram.env
sudo chmod 600 /etc/pi-telegram/pi-telegram.env
```

Required values:

| Variable | Notes |
|---|---|
| `TELEGRAM_BOT_TOKEN` | from @BotFather |
| `TELEGRAM_WEBHOOK_SECRET` | `openssl rand -hex 32` |
| `TELEGRAM_WEBHOOK_URL` | e.g. `https://bot.example.com/telegram/webhook` |
| `TELEGRAM_ALLOWED_USER_IDS` | your numeric Telegram user ID (comma-separated for more) |

Find your numeric user ID by messaging [@userinfobot](https://t.me/userinfobot).

**Pi settings are passthrough, not gateway configuration.**
`PI_PROVIDER`, `PI_MODEL`, provider API keys and anything else in the
env file flow straight through to the Pi child process unchanged — the
gateway never reads provider credentials. When `PI_PROVIDER`/`PI_MODEL`
are unset, the corresponding `--provider`/`--model` flags are omitted
and Pi uses its own defaults. To give Pi additional variables later
(proxy settings, other credentials), just add them to the env file —
no code or configuration changes.

Optional variables and defaults are documented in `.env.example`
(timeouts, listen address, log level, state file).

### 4. Register the Telegram webhook

The gateway must reach `api.telegram.org` once to register. Load the
environment file with `-env-file` (values not already in the
environment are applied):

```bash
sudo -u pi /home/pi/pi-telegram/pi-telegram \
  -env-file /etc/pi-telegram/pi-telegram.env -register-webhook
```

This calls `setWebhook` with your `TELEGRAM_WEBHOOK_URL`, the secret
token, and `allowed_updates=["message"]`. To remove the webhook later:

```bash
sudo -u pi /home/pi/pi-telegram/pi-telegram \
  -env-file /etc/pi-telegram/pi-telegram.env -delete-webhook
```

The webhook registration persists on Telegram's side; you only need to
re-run it if the URL or secret changes.

Manual alternative (registration tool not required):

```bash
set -a; . /etc/pi-telegram/pi-telegram.env; set +a
curl "https://api.telegram.org/bot$TELEGRAM_BOT_TOKEN/setWebhook" \
  -d "url=$TELEGRAM_WEBHOOK_URL" \
  -d "secret_token=$TELEGRAM_WEBHOOK_SECRET" \
  -d 'allowed_updates=["message"]' -d "drop_pending_updates=true"
```

Note that `TELEGRAM_WEBHOOK_SECRET` is not issued by Telegram — you
generate it yourself (e.g. `openssl rand -hex 32`). Registering it
tells Telegram to echo it back in every webhook request's
`X-Telegram-Bot-Api-Secret-Token` header, which the gateway then
validates. Registration and validation read the same env file, so they
cannot mismatch.

### Running in the foreground

For debugging you can run the gateway outside systemd. Load the
environment file with `-env-file`, otherwise required variables and
provider credentials will be missing:

```bash
sudo -u pi /home/pi/pi-telegram/pi-telegram \
  -env-file /etc/pi-telegram/pi-telegram.env
```

Stop it with Ctrl+C (shutdown is graceful). Set `LOG_LEVEL=debug` in
the env file to also see Pi's stderr live.

## Telegram commands

| Command | Description |
|---|---|
| `/start` | Welcome message and instructions |
| `/help` | List commands |
| `/status` | Pi process status, busy state, model, session |
| `/new` | Start a new Pi conversation session |
| `/cancel` | Abort the current Pi task |
| `/ask <message>` | Send a natural-language prompt to Pi |
| `/bash <command>` | Execute a shell command directly (safeguarded) |

Any other **text message is treated as a natural-language prompt** (the
same as `/ask`). Only private chats are served; media messages get a
"only text supported" reply.

While a task runs you receive a short acknowledgement, lightweight
progress notes for the first few tool executions, and the final
assistant answer once Pi reports `agent_settled`.

## Testing

### Unit tests (no Telegram bot or API key needed)

```bash
go test ./...
go test -race ./...
```

The suite covers authorization, webhook secret validation, update
parsing, duplicate suppression, RPC (de)serialization, Pi event
processing against a fake RPC connection, process supervision and
crash diagnostics with fake Pi scripts, message splitting/escaping,
session persistence and queue behavior.

### End-to-end verification on the server

1. Health check (through Nginx and directly):

   ```bash
   curl -s https://bot.example.com/healthz
   curl -s http://127.0.0.1:8080/healthz
   ```

2. Webhook rejects wrong methods and bad secrets (expected: 405/403):

   ```bash
   curl -si https://bot.example.com/telegram/webhook | head -1
   curl -si -X POST -H 'Content-Type: application/json' \
     -H 'X-Telegram-Bot-Api-Secret-Token: wrong' \
     -d '{"update_id":1}' \
     https://bot.example.com/telegram/webhook | head -1
   ```

3. From Telegram, message your bot:
   - `/start` → welcome text
   - `/status` → Pi process state
   - `What operating system is this? Check the kernel version.` →
     Pi answers with the final response after a moment
   - `/bash df -h` → command output with exit code
   - `/bash rm -rf /` → refused by the safeguard
   - `/new`, `/cancel` → session control
4. Watch logs while testing:

   ```bash
   journalctl -u pi-telegram -f
   ```

Set `LOG_LEVEL=debug` to also see Pi's stderr diagnostics.

### Verifying the Pi RPC protocol on your install

```bash
sudo -u pi pi --mode rpc --no-session --session-dir /var/lib/pi/sessions
```

(Add `--provider`/`--model` if you use them.) Type a command and
confirm the response shape matches:

```json
{"id":"t1","type":"get_state"}
{"id":"t1","type":"response","command":"get_state","success":true,"data":{...}}
```

## How it works

- **Webhook flow**: Nginx → `POST /telegram/webhook` → method + secret
  validation → parse update → drop duplicates → `200 OK` → handle
  asynchronously.
- **Pi lifecycle**: the process manager spawns Pi with pipes (Pi
  inherits the gateway's full environment unchanged — provider
  credentials and all), reads stdout continuously (JSON records via a
  strict JSON decoder — no generic line reader), writes commands under
  a mutex (single writer), streams Pi's stderr at debug level and
  replays its tail at error level when a process dies within 30
  seconds of starting (so startup failures are visible at the default
  log level), and restarts Pi with exponential backoff (capped, reset
  after a stable run). On shutdown it closes stdin, waits briefly,
  then kills the process group.
- **Prompt lifecycle**: subscribe to events *before* sending (so fast
  completions are not missed) → send `prompt` → check
  `data.disposition` (`started`/`queued` → wait; `handled` → done, no
  run) → collect assistant text from `message_end` events → finish on
  `agent_settled` (or timeout, reporting partial output) → send to
  Telegram. If no text was observed, `get_last_assistant_text` is used
  as a fallback.
- **Sessions**: each Telegram chat is mapped to a Pi session file. The
  mapping and the last active session are persisted in
  `pi-telegram-state.json` (`STATE_FILE`). When a different chat is
  served, the gateway issues `switch_session`; after restarts the last
  active session is restored automatically.

## Security notes

This gateway lets an AI agent operate your server over Telegram.
Please read this section carefully.

- Run everything as the dedicated, unprivileged `pi` user (the systemd
  unit enforces this). Never as root.
- Pi RPC is **not** exposed on any network interface — it only exists
  as pipes to a child process. The gateway listens on loopback only.
- The Telegram bot token, webhook secret and provider API keys live in
  a `chmod 600` environment file and are never logged. The gateway
  does not even read provider credentials — they pass through to Pi's
  child environment unchanged.
- Only allowlisted user IDs reach Pi; the webhook validates the secret
  token on every request.
- `/bash` refuses destructive command families, but **filters are not
  a security boundary**. Real enforcement must be Linux permissions:
  do not grant `pi` unrestricted sudo. If sudo is needed at all,
  restrict it to explicitly authorized scripts via sudoers, e.g.
  `pi ALL=(root) NOPASSWD: /usr/local/sbin/allowed-script.sh`.
- Do not let the agent modify SSH configuration, firewall rules, user
  accounts or sudoers; the `/bash` guard blocks these families.
- Treat Telegram message content as untrusted input at all times.
- Pi's system prompt is not a security boundary either.

## Troubleshooting

| Symptom | Check |
|---|---|
| Bot does not respond | `journalctl -u pi-telegram -e`; is the webhook registered? `curl "https://api.telegram.org/bot$TOKEN/getWebhookInfo"` |
| `getWebhookInfo` shows errors | Nginx reachable? Certificate valid? Secret matches? |
| `/status` says process not running | `PI_BINARY` correct? `sudo -u pi $PI_BINARY --version`; watch restart logs |
| Pi crash-loops (`restarting pi process` repeats) | Look for `pi process exited quickly` in the log — it includes Pi's last stderr lines, which usually contain the reason. Common causes: missing/invalid provider API key in the environment, invalid `PI_MODEL`, unwritable `PI_SESSION_DIRECTORY`, or flags not supported by the installed pi version (`pi --help` is authoritative). Reproduce manually (see below). |
| Prompts time out | Raise `PI_PROMPT_TIMEOUT_SECONDS`; check the provider credentials work: run Pi interactively once |
| Lost context after restart | Check `STATE_FILE` exists and is writable by `pi`; the last session is restored at startup |
| 403 from the webhook | `TELEGRAM_WEBHOOK_SECRET` differs between the env file and the registration |

To reproduce a Pi startup failure outside the gateway, run Pi with the
exact same environment and arguments:

```bash
sudo -u pi bash -c 'set -a; . /etc/pi-telegram/pi-telegram.env; set +a; \
  "$PI_BINARY" --mode rpc --provider "$PI_PROVIDER" --model "$PI_MODEL" \
  --session-dir "$PI_SESSION_DIRECTORY"'
```

If Pi starts, it waits silently for JSONL commands on stdin (try
`{"id":"t1","type":"get_state"}` + Enter). If it exits instead, the
error is printed to your terminal — that is what the crash-loop hides.
