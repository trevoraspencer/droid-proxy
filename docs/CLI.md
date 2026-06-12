# CLI reference

`droid-proxy` is a single binary with a foreground server mode, background
daemon commands, macOS launchd integration, and OAuth login helpers.

## Synopsis

```text
droid-proxy [--config PATH] [--env-file PATH] [--foreground] [--version]

droid-proxy config [--config PATH]          # interactive dashboard (alias: onboard)

droid-proxy start   [--config PATH] [--env-file PATH] [--foreground]
droid-proxy stop
droid-proxy status
droid-proxy restart [--config PATH] [--env-file PATH]
droid-proxy logs    [-n LINES] [PATH]

droid-proxy service install   [--config PATH]
droid-proxy service uninstall

droid-proxy update [--repo PATH] [--remote origin] [--branch main] [--binary PATH] [--no-restart] [--dry-run]

droid-proxy auth codex|xai [--config PATH] [--no-browser] [--device]
droid-proxy auth status [codex|xai] [--config PATH]
droid-proxy auth pool [--config PATH] [--url http://HOST:PORT]
droid-proxy auth enable|disable|logout <provider> <account> [--config PATH]
```

## Interactive config dashboard

The fastest way to add providers and models is the interactive TUI:

```bash
./droid-proxy config            # or: ./droid-proxy onboard
./droid-proxy config --config config.yaml
```

It is a full-screen dashboard that, from one place:

- lists configured models with the actual upstream provider (`known_auth`,
  OAuth provider, or custom host), upstream protocol, status badges (API key
  present, agent-ready, Factory-synced; OAuth account health for OAuth models),
  and proxy status;
- adds a model by picking a provider from the built-in registry (DeepSeek,
  Fireworks, Groq, Kimi, Z.AI, MiMo, xAI, OpenAI, Anthropic, Ollama, vLLM, …),
  a custom OpenAI-compatible endpoint, or Codex/xAI OAuth;
- prompts for the provider API key and stores it in `~/.droid-proxy/env`
  (chmod 600) — no manual `.env` editing; updates are line-oriented so your
  comments, blank lines, and unrelated keys in that file are preserved;
- discovers available models from the provider's `/models` endpoint so you pick
  from a list instead of pasting a slug (falls back to manual entry);
- writes the model to your YAML config (comments preserved);
- syncs the entry into Factory's `~/.factory/settings.json` (`s` for one, `S`
  for all), so you do not hand-edit `customModels`;
- manages OAuth accounts (`o`): browser or device login, enable/disable, logout;
- restarts the proxy (`r`) so changes take effect.

Keys onboarded here are written to `~/.droid-proxy/env`, which is always loaded
when the proxy starts (see env file resolution below), so they are picked up
even when a repo `.env.local` also exists. If both files define the same name,
the repo `.env.local` value wins.

## Config and env file resolution

**Config path** (for `start`, `restart`, `service install`, and `auth`):

1. `config.local.yaml` in the current directory (if it exists)
2. `config.yaml` in the current directory (if it exists)
3. The config path recorded by the running proxy in `~/.droid-proxy/runtime.json`
4. `config.local.yaml` or `config.yaml` beside the `droid-proxy` executable
5. Otherwise `config.yaml`

Override with `--config /absolute/or/relative/path.yaml`.

**Env file** (for `start`, `restart`, `auth`, and launchd):

API keys are loaded in layers, with later layers overriding earlier ones:

1. `~/.droid-proxy/env` — the managed secrets file written by
   `droid-proxy config` (always loaded as the base layer).
2. The repo env file `.env.local` in the config directory, or the `--env-file`
   path when given explicitly.

This means keys onboarded via `droid-proxy config` are available even when a
repo `.env.local` also exists, while `.env.local` can override matching names.
Env files use `KEY=value` or `export KEY=value` lines; `#` comments and blank
lines are ignored.

Recommended end-user setup:

```bash
cp config.example.yaml config.yaml
cp .env.local.example .env.local   # fill in your API keys
set -a && source .env.local && set +a
./droid-proxy start --config config.yaml
```

## Foreground server

Run the HTTP proxy in the current terminal. Useful for debugging.

```bash
./droid-proxy --config config.yaml
./droid-proxy --config config.yaml --env-file .env.local
./droid-proxy --foreground --config config.yaml   # same as default
./droid-proxy --version
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config.yaml` | Path to YAML config |
| `--env-file` | _(none)_ | Load API keys from this file before reading config |
| `--foreground` | false | Run in foreground (also used internally by daemon/launchd) |
| `--version` | | Print version and exit |

Logs go to stderr. No PID file is written unless `--foreground` is set (daemon
and launchd use that flag internally).

## Background daemon

Keep the proxy running without an open terminal.

```bash
./droid-proxy start --config config.yaml
./droid-proxy status
./droid-proxy restart
./droid-proxy stop
./droid-proxy logs
./droid-proxy logs -n 100
```

| Command | Description |
|---------|-------------|
| `start` | Detaches a child process, writes PID to `~/.droid-proxy/droid-proxy.pid` |
| `status` | Prints running PID or "not running" |
| `restart` | Restarts launchd when installed; otherwise stops and starts the background daemon |
| `stop` | Sends SIGTERM; waits up to 10 seconds |
| `logs` | Tails the last 40 lines of `~/.droid-proxy/stderr.log` (override path as optional arg) |

`start` fails if another instance is already running. On success it prints a
health-check hint:

```bash
curl -s http://127.0.0.1:8787/health
```

Daemon and launchd logs:

- `~/.droid-proxy/stdout.log`
- `~/.droid-proxy/stderr.log`

## macOS launchd service

Auto-start on login and auto-restart on crash. **macOS only.**

```bash
./droid-proxy service install --config "$(pwd)/config.yaml"
./droid-proxy service uninstall
```

| Detail | Value |
|--------|-------|
| LaunchAgent label | `com.droid-proxy.agent` |
| Plist path | `~/Library/LaunchAgents/com.droid-proxy.agent.plist` |
| Working directory | Directory containing the config file |
| Env file | Same resolution order as `start` (relative to config directory) |
| Logs | `~/.droid-proxy/stdout.log`, `~/.droid-proxy/stderr.log` |

The plist runs `droid-proxy start --foreground --config <abs> --env-file <resolved>`.

On Linux or other platforms, use `start` with your own process supervisor
(systemd user unit, `tmux`, etc.) — there is no built-in service installer.

## Update from GitHub

Update a source checkout from the GitHub `origin/main` branch and rebuild the
local binary:

```bash
./droid-proxy update --dry-run
./droid-proxy update
```

| Flag | Default | Description |
|------|---------|-------------|
| `--repo` | Current directory, then executable directory | Path to the `droid-proxy` source checkout |
| `--remote` | `origin` | Git remote to fetch |
| `--branch` | `main` | Branch to update from |
| `--binary` | Current executable | Binary path to replace after a successful build |
| `--no-restart` | false | Leave a running proxy alone after updating the binary |
| `--dry-run` | false | Print planned actions without fetching, merging, building, or restarting |

The updater is intentionally conservative: it refuses to run with uncommitted
files, untracked files, local-only commits, or a diverged branch. It fetches
from `https://github.com/trevoraspencer/droid-proxy`, fast-forwards only, builds
with Go, and atomically replaces the target binary. If the background proxy is
running, it restarts it after the new binary is installed unless `--no-restart`
is set.

## OAuth login

Browser PKCE login for Codex/ChatGPT and xAI accounts.

```bash
./droid-proxy auth codex --config config.yaml
./droid-proxy auth xai --config config.yaml
./droid-proxy auth codex --config config.yaml --no-browser
./droid-proxy auth codex --config config.yaml --device
```

| Flag | Description |
|------|-------------|
| `--config` | Config file (determines OAuth callback settings) |
| `--no-browser` | Print the authorization URL instead of opening a browser |
| `--device` | Use Codex device-code login. Codex only; useful for headless or remote machines. |

Tokens are saved under `oauth.auth_dir` (default `~/.droid-proxy/auth/`).
See [OAUTH.md](OAUTH.md) for the full walkthrough.

### OAuth account management

Inspect and manage stored OAuth accounts without re-running a login:

```bash
./droid-proxy auth status                       # all providers
./droid-proxy auth status codex                 # one provider
./droid-proxy auth disable xai user@example.com # stop using an account
./droid-proxy auth enable  xai user@example.com # re-enable it
./droid-proxy auth logout  codex user@example.com
```

| Command | Description |
|---------|-------------|
| `auth status [provider]` | Lists stored accounts with email, subject, account ID, expiry, last refresh, `disabled` flag, and token file path. Omit the provider to show both `codex` and `xai`. |
| `auth pool` | Shows Codex pool health (strategy, quota usage, affinity bindings, rate-limit/error cooldowns, and unhealthy recovery times). Uses `GET /v1/oauth/pool-health` when the proxy is running; otherwise prints an offline snapshot from token files. |
| `auth disable <provider> <account>` | Marks an account disabled. The proxy skips disabled accounts when selecting a token for requests. |
| `auth enable <provider> <account>` | Clears the disabled flag. |
| `auth logout <provider> <account>` | Deletes the account's token file from `oauth.auth_dir`. |

`<account>` is the same selector accepted by a model's `oauth_account`: an
email, subject (`sub`), account ID, or the token filename. The same actions are
available interactively from the `droid-proxy config` dashboard (press `o`).

## State directory

All persistent runtime files live under `~/.droid-proxy/`:

| File | Purpose |
|------|---------|
| `droid-proxy.pid` | Background daemon PID |
| `stdout.log` / `stderr.log` | Daemon and launchd output |
| `env` | Managed secrets file written by `droid-proxy config` (chmod 600); always loaded as the base env layer |
| `auth/*.json` | OAuth token files |

The reasoning cache is **in-memory only** and is not stored here.

## See also

- [Configuration reference](CONFIG.md)
- [OAuth walkthrough](OAUTH.md)
- [Smoke tests](SMOKE.md)
