# Codex / ChatGPT OAuth

## Overview

| | |
|---|---|
| **Tier** | T1 OAuth — native Codex Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `codex-responses` |
| **When to use** | Codex models via your ChatGPT/Codex subscription (no API key) |

Uses browser PKCE login instead of an API key. See [OAUTH.md](../OAUTH.md) for
the full OAuth walkthrough.

## Prerequisites

- ChatGPT account with Codex access
- OAuth login completed: `./droid-proxy auth codex --config config.yaml`
  - On headless machines, use `./droid-proxy auth codex --config config.yaml --device`.

> Alternatively, run `./droid-proxy config` to add OAuth models and manage
> accounts interactively — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: gpt-5.2-codex
    display_name: "GPT-5.2 Codex (ChatGPT OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.2-codex
    max_output_tokens: 128000
```

Optional: pin a specific logged-in account:

```yaml
    oauth_account: user@example.com
```

## Multi-account load balancing (Codex only)

When multiple Codex accounts are logged in, the proxy selects among them using
a configurable strategy. Configure under `oauth.load_balancing` in `config.yaml`
(see [CONFIG.md](../CONFIG.md#oauthload_balancing)):

```yaml
oauth:
  load_balancing:
    strategy: sticky           # sticky (default), round-robin, fill-first, least-connections, random
    quota_soft_cap_percent: 80 # prefer accounts below this usage (0 = off)
    max_failovers: 2         # additional alternate-account attempts
    rate_limit_cooldown: 60s # cooldown after 429 without Retry-After or exhausted-window reset
    error_cooldown: 30s      # cooldown after 5xx or transport timeout
```

All fields have defaults; omit the block or individual fields to use defaults.
This only applies to Codex OAuth — xAI OAuth is always single-account.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "gpt-5.2-codex",
      "displayName": "GPT-5.2 Codex (ChatGPT OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000
    }
  ]
}
```

## Run

```bash
./droid-proxy auth codex --config config.yaml
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.2-codex",
    "input": "hello"
  }' | jq '.output'
```

## Manage accounts

```bash
./droid-proxy auth status codex                  # list accounts + expiry
./droid-proxy auth disable codex user@example.com
./droid-proxy auth logout  codex user@example.com
```

Check the model is logged in: `curl -s http://127.0.0.1:8787/v1/models | jq
'.data[] | select(.oauth_auth) | {id, oauth_auth}'`. See
[OAUTH.md](../OAUTH.md#managing-accounts) for the full reference.
For pool-level readiness, run `./droid-proxy auth pool --config config.yaml`;
the `STATUS` column explains skips such as `disabled`, `rate_limited`, or
`expired_no_refresh`.

## Notes

- Replace `upstream_model` with the Codex model ID your account supports.
- Tokens refresh automatically five minutes before expiry, with per-account locking to avoid concurrent refresh-token reuse.
- Codex requests include stable installation/session metadata, and token files may record quota telemetry from upstream that informs load-balancing eligibility.
- Ready-to-paste Factory snippet: [codex-oauth.json](../factory-settings/codex-oauth.json).
