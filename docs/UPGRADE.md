# Upgrade guide

Use this guide for release installs and contributor source installs. Normal
users should install and upgrade from GitHub release assets; a source checkout is
only needed for development.

## Upgrade scenarios

| Scenario | Symptom | Safe path |
|---|---|---|
| Current release install | `droid-proxy doctor` reports `updater dry-run: skipped (release install)` | Re-run `curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh \| sh`. Existing config and secrets are preserved. |
| Current source install | `droid-proxy doctor` reports `updater dry-run: ok` | Run `droid-proxy update --dry-run --repo /path/to/droid-proxy --binary ~/.local/bin/droid-proxy`, then repeat without `--dry-run`. |
| Old updater binary | `droid-proxy update --dry-run` fails with `go.mod module is not droid-proxy` | Do the one-time manual rebuild below, then use the updater normally. |
| Stale user service | `doctor` reports missing config/env paths or a service executable inside a source checkout | Run `droid-proxy setup --service` from the installed release binary. |
| Mixed release/source install | `doctor --repo` is healthy but plain `doctor` reports a service issue | Keep the source checkout for development, but reinstall the service from `~/.local/bin/droid-proxy`. |

## Per-user runtime layout

The service should run from stable per-user install paths, not from a source
checkout.

| Item | macOS | Linux |
|---|---|---|
| Installed binary | `~/.local/bin/droid-proxy` | `~/.local/bin/droid-proxy` |
| Runtime config | `~/Library/Application Support/droid-proxy/config.yaml` | `${XDG_CONFIG_HOME:-~/.config}/droid-proxy/config.yaml` |
| User service | `~/Library/LaunchAgents/com.droid-proxy.agent.plist` | `${XDG_CONFIG_HOME:-~/.config}/systemd/user/droid-proxy.service` |
| Runtime state, logs, managed env | `~/.droid-proxy/` | `~/.droid-proxy/` |

Install or refresh the release binary and seed a config only when one does not
already exist:

```bash
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh
droid-proxy config
droid-proxy setup --service
```

Contributor source installs can still use:

```bash
cd ~/code/droid-proxy
make install-user
```

Add `~/.local/bin` to your shell `PATH` if needed. Run `droid-proxy config` to
onboard keys into `~/.droid-proxy/env`.

## One-time manual rebuild for old updater binaries

Some pre-public binaries only accepted `module droid-proxy`. Current source uses
`module github.com/trevoraspencer/droid-proxy`, so those binaries can fail before
they can update themselves.

```bash
cd /path/to/droid-proxy
git status --short
git fetch origin main
git merge --ff-only origin/main
make build
make install-user
./droid-proxy --version
droid-proxy doctor --repo "$(pwd)"
```

After this rebuild, `droid-proxy update --dry-run` should reach the printed plan
instead of failing module validation.

## Repair a user service

These commands preserve your config and managed secrets. They only remove and
reinstall the launchd plist or systemd user unit. The service should point at
the installed binary and the per-user runtime config, not files in the source
checkout.

```bash
droid-proxy doctor || true
droid-proxy service uninstall
droid-proxy setup --service
droid-proxy doctor || true
```

The service installer refuses missing configs before writing a service file, and
only includes `--env-file` when `.env.local` beside that config or
`~/.droid-proxy/env` exists.

## Verify after upgrading

```bash
droid-proxy --version
droid-proxy doctor || true
droid-proxy status
```

`doctor` exits nonzero when it finds hard install issues, such as a stale
service, but it does not print env file contents or secrets. For contributor
source checkouts, add `--repo ~/code/droid-proxy` to audit source freshness.
