# CLI reference

`droid-proxy` is a single binary with a foreground server mode, background
daemon commands, per-user macOS launchd and Linux systemd integration, and OAuth
login helpers.

## Synopsis

```text
droid-proxy [--config PATH] [--env-file PATH] [--foreground] [--version]

droid-proxy config [--config PATH]          # interactive dashboard (alias: onboard)
droid-proxy setup  [--config PATH] [--service]

droid-proxy start   [--config PATH] [--env-file PATH] [--foreground]
droid-proxy stop
droid-proxy status
droid-proxy restart [--config PATH] [--env-file PATH]
droid-proxy logs    [-n LINES] [PATH]

droid-proxy service install   [--config PATH]
droid-proxy service uninstall

droid-proxy doctor [--config PATH] [--env-file PATH] [--repo PATH]
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
- offers first-class Codex OAuth presets for GPT-5.6 Sol, Terra, and Luna,
  with standard and priority-tier local aliases that preserve the exact
  upstream model ID;
- prompts for the provider API key and stores it in `~/.droid-proxy/env`
  (chmod 600) — no manual `.env` editing; updates are line-oriented so your
  comments, blank lines, and unrelated keys in that file are preserved;
- discovers available models from the provider profile's model-list endpoint
  (OpenAI-compatible `/models`, Anthropic `/v1/models`, etc.) so you pick from
  a list instead of pasting a slug (falls back to manual entry);
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

**Config path** (for `start`, `restart`, `setup`, `service install`, and `auth`):

1. `config.local.yaml` in the current directory (if it exists)
2. `config.yaml` in the current directory (if it exists)
3. The config path recorded by the running proxy in `~/.droid-proxy/runtime.json`
4. The per-user config path, when it exists:
   `~/Library/Application Support/droid-proxy/config.yaml` on macOS, or
   `~/.config/droid-proxy/config.yaml` on Linux
5. `config.local.yaml` or `config.yaml` beside the `droid-proxy` executable
6. Otherwise `config.yaml`

Override with `--config /absolute/or/relative/path.yaml`.

**Env file** (for `start`, `restart`, `auth`, launchd, and systemd):

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
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh
droid-proxy config
droid-proxy setup --service
```

`droid-proxy setup` creates the per-user config if missing. The embedded install
template is intentionally minimal and never overwrites an existing config.

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
| `--foreground` | false | Run in foreground (also used internally by daemon/services) |
| `--version` | | Print version and commit identity, for example `droid-proxy 0.0.0-dev (414d18494e9a)` |

Logs go to stderr. No PID file is written unless `--foreground` is set (daemon
and user services use that flag internally).

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
| `status` | Prints the running PID; with a service installed it also queries launchctl/systemctl, so a proxy running under the managed service is reported even when the pidfile is stale |
| `restart` | Restarts the installed launchd/systemd user service when present; otherwise stops and starts the background daemon |
| `stop` | With a service installed, stops through the service manager (`launchctl bootout` / `systemctl --user stop`) so KeepAlive cannot resurrect the process; the service starts again at next login (`droid-proxy service uninstall` removes it). Without a service: SIGTERM, waits up to 10 seconds |
| `logs` | Tails the last 40 lines of `~/.droid-proxy/stderr.log` (override path as optional arg) |

`start` fails if another instance is already running. On success it prints a
health-check hint:

```bash
curl -s http://127.0.0.1:8787/health
```

Daemon and service logs:

- `~/.droid-proxy/stdout.log`
- `~/.droid-proxy/stderr.log`

## Per-user service

Auto-start on login and auto-restart on crash. Uses launchd on macOS and
`systemd --user` on Linux.

```bash
droid-proxy setup --service
droid-proxy service install --config "$HOME/Library/Application Support/droid-proxy/config.yaml"
droid-proxy service uninstall
```

`setup --service` creates the per-user config if missing, then writes and starts
the right user service for the current OS. `service install` is the lower-level
compatibility command. Both commands validate that the selected config can load
with the service env-file layers before writing the service; a seed-only or
invalid config is rejected with a prompt to run `droid-proxy config` first.
Durable service installs should run the binary from `~/.local/bin/droid-proxy`
and should not point at a source checkout.

| Detail | Value |
|--------|-------|
| macOS service | launchd label `com.droid-proxy.agent` |
| macOS path | `~/Library/LaunchAgents/com.droid-proxy.agent.plist` |
| Linux service | systemd user unit `droid-proxy.service` |
| Linux path | `${XDG_CONFIG_HOME:-~/.config}/systemd/user/droid-proxy.service` |
| Working directory | Directory containing the config file |
| Env file | Includes an absolute `--env-file` only when `.env.local` or `~/.droid-proxy/env` exists; missing env files are omitted |
| Logs | `~/.droid-proxy/stdout.log`, `~/.droid-proxy/stderr.log` |

The service runs `droid-proxy start --foreground --config <abs>` and only adds
`--env-file <abs>` for an existing env file. Live E2E env files are never
selected by default.

## Doctor

Diagnose a release or source install without printing env file contents or
secrets:

```bash
./droid-proxy doctor
./droid-proxy doctor --config config.yaml
./droid-proxy doctor --config config.yaml --env-file .env.local
./droid-proxy doctor --repo /path/to/droid-proxy
```

The doctor reports the executable path, resolved symlink target, CLI version and
commit, config/env load status, model count and `agent_ready` summary, source
checkout status when one is available, updater dry-run status for source
installs, daemon status, launchd/systemd service issues, and runnable service
configs using the env-file paths encoded in the service. It also probes
`/health` on the configured listen address (hard issue when a different server
answers or when nothing responds while the proxy reports running) and on
`[::1]:<port>` (soft warning when a foreign IPv6 listener shadows `localhost`
URLs), asks the service manager for the live service state (`service state:
running (pid N)`), and warns when `config.yaml` changed after the running proxy
loaded it. It names env files and env variable keys but never prints env file
contents or secret values. Missing
source checkouts are normal for release installs; pass `--repo` when you want a
source checkout audited. A missing implicit config is reported as a setup hint,
while an explicit `--config` path is treated as a hard diagnostic issue when it
cannot be read.

## Update from GitHub

Release installs are upgraded by re-running the release installer. Pass
`--restart` when a proxy is already running and should pick up the new binary
immediately:

```bash
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh -s -- --restart
```

`droid-proxy update` is for source checkouts. It updates from the GitHub
`origin/main` branch and rebuilds the local binary:

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
| `auth pool` | Shows Codex pool health (strategy, quota usage, affinity bindings, eligibility reason codes, rate-limit/error cooldowns, unhealthy recovery times, and `removed` entries whose token file was deleted while a request was in flight). Uses `GET /v1/oauth/pool-health` when the proxy is running; otherwise prints an offline snapshot from token files. |
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
| `stdout.log` / `stderr.log` | Daemon and user-service output |
| `env` | Managed secrets file written by `droid-proxy config` (chmod 600); always loaded as the base env layer |
| `auth/*.json` | OAuth token files |

The reasoning cache is **in-memory only** and is not stored here.

## See also

- [Configuration reference](CONFIG.md)
- [OAuth walkthrough](OAUTH.md)
- [Smoke tests](SMOKE.md)
