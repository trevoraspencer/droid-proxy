# Upgrade Guide

Release installs are upgraded by re-running the GitHub release installer. Source installs are for contributors who want to build from the repository.

## Release Installs

```bash
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh
droid-proxy doctor
```

The installer replaces the binary at `~/.local/bin/droid-proxy` and preserves existing config, OAuth tokens, logs, and managed secrets. It only creates a config when the target config file is missing.

## Per-User Runtime Layout

| Item | macOS | Linux |
|---|---|---|
| Installed binary | `~/.local/bin/droid-proxy` | `~/.local/bin/droid-proxy` |
| Runtime config | `~/Library/Application Support/droid-proxy/config.yaml` | `${XDG_CONFIG_HOME:-~/.config}/droid-proxy/config.yaml` |
| User service | `~/Library/LaunchAgents/com.droid-proxy.agent.plist` | `${XDG_CONFIG_HOME:-~/.config}/systemd/user/droid-proxy.service` |
| Runtime state, logs, managed env | `~/.droid-proxy/` | `~/.droid-proxy/` |

The service should run from the installed binary path and the per-user runtime config, not from a development checkout.

## Service Repair

Use this when `droid-proxy doctor` reports a stale service executable, a missing config path, or a missing env file referenced by the service.

```bash
droid-proxy doctor || true
droid-proxy service uninstall
droid-proxy setup --service
droid-proxy doctor || true
```

These commands preserve config, tokens, and managed secrets. They only remove and reinstall the launchd plist or systemd user unit.

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
