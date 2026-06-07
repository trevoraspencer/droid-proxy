# Live E2E (maintainers)

Optional end-to-end validation against real provider credentials. Not required
for normal use or for running unit tests.

## Prerequisites

- Go 1.26.4+, `zsh`, `jq`, `curl`, `rg`
- API keys and OAuth accounts you are willing to use for testing
- Factory Droid installed (`~/.factory/settings.json`)

## Setup

1. Choose a secrets file **outside the repo** (never commit this):

```bash
mkdir -p ~/.droid-proxy/live-e2e
export LIVE_E2E_ENV_FILE="$HOME/.droid-proxy/live-e2e/secrets.env"
```

2. Run the scaffold:

```bash
scripts/live-e2e/00-preflight.sh
scripts/live-e2e/01-clean-old-proxies.sh
scripts/live-e2e/02-generate-config.sh
```

This creates `config.local.yaml` (gitignored) and run artifacts under
`~/.droid-proxy/live-e2e/<run-id>/`.

3. Fill `secrets.env` with provider API keys. See `.env.local.example` for env
   var names. Do not paste keys into `config.local.yaml`.

4. Build and start the proxy:

```bash
scripts/live-e2e/03-build-and-start.sh
```

5. Complete OAuth (Codex and xAI):

```bash
scripts/live-e2e/auth-codex.sh
scripts/live-e2e/auth-xai.sh
scripts/live-e2e/04-check-oauth-ready.sh
```

6. Run the full suite:

```bash
export LIVE_E2E_ENV_FILE="$HOME/.droid-proxy/live-e2e/secrets.env"
scripts/live-e2e/run-all-after-secrets.sh
```

Results land in `~/.droid-proxy/live-e2e/<run-id>/results.ndjson`.

## Notes

- `LIVE_E2E_ENV_FILE` must stay outside the repository.
- Run artifacts are stored under `~/.droid-proxy/live-e2e/`, not in the repo.
- Factory Droid UI validation after the automated run is manual.
