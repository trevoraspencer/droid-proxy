# Live E2E Manual Secret and OAuth Steps

Run these after the scaffold is generated and before asking Codex to perform
the final live end-to-end test.

## 1. Generate local files

```bash
export LIVE_E2E_ENV_FILE="$PWD/.factory/validation/live-e2e/secrets.env"
scripts/live-e2e/00-preflight.sh
scripts/live-e2e/01-clean-old-proxies.sh
scripts/live-e2e/02-generate-config.sh
```

This creates:

- `config.local.yaml`
- the env file at `$LIVE_E2E_ENV_FILE`
- `.factory/validation/live-e2e/<run-id>/`
- `.factory/validation/live-e2e/<run-id>/current-run.env`

## 2. Add provider API keys

Edit the file at `$LIVE_E2E_ENV_FILE` and fill these names with real local
values:

```bash
export DEEPSEEK_API_KEY="..."
export ZAI_CODING_API_KEY="..."
export FIREWORKS_API_KEY="..."
export FIREWORKS_MODEL="..."
```

For Z.AI, the live E2E uses `known_auth: zai-coding-api`, which targets the GLM
Coding Plan endpoint. Use `ZAI_MAIN_API_KEY` only for normal Z.AI API configs
with `known_auth: zai-main-api`. `ZAI_API_KEY` remains available only for legacy
configs that still use `known_auth: zai`.

```bash
# Optional, not used by the live E2E default model:
export ZAI_MAIN_API_KEY="..."
export ZAI_API_KEY="..."
```

For Xiaomi MiMo, use normal API access:

```bash
export MIMO_API_KEY="..."
export MIMO_KNOWN_AUTH="mimo"
export MIMO_MODEL="mimo-v2.5-pro"
```

Or use one Token Plan region:

```bash
export MIMO_TOKEN_PLAN_CN_API_KEY="..."
export MIMO_KNOWN_AUTH="mimo-token-plan-cn"
```

```bash
export MIMO_TOKEN_PLAN_SGP_API_KEY="..."
export MIMO_KNOWN_AUTH="mimo-token-plan-sgp"
```

```bash
export MIMO_TOKEN_PLAN_AMS_API_KEY="..."
export MIMO_KNOWN_AUTH="mimo-token-plan-ams"
```

Optional model overrides, if your account exposes different current model IDs:

```bash
export CODEX_UPSTREAM_MODEL="gpt-5.2-codex"
export XAI_GROK_BUILD_MODEL="grok-build-0.1"
export XAI_GROK_MODEL="grok-4.3"
export ZAI_GLM_MODEL="glm-5.1"
export DEEPSEEK_MODEL="deepseek-v4-flash"
```

Do not paste API keys into `config.local.yaml`.

## 3. Build once and start the proxy

```bash
scripts/live-e2e/03-build-and-start.sh
```

You may leave the proxy running for OAuth setup. The final runner records a
single run id and will stop/restart any previous `droid-proxy` process started
from this repo with `config.local.yaml`.

## 4. Complete OAuth

Run the browser PKCE login for Codex/ChatGPT. Use the helper so the same
`LIVE_E2E_ENV_FILE` values are loaded before config validation:

```bash
scripts/live-e2e/auth-codex.sh
```

Complete the browser login and consent flow. The callback uses:

```text
http://localhost:1455/auth/callback
```

Run the browser PKCE login for xAI:

```bash
scripts/live-e2e/auth-xai.sh
```

Complete the browser login and consent flow. The callback uses:

```text
http://127.0.0.1:56121/callback
```

Verify local token storage:

```bash
scripts/live-e2e/04-check-oauth-ready.sh
```

If you run the raw `./droid-proxy auth ...` commands manually, source the env
file first in that terminal:

```bash
source "$LIVE_E2E_ENV_FILE"
```

Expected:

- `~/.droid-proxy/auth` is mode `700`.
- Token JSON files are mode `600`.
- At least one `codex` token and one `xai` token exist.

## 5. Tell Codex secrets are ready

After the API key env file is filled and both OAuth logins have completed,
tell Codex:

```text
I've added all secrets and completed OAuth. Run the final live E2E.
```

Codex can then run:

```bash
export LIVE_E2E_ENV_FILE="$PWD/.factory/validation/live-e2e/secrets.env"
scripts/live-e2e/run-all-after-secrets.sh
```

That final run performs build/test/vet, restarts the proxy, checks OAuth
storage, runs direct provider tests, checks OAuth refresh, runs safe negative
redaction cases, rewrites Factory Droid custom models, generates the Factory
Droid manual evidence checklist, and scans saved evidence for literal secret
leaks.

Factory Droid app validation remains manual. Use the generated
`.factory/validation/live-e2e/<run-id>/factory-manual-evidence.md` prompts after
the final runner completes.
