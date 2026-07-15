# Troubleshooting

Diagnosis order: `droid-proxy doctor` first — it validates config and env
loading, probes the listen address, checks the managed service, and never
prints secret values. The scenarios below cover the failure modes doctor
reports and how to read them.

## `localhost` shows a different server (or nothing) while the proxy is healthy

droid-proxy binds **IPv4 loopback only** (`127.0.0.1:9787` by default). macOS
resolves `localhost` to the IPv6 address `::1` first, so any process listening
on `[::1]:9787` silently captures every `localhost:9787` request while the
proxy keeps serving normally on `127.0.0.1`.

The default port was moved from `8787` to `9787` to avoid conflicts with other
local tools. Known port squatters on the **old** default `8787` (historical
context — these tools still use `8787`, which is why droid-proxy moved away
from it):

- **Cursor's MCP OAuth loopback** — Cursor binds a temporary OAuth callback
  server on port 8787 whenever it (re)connects to remote MCP servers that use
  OAuth. It comes and goes with Cursor restarts and token refreshes.
- **`wrangler dev`** — Cloudflare's dev server defaults to port 8787.
- **Dask** — the Dask dashboard can occupy `8787` by default.

What to do:

- Always health-check with the IPv4 address, never `localhost`:

  ```bash
  curl -s http://127.0.0.1:9787/health
  ```

- `droid-proxy doctor` probes both stacks and prints
  `warning: a different server is listening on [::1]:9787` when a squatter is
  present, and flags a hard `health probe: issue:` when something other than
  droid-proxy answers on the configured address itself.
- Factory settings written by `droid-proxy config` already use
  `http://127.0.0.1:<port>`, so Droid keeps working through an IPv6 squatter.
- To avoid the collision entirely, move the proxy: change `listen.port` in
  `config.yaml`, run `droid-proxy restart`, then re-sync Factory settings
  (`droid-proxy config`, `S` on the dashboard) so `baseUrl` follows the port.
- If you are still on the old default `8787`, run
  `droid-proxy migrate-port --config <path>` (see
  [docs/UPGRADE.md](UPGRADE.md) for the port migration guide).

## Factory shows a model, but requests fail with "model not configured"

`config.yaml` is loaded once at startup. Model edits (by hand or through the
`droid-proxy config` TUI) do not apply to a running proxy.

- The 404 body says so explicitly when it detects the situation:
  `model "X" not configured (known: ...) — config.yaml changed since the proxy
  started; restart droid-proxy to apply it`.
- `droid-proxy doctor` prints `config: changed since the proxy started —
  restart droid-proxy to apply` under the same condition.
- Fix: `droid-proxy restart` (or `r` on the config TUI dashboard).

## `status` / `stop` under the managed service

With the per-user service installed (`droid-proxy setup --service`), the proxy
is supervised by launchd (macOS) or `systemd --user` (Linux) and is not the
pidfile-managed background daemon that `start` creates:

- `droid-proxy status` asks the service manager for the live state and prints
  `droid-proxy is running under the managed service (pid N)` even when the
  local pidfile is stale.
- `droid-proxy stop` stops through the service manager (`launchctl bootout` /
  `systemctl --user stop`) so KeepAlive cannot immediately resurrect the
  process. The service still starts again at next login; run
  `droid-proxy service uninstall` to remove it permanently, or
  `droid-proxy restart` to bring it back now.
- `droid-proxy doctor` reports both views: the `daemon:` line (pidfile) and
  `service state:` (service manager).

## `config error: model "X": env var Y is empty`

Run `droid-proxy config`, export the variable in your shell, or add it to
`.env.local`. Runtime env files are layered from `~/.droid-proxy/env` and then
`.env.local` when present. To verify exactly which config/env files load,
without printing secret values:

```bash
droid-proxy doctor --config config.yaml --env-file .env.local
```

## Droid says the model is offline

```bash
droid-proxy status
curl -s http://127.0.0.1:9787/health
curl -s http://127.0.0.1:9787/v1/models | jq '.data[].id'
droid-proxy logs -n 100
```

If `/v1/models` lists the model but Droid cannot reach it, re-sync Factory
settings from the config TUI and confirm the `baseUrl` host is `127.0.0.1`
with the configured port.
