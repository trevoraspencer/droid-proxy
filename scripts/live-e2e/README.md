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

The default Codex checks exercise both `gpt-5.6` and the local
`gpt-5.6-fast` alias. Both map to the credential-validated explicit
`gpt-5.6-sol` upstream; only the fast alias requests the priority service tier.
The harness verifies those loaded mappings before sending provider requests.
The GPT-5.6 mappings are fixed in the harness config and have no environment
override; the previous `CODEX_UPSTREAM_MODEL` name is retired and ignored so a
preserved GPT-5.2 env file cannot produce a false GPT-5.6 pass.
The xAI OAuth mappings are fixed for the same reason: `grok-4.5`,
`grok-build-0.1`, `grok-composer-2.5-fast`, and `grok-4.3` cannot be replaced
by stale `XAI_*_MODEL` environment overrides. Before any provider request, the
harness checks every loaded alias-to-upstream mapping. Grok 4.5 additionally
probes low, medium, and high reasoning with a stable `prompt_cache_key`.
The effective tier is account/backend dependent and appears in the response.
Model access depends on the authenticated account, plan, workspace policy, and
current usage limits; a 4xx is reported as a failure rather than silently
testing another model. To run the
higher-cost compatibility gate as well, set
`LIVE_E2E_CODEX_GPT56_ADVANCED=1` in the external secrets env file.
That probe sends `reasoning: {effort: "max"}` plus
`prompt_cache_options: {mode: "explicit"}`; success verifies that max effort
works and the proxy strips the unsupported cache options. It omits `mode: pro`,
which returned upstream 400 on the credentialed test accounts; mode
availability remains account/plan dependent.

## Notes

- `LIVE_E2E_ENV_FILE` must stay outside the repository.
- Run artifacts are stored under `~/.droid-proxy/live-e2e/`, not in the repo.
- Factory Droid UI validation after the automated run is manual.

## Safety behavior

- **Factory settings are merged, not replaced.** `06-write-factory-settings.sh`
  upserts only the live-e2e model aliases into `~/.factory/settings.json` and
  **preserves any unrelated custom models** you already configured. A timestamped
  backup is still written before each change, and the file stays `chmod 600`. The
  merge is idempotent (rerunning does not duplicate entries) and lives in the
  testable jq program `merge-custom-models.jq`.
- **Proxy cleanup is scoped by default.** `01-clean-old-proxies.sh` terminates
  only processes whose executable basename is exactly `droid-proxy`/`cursor-proxy`
  **or** that own a proxy port (8787/1455/56121); it never matches by a substring
  in a command line, and it never kills the current shell or its ancestors.
  Selection logic is the testable `select-proxy-kills.zsh`. To restore the old
  broad behavior (`pkill -f 'droid-proxy|cursor-proxy'`, which can hit any
  process with that string anywhere in its argv), set `LIVE_E2E_FORCE_KILL=1`.
