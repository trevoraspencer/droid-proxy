# Upgrade Guide

Release installs are upgraded by re-running the GitHub release installer. Source installs are for contributors who want to build from the repository.

`cursor-proxy` and `deepseek-cursor-proxy` are retired; `droid-proxy` supersedes both.

## Release Installs

```bash
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh
droid-proxy doctor
```

The installer verifies `checksums.txt`, rejects archives with unsafe paths or link entries before extraction, replaces the binary at `~/.local/bin/droid-proxy`, and preserves existing config, OAuth tokens, logs, and managed secrets. It only creates a config when the target config file is missing.

If the proxy is already running during an upgrade, restart it after the new
binary is installed:

```bash
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh -s -- --restart
droid-proxy doctor
```

Interactive runs prompt before restarting a running proxy. Noninteractive runs
only restart when `--restart` is passed; use `--no-restart` to suppress the
prompt in scripted upgrades.

## Per-User Runtime Layout

| Item | macOS | Linux |
|---|---|---|
| Installed binary | `~/.local/bin/droid-proxy` | `~/.local/bin/droid-proxy` |
| Runtime config | `~/Library/Application Support/droid-proxy/config.yaml` | `${XDG_CONFIG_HOME:-~/.config}/droid-proxy/config.yaml` |
| User service | `~/Library/LaunchAgents/com.droid-proxy.agent.plist` | `${XDG_CONFIG_HOME:-~/.config}/systemd/user/droid-proxy.service` |
| Runtime state, logs, managed env | `~/.droid-proxy/` | `~/.droid-proxy/` |

The service should run from the installed binary path and the per-user runtime config, not from a development checkout.

## Service Repair

Use this when `droid-proxy doctor` reports a stale service executable, a missing config path, an invalid service config, or a missing env file referenced by the service.

```bash
droid-proxy doctor || true
droid-proxy service uninstall
droid-proxy setup --service
droid-proxy doctor || true
```

These commands preserve config, tokens, and managed secrets. They only remove and reinstall the launchd plist or systemd user unit.
If repair fails with `config is not ready to run`, run `droid-proxy config` to add at least one model or fix missing credential env vars, then repeat `droid-proxy setup --service`.

## Source Installs

Source installs are useful for development and testing local changes:

```bash
git clone https://github.com/trevoraspencer/droid-proxy.git
cd droid-proxy
make build
make install-user
droid-proxy doctor --repo "$(pwd)"
```

Source updater dry runs check for a clean repository, a fast-forwardable branch, and a target binary path before rebuilding:

```bash
droid-proxy update --dry-run --repo /path/to/droid-proxy --binary ~/.local/bin/droid-proxy
```

Repeat without `--dry-run` only after reviewing the plan.

## Verify After Upgrading

```bash
droid-proxy --version
droid-proxy doctor || true
droid-proxy status
```

`doctor` exits nonzero when it finds hard install issues. It does not print secrets or env file contents.
